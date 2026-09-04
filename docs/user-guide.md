---
title: "mavor — User Guide"
author: "Matthew Schulkind"
date: 2026-08-16
status: accepted
tags: [user-guide, manual, wayland, sway, configuration, streaming, cgo, systemd, parakeet, engines]
summary: "Task-oriented manual for installing, running, configuring, and debugging the mavor voice dictation daemon on Sway and Wayland."
---

# mavor — User Guide

A task-oriented manual for installing, running, configuring, and debugging `mavor`, the low-latency voice dictation daemon for Sway and Wayland compositors.

For high-level architecture see [`how-mavor-works.md`](./design/how-mavor-works.md). For benchmark comparisons across local runtimes see [`local-engine-benchmarks-and-architecture.md`](./design/local-engine-benchmarks-and-architecture.md).

---

## 1. Architecture & Core Workflow

`mavor` operates as a lightweight daemon managing a thread-safe finite state machine (`idle` ⇄ `recording` ⇄ `transcribing`). A global Sway keybind triggers dictation via push-to-talk or toggle actions:

```
                  ┌─────────────────────────────────────────┐
                  │          User Keybind ($mod+`)          │
                  └────────────────────┬────────────────────┘
                                       │
                         mavor start / stop / toggle (CLI)
                                       │
                                       ▼ (Unix Socket: $XDG_RUNTIME_DIR/mavor.sock)
                  ┌─────────────────────────────────────────┐
                  │               mavor daemon                │
                  │   ┌─────────────────────────────────┐   │
                  │   │   state.Machine (FSM Engine)    │   │
                  │   └──────┬──────────────┬───────────┘   │
                  │          │              │               │
                  │   ┌──────▼──────┐┌──────▼───────────┐   │
                  │   │ audio.VAD & ││   Pluggable      │   │
                  │   │   Ducking   ││   Transcriber    │   │
                  │   │ (PipeWire)  ││ (Sherpa / Whisper│   │
                  │   └──────┬──────┘└──────┬───────────┘   │
                  │          │              │               │
                  │   ┌──────▼──────┐┌──────▼───────────┐   │
                  │   │ layer HUD   ││  output.Emitter  │   │
                  │   │ Overlay     ││ (wtype + wl-copy)│   │
                  │   └─────────────┘└──────────────────┘   │
                  └─────────────────────────────────────────┘
```

### Key Workflow Steps

1. **Activation:** Keypress signals `mavor start` (push-to-talk) or `mavor toggle`. Daemon enters `recording`.
2. **Audio Capture & Ducking:** Audio capture initializes via PipeWire (`parec`). If configured, background media streams (Spotify, Firefox) are automatically ducked.
3. **Live HUD Waveform:** The layer-shell HUD overlay appears 8px below Waybar, rendering a live volume waveform meter across 6 discrete energy levels (0% to 100%).
4. **VAD Gating & Transcription:** On release (`mavor stop` / second `toggle`), Silero VAD evaluates speech frames. If speech is detected, the selected speech-to-text (STT) engine (in-process `sherpa-onnx` CGO, `whisper-cli`, or warm HTTP server) executes transcription.
5. **Output Emission:** `wtype` types text directly into the focused window while `wl-copy` updates the Wayland clipboard. Temporary recording files in `/tmp/mavor-recordings/` are immediately purged.

---

## 2. Choosing an Engine: `cli` vs `sherpa` vs `server`

The `engine` setting in `config.toml` dictates how speech is converted to text. Understanding the differences helps you tailor `mavor` to your hardware and typing workflow:

```toml
# ~/.config/mavor/config.toml
engine = "cli"     # Options: "cli" (default), "sherpa", or "server"
```

### Engine Comparison at a Glance

| Feature | `engine = "cli"` (Whisper CLI) | `engine = "sherpa"` (In-Process CGO) | `engine = "server"` (Warm HTTP/Socket) |
|---|---|---|---|
| **Underlying Runtime** | `whisper-cli` subprocess (`whisper.cpp`) | In-process ONNX Runtime (CGO) | Background HTTP / Unix socket daemon |
| **Post-Speech Latency** | 1.5 s – 4.0 s (batch processing) | **< 100 ms** (instantaneous) | 200 ms – 500 ms |
| **Real-Time Streaming** | No (text arrives after key release) | **Yes** (80ms streaming chunks) | No / Optional chunking |
| **Idle Memory Footprint** | **~0 MB** (memory freed after dictation) | ~150 MB – 600 MB (resident in RAM) | ~0 MB in daemon (server holds RAM) |
| **Supported Models** | OpenAI Whisper GGML models | Parakeet-TDT, Zipformer, Moonshine, SenseVoice | Any Whisper / OpenAI-compatible model |
| **Hotword Boosting** | No | **Yes** (custom vocabulary list) | Engine dependent |
| **Best For** | Casual dictation, low RAM usage | Heavy daily dictation, sub-second typing | Offloading inference to LAN / GPU server |

---

### Detailed Engine Breakdown (User POV)

#### 1. `engine = "cli"` — Zero-RAM Whisper Execution (Default)

- **How it works:** When you finish speaking, the daemon invokes `whisper-cli` over the captured WAV file, reads the transcript from stdout, and terminates the process.
- **Why users choose it:**
  - **Zero Idle Memory:** When you are not actively dictating, the daemon consumes virtually 0 MB of system RAM. Whisper is only loaded into memory during transcription.
  - **Zero Extra Dependencies:** Uses the standard `whisper-cli` package distributed by all major Linux package managers (`whisper-cpp`).
  - **Standard Whisper Formatting:** Renowned for high-quality punctuation, sentence casing, numbers, and grammar formatting.
- **Trade-off:** You must wait ~1.5 to 3 seconds after releasing the key for the subprocess to initialize, transcribe, and emit text.

#### 2. `engine = "sherpa"` — Sub-100ms In-Process Streaming (Power User Favorite)

- **How it works:** The neural network weights remain permanently resident in memory within the daemon via in-process CGO ONNX Runtime bindings.
- **Why users choose it:**
  - **Sub-100ms Latency:** The transcription is processed in real-time as you speak. The instant you release the hotkey, the text is already typed.
  - **Live Token Streaming:** Words appear on screen or in the HUD subtitle incrementally in 80ms chunks while you speak.
  - **SOTA Acoustic Models (Parakeet-TDT):** Access to NVIDIA FastConformer / Parakeet-TDT, which matches or exceeds Whisper accuracy on English with dramatically lower compute requirements.
  - **Custom Hotwords (`sherpa_hotwords_file`):** Boost technical terms, code symbols, variable names, and personal names so the engine never misspells them.
- **Trade-off:** Holds model weights in RAM (approx. 200 MB for Parakeet-TDT, 60 MB for quantized Zipformer).

#### 3. `engine = "server"` — Offloaded or Shared Inference

- **How it works:** The daemon sends audio data over a local Unix socket (`$XDG_RUNTIME_DIR/mavor-server.sock`) or remote HTTP endpoint.
- **Why users choose it:**
  - Allows running speech inference on a dedicated GPU machine or server on your local network while dictating from a lightweight laptop.

---

## 3. Requirements & Dependencies

### Runtime Dependencies

| Package / Tool | Component | Purpose |
|---|---|---|
| `sway` / wlroots | Compositor | Layer-shell floating HUD overlay & window management |
| `pipewire`, `pulseaudio-utils` | Audio Stack | Audio recording (`parec`) and volume ducking (`pactl`) |
| `wtype` | Virtual Keyboard | Synthetic keystroke injection into focused window |
| `wl-clipboard` | Clipboard | `wl-copy` synchronization for instant pasting |
| `whisper-cpp` | STT Engine | Default CLI engine (`whisper-cli`) |

### Build Prerequisites

- Go ≥ 1.26
- `just` task runner
- CGO compiler (`gcc` or `clang`)
- No development headers. The default build is pure Go (`CGO_ENABLED=0`); only the optional `sherpa` build tag needs a C toolchain.

---

## 4. Installation & Deployment

### Quick Install (`just install`)

Builds the binary and installs it to `~/.local/bin/mavor`:

```console
$ git clone https://github.com/mschulkind-oss/mavor && cd mavor
$ mise install     # ensures pinned Go version
$ just install     # builds binary and installs to ~/.local/bin/mavor
```

### Full Deployment (`just deploy`)

Installs the binary to `~/.local/bin/mavor` and sets up the systemd user service (`~/.config/systemd/user/mavor.service`):

```console
$ just deploy
```

### Cross-compiling

The default build is pure Go, so a binary for another architecture needs no
toolchain beyond Go itself:

```console
$ CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/mavor-arm64 ./cmd/mavor
```

The `sherpa` build tag is the exception: it links the in-process ONNX
recognizers through cgo and must be built on the target architecture.

---

## 5. Self-Documenting CLI & Environment Diagnostics

`mavor` provides built-in tools for environment inspection and configuration setup:

### Environment Diagnostics (`mavor doctor`)

Run `mavor doctor` to inspect the full audio, Wayland, and engine pipeline:

```console
$ mavor doctor
mavor doctor — system and environment verification
==================================================
✅ Wayland session:             WAYLAND_DISPLAY=wayland-1
✅ Audio capture (parec/Pulse): parec available, PipeWire/PulseAudio server connected
✅ Virtual typing (wtype):      wtype installed at /bin/wtype
✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
✅ Speech engine:               whisper-cli installed at /bin/whisper-cli
✅ GPU acceleration:            CPU only (whisper-cli loaded no GPU backend — the stock build ships CPU backends only)
✅ Configuration file:          valid config (mode=streaming, preset=balanced, model=base.en)
✅ Voice model availability:    whisper model found at ~/.cache/mavor/models/ggml-base.en.bin
✅ Daemon socket status:        daemon is active (state: idle)
✅ Systemd user service:        systemd unit installed and active (active)
==================================================
✅ All environment checks passed! mavor is ready.
```

### Configuration Management (`mavor config`)

```console
$ mavor config init          # create ~/.config/mavor/config.toml with commented defaults
$ mavor config init --force  # overwrite existing configuration with defaults
$ mavor config show          # print resolved active configuration
$ mavor config path          # print canonical path to config.toml
```

### Systemd Service Management (`mavor service`)

```console
$ mavor service install --start  # install, enable, and start mavor.service
$ mavor service status           # inspect background daemon status
$ mavor service restart          # restart background daemon
$ mavor service stop             # stop background daemon
$ mavor service uninstall        # disable and remove systemd unit
```

---

## 6. Sway Keybinding Setup

Edit `~/.config/sway/config` to launch the daemon and register hotkeys:

### Option A: Push-to-Talk Mode (Recommended: Hold to Speak)

```
# Launch daemon on login (if not using systemd service)
exec /home/you/.local/bin/mavor daemon

# Push-to-talk: Hold to record, release to transcribe
bindsym $mod+grave exec mavor start
bindsym --release $mod+grave exec mavor stop
```

### Option B: Toggle Mode (Hands-Free Dictation)

```
# Press once to start recording, press again to transcribe
bindsym $mod+grave exec mavor toggle
```

Reload Sway configuration:
```console
$ swaymsg reload
```

---

## 7. Configuration Reference

Configuration file location: `$XDG_CONFIG_HOME/mavor/config.toml` (defaults to `~/.config/mavor/config.toml`).

> **Path Expansion:** All paths in `config.toml` support `~` (user home directory) and `$ENVIRONMENT_VARIABLES`.

### Annotated `config.toml`

```toml
# ==========================================
# Overlay & UI Configuration
# ==========================================
# Gap in pixels between top screen edge and overlay (default: 8)
top_margin = 8

# ==========================================
# STT Inference Engines
# ==========================================
# Engine type: "cli" (whisper.cpp), "sherpa" (in-process CGO), or "server" (HTTP)
engine = "cli"

# Whisper CLI settings (when engine = "cli")
model = "base.en"
model_dir = "~/.cache/mavor/models"
threads = 4
gpu_layers = 0             # Set >0 to offload layers to Vulkan/ROCm (-ngl)
device = "auto"            # "auto", "vulkan", "rocm", "cpu"

# Sherpa ONNX settings (when engine = "sherpa")
sherpa_model = "parakeet"  # "parakeet", "zipformer", "moonshine", or custom dir name
sherpa_model_type = "auto" # "transducer" (Parakeet/Zipformer), "moonshine", "sensevoice"
sherpa_provider = "cpu"    # "cpu", "cuda", "vulkan"
sherpa_hotwords_file = "~/.config/mavor/hotwords.txt"
sherpa_hotwords_score = 1.5
sherpa_decoding_method = "greedy_search"

# ==========================================
# Audio Capture & PipeWire Ducking
# ==========================================
# Enable automatic volume ducking of background streams during recording
duck_audio = true
duck_volume = "0%"   # muted by default; e.g. "25%" to lower instead of silence

# Target specific media applications while preserving voice calls (Discord/Zoom)
duck_streams = ["spotify", "firefox", "vlc", "chromium"]

# Optional target sink name (empty = default audio sink)
duck_sink = ""

# ==========================================
# Daemon IPC Socket
# ==========================================
socket = "$XDG_RUNTIME_DIR/mavor.sock"
server_socket = "$XDG_RUNTIME_DIR/mavor-server.sock"
```

---

## 8. Model Management & Supported Architectures

`mavor` supports both batch **Whisper GGML** models via `whisper-cli` and in-process streaming **Sherpa-ONNX** models (including **Parakeet-TDT**, **Zipformer**, **Moonshine**, and **SenseVoice**) via native CGO bindings.

### Supported Model Matrix

| Model Name | Engine | Architecture | Streaming Chunk | Accuracy / Speed Profile | Automatic CLI Pull |
|---|---|---|---|---|---|
| `parakeet` / `parakeet-tdt` | `sherpa` (CGO) | FastConformer Transducer | **80 ms** | SOTA English accuracy, sub-100ms streaming latency | `mavor models pull parakeet` |
| `zipformer` | `sherpa` (CGO) | Zipformer Transducer | **160 ms** | Ultra-lightweight streaming, minimal CPU usage | `mavor models pull zipformer` |
| `moonshine` | `sherpa` (CGO) | Moonshine INT8 | Batch / Offline | Fast quantized encoder-decoder for short phrases | `mavor models pull moonshine` |
| `sensevoice` | `sherpa` (CGO) | SenseVoice | Batch / Offline | Multilingual (EN, ZH, JA, KO, Cantonese) + emotion tagging | `mavor models pull sensevoice` |
| `base.en` | `cli` (whisper) | Whisper GGML | Batch / Offline | Stock production Whisper default (141 MB) | `mavor models pull base.en` |
| `tiny.en` | `cli` (whisper) | Whisper GGML | Batch / Offline | Lightweight test model (74 MB) | `mavor models pull tiny.en` |
| `small.en` | `cli` (whisper) | Whisper GGML | Batch / Offline | High-accuracy technical vocabulary (465 MB) | `mavor models pull small.en` |
| `large-v3-turbo` | `cli` (whisper) | Whisper GGML | Batch / Offline | Highest accuracy Whisper model (1.5 GB) | `mavor models pull large-v3-turbo` |

### Automatic Downloads (`mavor models pull`)

`mavor models pull` automatically downloads, verifies, and extracts model archives into your cache directory:

```console
# Download NVIDIA Parakeet-TDT Streaming Transducer
$ mavor models pull parakeet

# Download Streaming Zipformer Transducer
$ mavor models pull zipformer

# Download Whisper GGML models
$ mavor models pull base.en
$ mavor models pull large-v3-turbo
```

List all downloaded models in your local cache:

```console
$ mavor models list --installed
Model cache: /home/you/.cache/mavor/models

NAME      ENGINE       SIZE  LANGUAGES  STREAM  STATUS         ALIASES
base.en   whisper  141.1 MB  en         no      ✓ 141.1 MB  ★  whisper-base.en
parakeet  sherpa   429.4 MB  en         yes     ✓ 429.4 MB     parakeet-tdt

★ active   ✓ downloaded   – not downloaded
```

### Switching to Parakeet-TDT Streaming Engine

To use NVIDIA Parakeet-TDT for real-time dictation:

1. Download the model:
   ```console
   $ mavor models pull parakeet
   ```

2. Update `~/.config/mavor/config.toml`:
   ```toml
   engine = "sherpa"
   sherpa_model = "parakeet"
   ```

3. Restart the daemon:
   ```console
   $ mavor service restart    # or: pkill -f 'mavor daemon' && mavor daemon
   ```

### Manual Model Installation (Custom ONNX Weights)

If you have trained or fine-tuned custom ONNX models from Hugging Face or k2-fsa, place the model assets into `~/.cache/mavor/models/sherpa/<model-name>/`:

```console
$ mkdir -p ~/.cache/mavor/models/sherpa/my-custom-model
$ cp tokens.txt encoder.onnx decoder.onnx joiner.onnx ~/.cache/mavor/models/sherpa/my-custom-model/
```

Then specify `sherpa_model = "my-custom-model"` in `config.toml`. `mavor` automatically discovers `encoder.onnx`, `decoder.onnx`, `joiner.onnx`, and `tokens.txt`.

---

## 9. Development & Quality Gate

All development commands are unified in the [`Justfile`](../Justfile):

```console
$ just check-ci   # Read-only quality gate (format check + vet + unit tests)
$ just check      # Local dev gate (auto-formats + lints + runs tests)
$ just format     # Formats all Go files in-place
$ just lint       # Runs go vet and static analysis
$ just test       # Runs unit tests with race detection
$ just test-int   # Runs headless Sway + PipeWire integration test harness
$ just storybook  # Generates HTML visual storybook report with real screenshots
$ just install    # Builds and installs binary to ~/.local/bin/mavor
$ just deploy     # Installs binary and sets up systemd user service
$ just doctor     # Runs environment health check (mavor doctor)
$ just dev        # Runs daemon in foreground with verbose debug logging
$ just done       # Pre-commit quality verification
```

---

## 10. Troubleshooting

| Symptom | Cause | Solution |
|---|---|---|
| `model "base.en" not found` | Model file has not been downloaded | Run `mavor models pull base.en` |
| `sherpa model "parakeet" not found` | Parakeet ONNX model archive not extracted | Run `mavor models pull parakeet` |
| `toggle: connect: no such file or directory` | Daemon is not running or socket mismatch | Run `mavor daemon -v` or `mavor doctor` to inspect status |
| Overlay does not appear | Compositor does not implement `wlr-layer-shell` | Ensure a wlroots session (sway, hyprland, river) is active; `mavor daemon -v` logs the reason it fell back to a silent overlay |
| Audio volume does not duck | `duck_audio = true` not set in `config.toml` | Set `duck_audio = true` and check `duck_streams` in config |
| Ghost words typed during silence | Silence hallucination without VAD | Ensure Silero VAD is active or use in-process `sherpa` engine |
| Text typed in wrong window | Focus shifted during transcription | Keep window focused until overlay closes |
| Systemd service fails to start | Audio socket or Wayland display not ready | Ensure `PartOf=graphical-session.target` and PipeWire is running |
