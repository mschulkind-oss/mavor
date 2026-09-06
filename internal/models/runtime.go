package models

import "fmt"

// Runtime is the inference library that executes a model. mavor has exactly
// two, and a runtime is never configured: it is a property of the model,
// recorded in the catalog. See docs/design/configuration-surface.md §3.
type Runtime string

const (
	// RuntimeWhisper is whisper.cpp, which reads GGML model files.
	RuntimeWhisper Runtime = "whisper.cpp"
	// RuntimeSherpa is ONNX Runtime reached through sherpa-onnx.
	RuntimeSherpa Runtime = "sherpa-onnx"
)

// Placement is where the runtime executes relative to the daemon process. It
// is an independent question from the runtime, with a different default
// answer for each one. See docs/design/configuration-surface.md §3.1.
type Placement string

const (
	// PlacementInProcess links the runtime into the daemon; the model is
	// resident for the life of the daemon.
	PlacementInProcess Placement = "in-process"
	// PlacementLocalServer runs a supervised child process holding the model,
	// reached over loopback HTTP. The model stays warm for the child's life.
	PlacementLocalServer Placement = "local-server"
	// PlacementSubprocess spawns a fresh process per utterance, loading and
	// freeing the model each time.
	PlacementSubprocess Placement = "subprocess"
	// PlacementRemote posts audio to an HTTP server someone else runs.
	PlacementRemote Placement = "remote"
)

// RuntimeFor reports which runtime executes the named model.
//
// A name the catalog does not carry is a model the user installed by hand,
// which mavor looks for as a directory under the sherpa model directory — so
// it runs on sherpa-onnx. Whether that directory exists is a separate
// question, answered where the model is actually loaded.
func RuntimeFor(name string) Runtime {
	if m, ok := Lookup(name); ok && m.Engine != "sherpa" {
		return RuntimeWhisper
	}
	return RuntimeSherpa
}

// Selection is what the daemon will actually run: the model, the runtime that
// owns it, and where that runtime executes.
type Selection struct {
	Model     string
	Runtime   Runtime
	Placement Placement

	// Server is the URL of a transcription server someone else runs, set
	// only when Placement is PlacementRemote.
	Server string

	// Reason states in one clause why this placement was chosen, for
	// `mavor doctor` and the daemon's startup log.
	Reason string

	// Warnings are settings that are not errors but do not do what they
	// appear to say — a placement named alongside a server URL, say.
	Warnings []string
}

// Select derives the runtime and placement for a model from the model name and
// the two `[advanced]` keys that can influence them.
//
// placement accepts "auto" (or empty) and "subprocess" only; the other two
// placements are derived and are not things a user can ask for. server is a
// URL, and setting it implies a remote placement. Naming a placement the
// model's runtime cannot provide is an error rather than something quietly
// ignored — see docs/design/configuration-surface.md §3.2 for the table of
// combinations that exist.
func Select(model, placement, server string) (Selection, error) {
	rt := RuntimeFor(model)
	sel := Selection{Model: model, Runtime: rt}

	switch placement {
	case "", "auto", "subprocess":
	default:
		return Selection{}, fmt.Errorf(
			"models: advanced.placement = %q is not a placement you can ask for (use \"auto\", or \"subprocess\" for a whisper model)",
			placement)
	}

	if server != "" {
		if rt != RuntimeWhisper {
			return Selection{}, fmt.Errorf(
				"models: advanced.server names an HTTP transcription server, which model %q cannot use — it runs on %s, and mavor has no remote placement for it",
				model, rt)
		}
		sel.Placement = PlacementRemote
		sel.Server = server
		sel.Reason = "advanced.server names a server to post audio to"
		if placement == "subprocess" {
			sel.Warnings = append(sel.Warnings,
				"advanced.placement = \"subprocess\" is ignored because advanced.server is set; audio goes to the server")
		}
		return sel, nil
	}

	if placement == "subprocess" {
		if rt != RuntimeWhisper {
			return Selection{}, fmt.Errorf(
				"models: advanced.placement = \"subprocess\" is not available for model %q — it runs %s in the daemon's own process, and there is no per-utterance command to spawn",
				model, rt)
		}
		sel.Placement = PlacementSubprocess
		sel.Reason = "advanced.placement asked for a fresh whisper-cli per utterance"
		return sel, nil
	}

	switch rt {
	case RuntimeWhisper:
		sel.Placement = PlacementLocalServer
		sel.Reason = "whisper models default to a supervised warm whisper-server"
	default:
		sel.Placement = PlacementInProcess
		sel.Reason = "sherpa models are linked into the daemon and stay resident"
	}
	return sel, nil
}
