package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mschulkind-oss/mavor/internal/config"
	toml "github.com/pelletier/go-toml/v2"
)

const defaultConfigTemplate = `# ~/.config/mavor/config.toml
# mavor voice-to-text dictation daemon configuration
# ==============================================================================
# Simple Configuration (Smart Defaults)
# ==============================================================================

# Live preview while you speak. The text is typed once either way — when
# transcription finishes — because partial results are provisional.
# - "batch": no preview; the overlay shows only that it is recording (default).
# - "streaming": show partial text in the overlay as the words are recognized.
mode = "batch"

# Quality & Speed Preset:
# - "balanced": Whisper Base / Parakeet-TDT (100% accuracy, ~1.2s or 80ms live) — default.
# - "accurate": Whisper Large-v3 Turbo (maximum vocabulary nuance).
# - "fast":     Whisper Tiny / Moonshine (ultra-light, sub-0.7s).
preset = "balanced"

# Silence background music / media playback while recording. Set duck_volume to
# lower it instead of muting, e.g. duck_volume = "25%".
duck_audio = true
# duck_volume = "0%"

# ==============================================================================
# Advanced Overrides (Optional — smart defaults are resolved automatically above)
# ==============================================================================
# top_margin = 32             # Gap below your bar (Waybar, etc.) in pixels
# engine = "server"           # "server", "cli", or "sherpa"
# model = "base.en"           # Whisper GGML model name
# threads = 4                 # CPU inference threads
# gpu_layers = 0              # Set >0 to offload layers to Vulkan/ROCm (-ngl)
# streaming_strategy = "auto" # "auto", "vad_batch" (Whisper pauses), or "transducer" (Parakeet)
# silence_threshold_ms = 450  # Pause duration to trigger VAD batch slice
# min_phrase_ms = 600         # Minimum speech duration before slicing
# socket = "$XDG_RUNTIME_DIR/mavor.sock"
# Where engine = "server" sends audio. A filesystem path means "run a local
# whisper-server for me" — the daemon picks a loopback port and supervises the
# child, so nothing is created at the path itself. Use an http:// URL to reach
# a server you run.
# server_socket = "$XDG_RUNTIME_DIR/mavor-server.sock"
`

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
	if err := os.WriteFile(p, []byte(defaultConfigTemplate), 0o644); err != nil {
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
