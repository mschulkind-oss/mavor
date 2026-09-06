package models

import (
	"strings"
	"testing"
)

// The runtime is a property of the model, read off the catalog. There is no
// key that sets it, so this is the only thing that decides which inference
// library loads a model.
func TestRuntimeIsReadOffTheCatalog(t *testing.T) {
	for name, want := range map[string]Runtime{
		"whisper-tiny.en":         RuntimeWhisper,
		"whisper-base.en":         RuntimeWhisper,
		"whisper-large-v3-turbo":  RuntimeWhisper,
		"whisper-distil-large-v3": RuntimeWhisper,
		"parakeet-tdt-0.6b":       RuntimeSherpa,
		"moonshine-tiny":          RuntimeSherpa,
		"zipformer-streaming":     RuntimeSherpa,
		"sensevoice-small":        RuntimeSherpa,
		"canary-1b":               RuntimeSherpa,
	} {
		if got := RuntimeFor(name); got != want {
			t.Errorf("RuntimeFor(%q) = %q, want %q", name, got, want)
		}
	}
}

// Every catalog entry has to resolve to one of the two runtimes; a third
// value would mean a model nothing can load.
func TestEveryCatalogModelHasARuntime(t *testing.T) {
	for _, m := range Catalog {
		switch got := RuntimeFor(m.Name); got {
		case RuntimeWhisper, RuntimeSherpa:
		default:
			t.Errorf("RuntimeFor(%q) = %q, which is neither runtime", m.Name, got)
		}
	}
}

// A name the catalog does not carry is a model the user installed by hand,
// which mavor looks for as a directory under the sherpa model dir.
func TestModelOutsideTheCatalogRunsOnSherpa(t *testing.T) {
	for _, name := range []string{"my-own-model", "/home/me/models/custom", "moonshin-tiny"} {
		if got := RuntimeFor(name); got != RuntimeSherpa {
			t.Errorf("RuntimeFor(%q) = %q, want %q", name, got, RuntimeSherpa)
		}
	}
}

func TestDefaultPlacementsAreThePerRuntimeOnes(t *testing.T) {
	whisper, err := Select("whisper-base.en", "auto", "")
	if err != nil {
		t.Fatal(err)
	}
	if whisper.Placement != PlacementLocalServer {
		t.Errorf("whisper default placement = %q, want %q", whisper.Placement, PlacementLocalServer)
	}

	sherpa, err := Select("parakeet-tdt-0.6b", "auto", "")
	if err != nil {
		t.Fatal(err)
	}
	if sherpa.Placement != PlacementInProcess {
		t.Errorf("sherpa default placement = %q, want %q", sherpa.Placement, PlacementInProcess)
	}
}

// An empty placement is the same as "auto": a config that never mentioned the
// key must behave like one that set it to the default.
func TestEmptyPlacementIsAuto(t *testing.T) {
	for _, model := range []string{"whisper-base.en", "parakeet-tdt-0.6b"} {
		empty, err := Select(model, "", "")
		if err != nil {
			t.Fatal(err)
		}
		auto, err := Select(model, "auto", "")
		if err != nil {
			t.Fatal(err)
		}
		if empty.Placement != auto.Placement {
			t.Errorf("%s: placement %q for the empty value, %q for \"auto\"", model, empty.Placement, auto.Placement)
		}
	}
}

func TestSubprocessIsAvailableOnlyForWhisper(t *testing.T) {
	sel, err := Select("whisper-base.en", "subprocess", "")
	if err != nil {
		t.Fatalf("subprocess on a whisper model: %v, want it accepted", err)
	}
	if sel.Placement != PlacementSubprocess {
		t.Errorf("Placement = %q, want %q", sel.Placement, PlacementSubprocess)
	}

	if _, err := Select("parakeet-tdt-0.6b", "subprocess", ""); err == nil {
		t.Error("subprocess on a sherpa model was accepted; there is no per-utterance command to spawn")
	} else if !strings.Contains(err.Error(), "parakeet-tdt-0.6b") {
		t.Errorf("error %q does not name the model it refused", err)
	}
}

// The derived placements are not values a user can ask for. Naming one is a
// config error, not a request that happens to agree with the default.
func TestDerivedPlacementsCannotBeNamed(t *testing.T) {
	for _, placement := range []string{"local-server", "in-process", "remote"} {
		if _, err := Select("whisper-base.en", placement, ""); err == nil {
			t.Errorf("placement = %q was accepted, want a refusal", placement)
		}
	}
}

func TestUnknownPlacementIsRefused(t *testing.T) {
	_, err := Select("whisper-base.en", "cli", "")
	if err == nil {
		t.Fatal("placement = \"cli\" was accepted; it is a value from the deleted engine key")
	}
	if !strings.Contains(err.Error(), "auto") || !strings.Contains(err.Error(), "subprocess") {
		t.Errorf("error %q does not name the values that are accepted", err)
	}
}

func TestServerURLImpliesRemote(t *testing.T) {
	sel, err := Select("whisper-base.en", "auto", "http://example.invalid:8080")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Placement != PlacementRemote {
		t.Errorf("Placement = %q, want %q", sel.Placement, PlacementRemote)
	}
	if sel.Server != "http://example.invalid:8080" {
		t.Errorf("Server = %q, want the configured URL", sel.Server)
	}
}

// A placement named alongside a server URL is not an error — the URL simply
// wins — but it is not doing what it looks like it is doing, so it is
// reported.
func TestPlacementBesideAServerURLIsWarnedAbout(t *testing.T) {
	sel, err := Select("whisper-base.en", "subprocess", "http://example.invalid:8080")
	if err != nil {
		t.Fatal(err)
	}
	if sel.Placement != PlacementRemote {
		t.Errorf("Placement = %q, want the server URL to win", sel.Placement)
	}
	if len(sel.Warnings) == 0 {
		t.Error("no warning for a placement that the server URL made irrelevant")
	}
}

// There is no remote placement for sherpa: upstream ships websocket servers
// mavor does not speak, so pointing a sherpa model at a URL is a refusal
// rather than a request that quietly does nothing.
func TestServerURLIsRefusedForASherpaModel(t *testing.T) {
	if _, err := Select("parakeet-tdt-0.6b", "auto", "http://example.invalid:8080"); err == nil {
		t.Error("advanced.server was accepted for a sherpa model")
	}
}

// Every selection carries a reason, because `mavor doctor` prints it: the
// placement is derived, so a user has no other way to see why they got it.
func TestEverySelectionExplainsItself(t *testing.T) {
	cases := []struct{ model, placement, server string }{
		{"whisper-base.en", "auto", ""},
		{"whisper-base.en", "subprocess", ""},
		{"whisper-base.en", "auto", "http://example.invalid"},
		{"parakeet-tdt-0.6b", "auto", ""},
	}
	for _, c := range cases {
		sel, err := Select(c.model, c.placement, c.server)
		if err != nil {
			t.Fatalf("Select(%q, %q, %q): %v", c.model, c.placement, c.server, err)
		}
		if sel.Reason == "" {
			t.Errorf("Select(%q, %q, %q) gave placement %q with no reason", c.model, c.placement, c.server, sel.Placement)
		}
	}
}
