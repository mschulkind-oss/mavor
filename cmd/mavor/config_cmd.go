package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mschulkind-oss/mavor/internal/config"
	toml "github.com/pelletier/go-toml/v2"
)

// defaultConfigTemplate renders the file `mavor config init` scaffolds.
//
// It is generated from config.Default() rather than written out a second
// time. The two used to be separate literals and drifted — a user who ran
// `config init` got a different mode, a different ducking setting and a
// different top margin from a user who did not — which is the bug this
// generator exists to make impossible. A test parses the output and asserts
// it is exactly config.Default().
//
// Every value it prints therefore comes from d. The commented-out lines are
// documentation, not settings: deleting one changes nothing, because the
// value beside it is already the default.
func defaultConfigTemplate() string {
	d := config.Default()
	return fmt.Sprintf(`# ~/.config/mavor/config.toml
#
# Every value has a working default; delete a line to get it back.
# Run `+"`mavor doctor`"+` after editing — it checks each setting against this
# machine and reports what the daemon will actually do with it.

# The model that produces your text. `+"`mavor models list`"+` shows every choice
# with its size, speed and accuracy, and marks what is installed.
model = %q

[preview]
# Text in the overlay while you speak. The final text always comes from
# `+"`model`"+`, typed once when you release the key. The preview types nothing.
enabled = %t

# Where the preview text comes from.
#   "auto"       — read `+"`model`"+` directly if it decodes as you speak;
#                  otherwise run a small streaming model alongside it;
#                  otherwise fall back to "phrases"
#   "phrases"    — no second model: re-transcribe with `+"`model`"+` at each
#                  pause. Cheaper, slower, and prone to filling silence
#                  with words you did not say
#   <model name> — run that model alongside as the preview source
source = %q

# "phrases" only: how long a pause ends a phrase, and how much speech a
# phrase needs before a pause can end it.
# pause_ms = %d
# min_phrase_ms = %d

[ducking]
# Lower other audio while recording.
enabled = %t
volume = %-25q# "0%%" mutes; "25%%" merely lowers
# apps = ["spotify", "firefox"]   # only these; the default is every stream
# sink = ""                       # a specific output, not the default one

[vocabulary]
# Words the model gets wrong: names, jargon, commands. whisper models take
# these as a prompt; transducer models (parakeet, zipformer) boost them
# while decoding. Other models cannot use them and `+"`mavor doctor`"+` says so.
# words = ["mavor", "wlroots", "Schulkind"]
# file  = "~/.config/mavor/vocabulary.txt"   # one phrase per line
# boost = %s   # transducers only. 1.5 to 3.0 is the useful range; higher
#               # makes these words appear where they were not said.

[logging]
# Debug-level detail: overlay surface sizes and repaint timings, preview
# chunk cadence and text growth, and how long each stage of a dictation
# took. Noisy — the preview alone logs on a 30 ms tick — so it is off
# until something is wrong. `+"`mavor daemon -v`"+` does the same for one run.
# Output goes wherever paths.log points.
verbose = %t

[output]
# Your transcript is always typed into the focused window. This also copies
# it to the clipboard, replacing whatever was there. Off by default: it
# makes a keystroke that landed in the wrong window recoverable, but it
# costs you the clipboard on every utterance. `+"`mavor history --copy`"+` recovers
# a transcript on demand without it.
clipboard = %t

[overlay]
top_margin = %d   # px below the top of the usable area, under your bar

# Chosen for you. Override only if `+"`mavor doctor`"+` gives you a reason to.
[advanced]
# placement = %q     # "auto", or "subprocess" for whisper models
# server = "http://…"    # send audio to a whisper server you run instead
# threads = %-12d # default: this machine's physical core count
# gpu = %q           # "auto" or "off". whisper only — sherpa models
#                        # run on the CPU whatever this says.

[paths]
# models = %q
# log    = %q
# socket = %q
`,
		d.Model,
		d.Preview.Enabled,
		d.Preview.Source,
		d.Preview.PauseMS,
		d.Preview.MinPhraseMS,
		d.Ducking.Enabled,
		d.Ducking.Volume,
		strconv.FormatFloat(float64(d.Vocabulary.Boost), 'f', -1, 32),
		d.Logging.Verbose,
		d.Output.Clipboard,
		d.Overlay.TopMargin,
		d.Advanced.Placement,
		d.Advanced.Threads,
		d.Advanced.GPU,
		d.Paths.Models,
		d.Paths.Log,
		d.Paths.Socket,
	)
}

func runConfig(args []string) error {
	if len(args) == 0 {
		return runConfigShow()
	}
	switch args[0] {
	case "init":
		force := false
		for _, a := range args[1:] {
			if a == "--force" || a == "-f" {
				force = true
			}
		}
		return runConfigInit(force)
	case "path":
		fmt.Println(config.Path())
		return nil
	case "show":
		return runConfigShow()
	case "help", "-h", "--help":
		fmt.Println(`usage: mavor config <command>

commands:
  init [--force]   create default configuration file (~/.config/mavor/config.toml)
  show             print the current resolved configuration
  path             print the path to the configuration file`)
		return nil
	default:
		return fmt.Errorf("unknown config command: %s (try 'mavor config help')", args[0])
	}
}

func runConfigInit(force bool) error {
	p := config.Path()
	if _, err := os.Stat(p); err == nil && !force {
		return fmt.Errorf("configuration file already exists at %s (use --force to overwrite)", p)
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory %s: %w", dir, err)
	}
	if err := os.WriteFile(p, []byte(defaultConfigTemplate()), 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", p, err)
	}
	fmt.Printf("✅ Initialized configuration at %s\n", p)
	return nil
}

func runConfigShow() error {
	p := config.Path()
	cfg, err := config.Load(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read config %s: %w", p, err)
	}
	b, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	fmt.Printf("# Config file: %s\n", p)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		fmt.Printf("# (file does not exist on disk — using built-in defaults)\n\n")
	} else {
		fmt.Printf("\n")
	}
	fmt.Print(string(b))
	return nil
}
