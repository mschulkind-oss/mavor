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

For high-level architecture see [`how-mavor-works.md`](./reference/how-mavor-works.md).

> [!TIP]
> **Which model should you use?** See [`choosing-a-model.md`](./choosing-a-model.md)
> — the short answer is `base.en`, and the reasons are measured. The raw
> numbers behind it are in [`model-benchmarks.md`](./reports/model-benchmarks.md),
> regenerable on your own hardware with `just bench`.

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
| **Time to transcribe 20 s** | 1.0 s (`tiny.en`) – 1.6 s (`base.en`); 34 s for `large-v3` | 1.6 s (`zipformer-ctc`) – 4.4 s (`canary-180m`) | Model dependent, minus the per-call model load |
| **Real-Time Streaming** | No — text arrives after key release | **Yes**, with a streaming model: first text 114 ms in | No / optional chunking |
| **Resident Memory** | **~0 MB** — freed after each dictation | 150 MB – 2.3 GB, held while the daemon runs | ~0 MB locally; the server holds it |
| **Supported Models** | 11 Whisper GGML models | 13 sherpa models — NeMo, Zipformer, Moonshine, SenseVoice, Canary | Any Whisper / OpenAI-compatible model |
| **Hotword Boosting** | No | Transducer models only | Engine dependent |
| **Needs cgo** | No | **Yes** (`just build-sherpa`) | No |
| **Best For** | Almost everyone — it is the default for good reason | Non-English, or watching words appear as you speak | Offloading inference to a LAN or GPU box |

> [!NOTE]
> The `sherpa` engine's advantage is not raw speed: `base.en` on the `cli`
> engine transcribes faster than every sherpa model measured. What it buys is
> streaming, language coverage, and no per-dictation model load. Numbers in
> [`choosing-a-model.md`](./choosing-a-model.md).

---

### Detailed Engine Breakdown (User POV)

#### 1. `engine = "cli"` — Zero-RAM Whisper Execution (Default)

- **How it works:** When you finish speaking, the daemon invokes `whisper-cli` over the captured WAV file, reads the transcript from stdout, and terminates the process.
- **Why users choose it:**
  - **Zero Idle Memory:** When you are not actively dictating, the daemon consumes virtually 0 MB of system RAM. Whisper is only loaded into memory during transcription.
  - **Zero Extra Dependencies:** Uses the standard `whisper-cli` package distributed by all major Linux package managers (`whisper-cpp`).
  - **Standard Whisper Formatting:** Renowned for high-quality punctuation, sentence casing, numbers, and grammar formatting.
- **Trade-off:** You must wait ~1.5 to 3 seconds after releasing the key for the subprocess to initialize, transcribe, and emit text.

#### 2. `engine = "sherpa"` — In-Process Streaming and Wider Language Coverage

- **How it works:** The neural network weights remain permanently resident in memory within the daemon via in-process CGO ONNX Runtime bindings.
- **Why users choose it:**
  - **No process launch per transcription:** weights stay resident, so there is no model load between dictations the way the `cli` engine pays one.
  - **Live token streaming:** `zipformer-streaming` returns its first text **114 ms** after audio starts flowing, so words can appear while you are still speaking. Measured, from a warm model.
  - **Languages Whisper handles less well:** `sensevoice-small` covers Chinese, Japanese, Korean and Cantonese; `canary-180m` covers English, Spanish, German and French in 457 MB.
  - **Custom hotwords (`sherpa_hotwords_file`):** boost technical terms, code symbols, and personal names. Transducer models only — sherpa-onnx implements biasing during beam search, so the CTC and encoder-decoder models cannot use one.
- **Trade-off:** holds weights in RAM — 457 MB for `canary-180m`, 1.56 GB for `parakeet-tdt-0.6b`, 150 MB for `zipformer-streaming`. And the streaming models are markedly less accurate than the batch ones: 9.1% word error rate against 1.8%.

> [!NOTE]
> The `sherpa` engine needs a binary built with `just build-sherpa`. It is the
> one variant requiring cgo, so it cannot be cross-compiled. See
> [`choosing-a-model.md`](./choosing-a-model.md) for which sherpa model to pick.

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
sherpa_model = "canary-180m"  # any catalogued sherpa model, or a custom dir name
                              # `mavor models list` prints them all
sherpa_model_type = "auto"    # "auto" reads the file layout and is almost always
                              # right. Override with "transducer", "canary",
                              # "moonshine", "sensevoice", "paraformer",
                              # "nemo_ctc", "zipformer_ctc" or "whisper".
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

`mavor` supports batch **Whisper GGML** models via `whisper-cli` and
in-process **sherpa-onnx** models via native CGO bindings. The full catalog is
24 models; `mavor models list` prints it with sizes, languages and what is
already downloaded.

> [!TIP]
> **[`choosing-a-model.md`](./choosing-a-model.md) is the page that answers
> "which one?"** The table below is a summary of it. Both come from
> [`model-benchmarks.md`](./reports/model-benchmarks.md), which is generated by
> `just bench` — every figure was measured, none is a manufacturer claim.

### Supported Model Matrix

Times are for 20 seconds of speech on CPU; **Format** is whether the model
returns punctuated, capitalised text or a bare lowercase word stream.

| Model | Engine | Architecture | Time | RAM | Format | Use it for |
|---|---|---|---:|---:|---|---|
| `base.en` | `cli` | Whisper GGML | 1.63 s | 302 MB | Full | **The default.** Best accuracy measured. |
| `tiny.en` | `cli` | Whisper GGML | 1.05 s | 196 MB | Full | The lightest option that still formats. |
| `small.en` | `cli` | Whisper GGML | 5.10 s | 768 MB | Full | Little gain over `base.en` here. |
| `large-v3-turbo` | `cli` | Whisper GGML | 21.01 s | 1.81 GB | **None** | Not recommended — see the warning below. |
| `canary-180m` | `sherpa` | NeMo Canary | 4.40 s | 457 MB | Full | Best sherpa model; en/es/de/fr. |
| `parakeet-tdt-0.6b` | `sherpa` | NeMo transducer | 5.82 s | 1.56 GB | Full | 25 languages. |
| `sensevoice-small` | `sherpa` | SenseVoice | 3.88 s | 1.46 GB | Good | zh, en, ja, ko, yue. |
| `zipformer-streaming` | `sherpa` | Zipformer (online) | 4.65 s | 150 MB | Minimal | Streaming: first text in 114 ms. |

> [!WARNING]
> **The largest Whisper models return unpunctuated lowercase text.**
> `large-v3`, `large-v3-turbo`, `distil-large-v3` and `medium.en` all emit
> `lux is in the pit he cannot sit still` where `base.en` emits
> `Lux is in the pit. He cannot sit still.` Word error rate is the same; the
> output is not. `large-v3` is also 20x slower than `base.en` on CPU and wants
> 3.9 GB of RAM. [Details](./choosing-a-model.md#do-not-reach-for-the-biggest-model).

GPU changes the calculation for the larger models but not their formatting: a
Vulkan build (`just bench-gpu-build`) runs `medium.en` **12.8x** faster and
drops host memory from 2.07 GB to 174 MB, because the weights move to the
card. Sherpa models have no GPU path — the vendored ONNX Runtime ships no
execution providers.

### Automatic Downloads (`mavor models pull`)

`mavor models pull` automatically downloads, verifies, and extracts model archives into your cache directory:

```console
# The default, and the best measured model
$ mavor models pull base.en

# Best sherpa model: en/es/de/fr, formats well, 457 MB
$ mavor models pull canary-180m

# Streaming — text while you speak
$ mavor models pull zipformer-streaming
```

See [`choosing-a-model.md`](./choosing-a-model.md) before pulling one of the
large Whisper models; they are slower and format worse than `base.en`.

List all downloaded models in your local cache:

```console
$ mavor models list --installed
Model cache: /home/you/.cache/mavor/models

NAME      ENGINE       SIZE  LANGUAGES  STREAM  STATUS         ALIASES
base.en   whisper  141.1 MB  en         no      ✓ 141.1 MB  ★  whisper-base.en
parakeet  sherpa   429.4 MB  en         yes     ✓ 429.4 MB     parakeet-tdt

★ active   ✓ downloaded   – not downloaded
```

### Switching to the sherpa Engine

`canary-180m` is the sherpa model to start with: it is the only one that
punctuates and capitalises as well as `base.en`, in 457 MB, across English,
Spanish, German and French.

1. Build with cgo, which the sherpa engine requires:
   ```console
   $ just build-sherpa
   ```

2. Download the model:
   ```console
   $ mavor models pull canary-180m
   ```

3. Update `~/.config/mavor/config.toml`:
   ```toml
   engine = "sherpa"
   sherpa_model = "canary-180m"
   ```

   For streaming instead — words appearing as you speak, at a real cost in
   accuracy — use `sherpa_model = "zipformer-streaming"`.

4. Restart the daemon:
   ```console
   $ mavor service restart    # or: pkill -f 'mavor daemon' && mavor daemon
   ```

### Manual Model Installation (Custom ONNX Weights)

If you have trained or fine-tuned custom ONNX models from Hugging Face or k2-fsa, place the model assets into `~/.cache/mavor/models/sherpa/<model-name>/`:

```console
$ mkdir -p ~/.cache/mavor/models/sherpa/my-custom-model
$ cp tokens.txt encoder.onnx decoder.onnx joiner.onnx ~/.cache/mavor/models/sherpa/my-custom-model/
```

Then specify `sherpa_model = "my-custom-model"` in `config.toml`.

`mavor` identifies the architecture from the files present, not from the
directory name: an encoder, decoder and joiner is a transducer; an encoder and
decoder without a joiner is Canary or Whisper; a lone `model.onnx` is one of
the CTC or paraformer variants. Filenames carrying a training run — the
`encoder-epoch-99-avg-1.onnx` form sherpa-onnx publishes — are matched too.

If the layout is genuinely ambiguous (a bare `model.onnx` could be SenseVoice
or NeMo CTC), set `sherpa_model_type` explicitly. `mavor` reports what it
could not identify rather than guessing.

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
| `sherpa model "<name>" not found` | Model archive not downloaded or extracted | Run `mavor models pull <name>`; `mavor models list --installed` shows what is present |
| `toggle: connect: no such file or directory` | Daemon is not running or socket mismatch | Run `mavor daemon -v` or `mavor doctor` to inspect status |
| Overlay does not appear | Compositor does not implement `wlr-layer-shell` | Ensure a wlroots session (sway, hyprland, river) is active; `mavor daemon -v` logs the reason it fell back to a silent overlay |
| Audio volume does not duck | `duck_audio = true` not set in `config.toml` | Set `duck_audio = true` and check `duck_streams` in config |
| Ghost words typed during silence | Silence hallucination without VAD | Ensure Silero VAD is active or use in-process `sherpa` engine |
| Text typed in wrong window | Focus shifted during transcription | Keep window focused until overlay closes |
| Systemd service fails to start | Audio socket or Wayland display not ready | Ensure `PartOf=graphical-session.target` and PipeWire is running |
