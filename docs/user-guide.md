---
title: "mavor — User Guide"
author: "Matthew Schulkind"
date: 2026-09-05
status: accepted
tags: [user-guide, manual, wayland, sway, configuration, preview, vocabulary, cgo, systemd, models]
summary: "Task-oriented manual for installing, running, configuring, and debugging the mavor voice dictation daemon on Sway and Wayland."
---

# mavor — User Guide

A task-oriented manual for installing, running, configuring, and debugging `mavor`, the low-latency voice dictation daemon for Sway and Wayland compositors.

For high-level architecture see [`how-mavor-works.md`](./reference/how-mavor-works.md).

> [!IMPORTANT]
> **The configuration schema was rewritten.** One top-level `model` key and six
> tables replaced twenty-nine flat keys, and there are no compatibility
> aliases: an old `config.toml` parses, contributes nothing, and every default
> applies. `mavor doctor` says so when it sees such a file. The fix is
> `mavor config init --force`, and [§7](#7-configuration-reference) is the new
> file annotated. The reasoning is in
> [`configuration-surface.md`](./design/configuration-surface.md).

> [!TIP]
> **Which model should you use?** See [`choosing-a-model.md`](./choosing-a-model.md)
> — the short answer is `whisper-base.en`, and the reasons are measured. The raw
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
                  │              mavor daemon               │
                  │   ┌─────────────────────────────────┐   │
                  │   │   state.Machine (FSM Engine)    │   │
                  │   └──────┬──────────────┬───────────┘   │
                  │          │              │               │
                  │   ┌──────▼──────┐┌──────▼───────────┐   │
                  │   │ audio.VAD & ││  Transcriber for │   │
                  │   │   Ducking   ││  the configured  │   │
                  │   │ (PipeWire)  ││      model       │   │
                  │   └──────┬──────┘└──────┬───────────┘   │
                  │          │              │               │
                  │   ┌──────▼──────┐┌──────▼───────────┐   │
                  │   │ layer HUD   ││  output.Emitter  │   │
                  │   │  + preview  ││ (wtype + wl-copy)│   │
                  │   └─────────────┘└──────────────────┘   │
                  └─────────────────────────────────────────┘
```

### Key Workflow Steps

1. **Activation:** Keypress signals `mavor start` (push-to-talk) or `mavor toggle`. Daemon enters `recording`.
2. **Audio Capture & Ducking:** Audio capture initializes via PipeWire (`parec`). If `[ducking]` is enabled, background media streams (Spotify, Firefox) are automatically ducked.
3. **Live HUD Waveform and preview:** The layer-shell HUD overlay appears 8px below Waybar, rendering a live volume waveform meter across 6 discrete energy levels (0% to 100%). If the preview is on, provisional text appears there too — see [§7.2](#72-preview--text-in-the-overlay-while-you-speak). **The preview is never typed.**
4. **VAD Gating & Transcription:** On release (`mavor stop` / second `toggle`), an energy-threshold voice-activity check scans the captured WAV: it needs at least 150 ms of frames above an RMS threshold before the audio is worth decoding. Below that the cycle ends silently rather than handing whisper a silent clip to hallucinate over. This is a plain RMS gate computed in Go — there is no neural VAD model in `mavor`. If speech is detected, the model named by `model` transcribes it, once.
5. **Output Emission:** `wtype` types text directly into the focused window while `wl-copy` updates the Wayland clipboard. Temporary recording files in `/tmp/mavor-recordings/` are immediately purged.

---

## 2. How a Model Runs: Runtime and Placement

There is no `engine` key any more. It welded together two questions that are not the same kind of thing, and both of them now have their own answer — the diagnosis is [§3 of the configuration design](./design/configuration-surface.md#3-two-axes-welded-into-one-enum), which coined the two terms this section uses:

- **Runtime** — the inference library that executes a model. mavor has exactly two: whisper.cpp, and ONNX Runtime reached through sherpa-onnx. **A runtime is never configured.** It is a property of the model, recorded in the catalog: `whisper-*` names run on whisper.cpp, everything else runs on sherpa-onnx.
- **Placement** — where that runtime executes relative to the daemon process. It has a different default answer for each runtime, and mavor derives it.

So the one line most users write is the model:

```toml
model = "whisper-base.en"
```

Everything below follows from it, and `mavor doctor` reports what was derived.

### 2.1 The placement values

| Placement | What it means | Model stays warm |
|---|---|---|
| `in-process` | The runtime is linked into the daemon and the model is resident. | Yes, for the life of the daemon |
| `local-server` | mavor starts and supervises a child `whisper-server` holding the model, and posts audio to it over loopback HTTP. | Yes, for the life of the child |
| `subprocess` | A fresh `whisper-cli` per utterance; the model is loaded and freed each time. | No |
| `remote` | An HTTP server someone else runs, named by URL in `advanced.server`. | Not mavor's problem |

### 2.2 What exists, per runtime

| Runtime | `in-process` | `local-server` | `subprocess` | `remote` |
|---|---|---|---|---|
| whisper.cpp (`whisper-*` models) | not built | ✅ **the default** | ✅ `whisper-cli` | ✅ any HTTP URL |
| sherpa-onnx (every other model) | ✅ cgo, **the default** | not built | not built | not built |

Two cells are the defaults, and both are derived from the model name. You override placement only to get a *different* behavior, and `advanced.placement` accepts exactly two values for that reason:

```toml
[advanced]
placement = "subprocess"          # whisper models only: one whisper-cli per utterance
server = "http://box.lan:8080"    # or: send audio to a whisper server you run
```

Asking for a placement the model's runtime cannot provide — `subprocess` on a sherpa model, or `advanced.server` alongside one — is a config error the daemon refuses at start rather than ignoring quietly.

> [!NOTE]
> **Does a sherpa model run in server or CLI mode?** Neither. It runs
> **in-process**: the model is loaded into the daemon through cgo and stays
> resident for the life of the daemon. That is why there is nothing to install
> and nothing to supervise for those models — and why they hold their weights
> in RAM the whole time the daemon is up.

### 2.3 Why whisper defaults to a warm local server

A supervised `whisper-server` pays the model load once, at daemon start, instead of once per utterance. Measured, from [`model-benchmarks.md`](./reports/model-benchmarks.md):

| Model | Warm server | Cold subprocess | Saved per utterance |
|---|---:|---:|---:|
| `whisper-tiny.en` | 550 ms | 809 ms | 259 ms |
| `whisper-base.en` | 1.30 s | 1.51 s | 207 ms |
| `whisper-small.en` | 3.31 s | 4.77 s | 1.45 s |

Startup cost is 325–551 ms, paid once when the daemon starts. The trade is resident memory: the child holds the model for as long as the daemon runs, where `subprocess` holds nothing between dictations.

> [!NOTE]
> The default placement wants a `whisper-server` binary on `$PATH`. If none is
> found, mavor falls back to `subprocess` and says so — once in the daemon log
> and in `mavor doctor` — because the placement was derived rather than
> requested, so the cost is a warm model and nothing else. Dictation still
> works; every utterance reloads the model. Install a whisper.cpp that ships
> the server to get the warm one back, or set
> `advanced.placement = "subprocess"` to make the choice explicit and silence
> the warning. A placement you name yourself is never rewritten.

### 2.4 Choosing between the two runtimes

| | whisper.cpp (`whisper-*`) | sherpa-onnx (everything else) |
|---|---|---|
| **Resident memory** | ~0 MB with `subprocess`; the model's size while a warm server runs | 150 MB – 2.3 GB, held for the life of the daemon |
| **Decodes as you speak** | No — whisper transcribes in 30-second windows | Only the models `mavor models list` marks `STREAM yes` |
| **Vocabulary biasing** | Initial prompt, capped at 224 tokens | Transducer models only, as a hotwords file |
| **GPU** | Whatever backend the whisper.cpp build loaded | None — the vendored ONNX Runtime is a CPU-only build |
| **Best for** | Almost everyone. `whisper-base.en` is the default for measured reasons | Non-English coverage, or a model that streams |

> [!NOTE]
> A sherpa model's advantage is not raw speed: `whisper-base.en` transcribes
> faster than every sherpa model measured. What it buys is incremental
> decoding, language coverage, and no per-dictation model load. Numbers in
> [`choosing-a-model.md`](./choosing-a-model.md).

---

## 3. Requirements & Dependencies

### Runtime Dependencies

| Package / Tool | Component | Purpose |
|---|---|---|
| `sway` / wlroots | Compositor | Layer-shell floating HUD overlay & window management |
| `pipewire`, `pulseaudio-utils` | Audio Stack | Audio recording (`parec`) and volume ducking (`pactl`) |
| `wtype` | Virtual Keyboard | Synthetic keystroke injection into focused window |
| `wl-clipboard` | Clipboard | `wl-copy` synchronization for instant pasting |
| `whisper-cpp` | whisper.cpp runtime | `whisper-server` for the default `local-server` placement, `whisper-cli` for `subprocess`. Not needed at all if `model` names a sherpa model |

### Build Prerequisites

- Go ≥ 1.26
- `just` task runner
- A C compiler (`gcc` or `clang`). This is not optional: mavor links the
  in-process sherpa-onnx recognizers, so `CGO_ENABLED=0` does not build. There
  is one build and no `sherpa` build tag — see
  [§4 of the configuration design](./design/configuration-surface.md#4-the-build-is-cgo-always).
- No development headers beyond that. sherpa-onnx's Go binding vendors its own
  prebuilt shared objects; nothing has to be installed system-wide.

---

## 4. Installation & Deployment

### Quick Install (`just install`)

Builds the binary, installs it to `~/.local/bin/mavor`, and puts the two
sherpa-onnx shared objects it links against in `~/.local/lib` — the binary is
linked to look there, and will not start without them:

```console
$ git clone https://github.com/mschulkind-oss/mavor && cd mavor
$ mise install     # ensures pinned Go version
$ just install     # builds binary and installs to ~/.local/bin/mavor
```

Then make the configuration runnable:

```console
$ mavor setup      # scaffolds config.toml and pulls every model it names
```

`mavor setup` is idempotent and config-driven: it downloads the main model and
the preview companion, skips whatever is already present, and can be re-run
after any edit to `config.toml`. After it exits zero, `mavor daemon` starts
with that config and needs no further downloads.

### Full Deployment (`just deploy`)

Installs the binary to `~/.local/bin/mavor` and sets up the systemd user service (`~/.config/systemd/user/mavor.service`):

```console
$ just deploy
```

### Cross-compiling

Not without a cross toolchain. mavor is a cgo program, and `go build` for
another architecture fails inside Go's own runtime rather than in mavor:

```console
$ GOOS=linux GOARCH=arm64 go build ./cmd/mavor
# runtime/cgo
gcc_arm64.S:30: Error: no such instruction: `stp x29,x30,[sp,'
```

Build on the target architecture, or install an `aarch64-linux-gnu` cross
toolchain and point `CC` at it. This is also why releases publish
`linux/amd64` only.

---

## 5. Self-Documenting CLI & Environment Diagnostics

`mavor` provides built-in tools for environment inspection and configuration setup:

### Environment Diagnostics (`mavor doctor`)

`mavor doctor` is the second half of the config file: the file says what you
asked for, `doctor` says what this machine will actually do with it. Every
setting whose effect depends on hardware or on installed tools is reported
here.

```console
$ mavor doctor
mavor doctor — system and environment verification
==================================================
❌ Wayland session:             No Wayland session detected ($WAYLAND_DISPLAY unset; fix: run inside a Wayland session)
✅ Audio capture (parec/Pulse): parec available (audio server check skipped/idle)
✅ Virtual typing (wtype):      wtype installed at /bin/wtype
✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
✅ Runtime and placement:       whisper.cpp, local-server — whisper models default to a supervised warm whisper-server
✅ Inference threads:           6 (this machine's physical core count; 12 logical)
✅ GPU acceleration:            CPU only (whisper-cli loaded no GPU backend — the stock build ships CPU backends only; install a whisper.cpp built with -DGGML_VULKAN=ON for acceleration)
✅ Configuration file:          valid config (model=whisper-base.en, preview=auto)
✅ Voice model availability:    whisper-base.en found at /home/you/.cache/mavor/models/ggml-base.en.bin
✅ Live preview source:         companion (zipformer-streaming-20m) — "whisper-base.en" does not decode incrementally, so the streaming companion "zipformer-streaming-20m" runs alongside it
✅ Vocabulary biasing:          no [vocabulary] configured — nothing is biased
❌ Daemon socket status:        daemon is not running at /run/user/1000/mavor.sock (run 'mavor daemon' or 'mavor service start')
✅ Systemd user service:        systemd unit not installed (optional; run 'mavor service install' to enable)
==================================================
❌ 2 check(s) failed. Fix the issues above before running mavor.

💡 Tip: Run 'mavor doctor --fix' (or 'mavor setup') to automatically configure mavor and download the default model.
```

That run is from a headless machine with no Wayland session and no daemon
started, which is what the two ❌ lines say. The five lines in the middle are
the ones to read after a config edit: which runtime and placement were chosen
and why, the thread count and where it came from, whether a GPU backend
actually loaded, where the preview text comes from, and whether the vocabulary
can reach this model at all.

### Configuration Management (`mavor config`)

```console
$ mavor config init          # create ~/.config/mavor/config.toml with commented defaults
$ mavor config init --force  # overwrite existing configuration with defaults
$ mavor config show          # print resolved active configuration
$ mavor config path          # print canonical path to config.toml
```

The scaffolded file is generated from the compiled defaults rather than written
out a second time, so the file you get and the behavior you get cannot
disagree.

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

Configuration is read **once, at daemon start**. There is no hot reload: a
change needs `mavor service restart` (or a stop and a restart of
`mavor daemon`). `mavor config show` and `mavor doctor` read from disk on every
invocation, so they always show the file as it is now, not as the running
daemon loaded it.

### Annotated `config.toml`

This is exactly what `mavor config init` writes — the commented-out lines are
documentation, not settings, because the value beside each one is already the
default. The three `[paths]` values and the `threads` comment are printed with
this machine's own values filled in.

```toml
# ~/.config/mavor/config.toml
#
# Every value has a working default; delete a line to get it back.
# Run `mavor doctor` after editing — it checks each setting against this
# machine and reports what the daemon will actually do with it.

# The model that produces your text. `mavor models list` shows every choice
# with its size, speed and accuracy, and marks what is installed.
model = "whisper-base.en"

[preview]
# Text in the overlay while you speak. The final text always comes from
# `model`, typed once when you release the key. The preview types nothing.
enabled = true

# Where the preview text comes from.
#   "auto"       — read `model` directly if it decodes as you speak;
#                  otherwise run a small streaming model alongside it;
#                  otherwise fall back to "phrases"
#   "phrases"    — no second model: re-transcribe with `model` at each
#                  pause. Cheaper, slower, and prone to filling silence
#                  with words you did not say
#   <model name> — run that model alongside as the preview source
source = "auto"

# "phrases" only: how long a pause ends a phrase, and how much speech a
# phrase needs before a pause can end it.
# pause_ms = 450
# min_phrase_ms = 600

[ducking]
# Lower other audio while recording.
enabled = false
volume = "0%"                     # "0%" mutes; "25%" merely lowers
# apps = ["spotify", "firefox"]   # only these; the default is every stream
# sink = ""                       # a specific output, not the default one

[vocabulary]
# Words the model gets wrong: names, jargon, commands. whisper models take
# these as a prompt; transducer models (parakeet, zipformer) boost them
# while decoding. Other models cannot use them and `mavor doctor` says so.
# words = ["mavor", "wlroots", "Schulkind"]
# file  = "~/.config/mavor/vocabulary.txt"   # one phrase per line
# boost = 1.5   # transducers only. 1.5 to 3.0 is the useful range; higher
#               # makes these words appear where they were not said.

[overlay]
top_margin = 8   # px below the top of the usable area, under your bar

# Chosen for you. Override only if `mavor doctor` gives you a reason to.
[advanced]
# placement = "auto"     # "auto", or "subprocess" for whisper models
# server = "http://…"    # send audio to a whisper server you run instead
# threads = 6            # default: this machine's physical core count
# gpu = "auto"           # "auto" or "off". whisper only — sherpa models
#                        # run on the CPU whatever this says.

[paths]
# models = "/home/you/.cache/mavor/models"
# log    = "/home/you/.local/state/mavor/daemon.log"
# socket = "/run/user/1000/mavor.sock"
```

### 7.1 `model` — the one key most people set

A catalog name, as `mavor models list` prints it: `whisper-base.en`,
`parakeet-tdt-0.6b`, `canary-180m`. It is **not** a filename — a whisper model
keeps the name upstream serves it under (`whisper-base.en` is the file
`ggml-base.en.bin`), and mavor maps between the two.

Every name carries its model family as a prefix. There are no bare names and
no aliases: `base.en`, `tiny`, `zipformer` and `parakeet-tdt` do not resolve.

A name the catalog does not carry is looked up as a directory under the sherpa
model directory before failing — that is how a hand-installed model is named,
and [§8.4](#84-installing-a-custom-sherpa-model) is the walkthrough. If neither
resolves, the daemon **refuses to start** and names the closest catalog
entries. A model name is a request, never a hint.

### 7.2 `[preview]` — text in the overlay while you speak

**Preview** is the text mavor paints in the overlay while you are speaking. It
is never typed and never reaches the clipboard. The final transcript always
comes from `model`, produced once, when you release the key — partial results
are provisional, and typing them would insert the same words twice.

`source = "auto"` resolves in this order, once, at daemon start:

1. **`model` already decodes incrementally** (the models `mavor models list`
   marks `STREAM yes`). Its own partial output is painted. No second model is
   loaded.
2. **The companion model is installed.** A **companion model** is a small
   streaming recognizer loaded alongside the main model, fed the same audio,
   emitting partial text continuously; it never contributes to the final
   transcript. The designated one is `zipformer-streaming-20m`, and
   `mavor setup` pulls it.
3. **Otherwise, phrase mode.** No second model: when you pause, the audio since
   the last pause is transcribed with the main model and appended to the
   preview. `mavor doctor` names the model to pull for a better preview.

Explicit values override the order:

| `source` | Effect |
|---|---|
| `"auto"` | The three-step resolution above |
| `"phrases"` | Phrase mode, even when a companion is installed |
| a model name | That model runs alongside as the preview source |
| the same value as `model` | Case 1 — never loads the same model twice. Falls to phrase mode if that model does not stream |

> [!WARNING]
> **A model named in `preview.source` and not installed is fatal at daemon
> start**, naming the model and the directory searched. Only `"auto"` degrades:
> it warns, falls back to phrase mode, and tells you what to pull. A name you
> wrote is a request, and mavor never substitutes something else for it.

Phrase mode is the fallback rather than the default for two structural reasons:
whisper hallucinates on short clips, frequently by repeating the previous
phrase; and each phrase is decoded with no context from the last. `pause_ms`
and `min_phrase_ms` tune it and do nothing in any other mode — a phrase ends
after `pause_ms` (default 450 ms) of silence, once at least `min_phrase_ms`
(default 600 ms) of speech has accumulated.

Set `enabled = false` and the overlay shows only that mavor is recording.

### 7.3 `[ducking]` — lowering other audio

```toml
[ducking]
enabled = true
volume = "25%"                    # "0%" mutes outright
apps = ["spotify", "firefox"]     # omit the key to duck every stream
sink = ""                         # a specific output, not the default one
```

Ducking is **off** by default. It applies on entering `recording` and is
restored on leaving it, including when transcription errors out. Naming `apps`
narrows it to those streams, which is how you duck music without ducking a
voice call; leaving the key out ducks everything.

### 7.4 `[vocabulary]` — words the model gets wrong

One runtime-neutral table for names, jargon and commands. `words` and `file`
are unioned, `words` first, duplicates dropped. What it maps to depends on the
model:

| Model kind | Mechanism | Notes |
|---|---|---|
| whisper (any `whisper-*`) | Initial prompt (`--prompt`) | Capped at 224 tokens upstream. A longer list is truncated at a phrase boundary and warned about once at load |
| Transducer (`parakeet-tdt-0.6b`, `zipformer-streaming`, …) | Hotwords file, one phrase per line | **Switches decoding to modified beam search**, because sherpa-onnx ignores hotwords under greedy decoding without complaint |
| CTC, paraformer, moonshine, sensevoice | Nothing | sherpa-onnx implements biasing inside transducer beam search only. The phrases are ignored, and `mavor doctor` says so rather than failing |

`mavor models list --verbose` prints the mechanism per model, and the
`Vocabulary biasing` line in `mavor doctor` reports what your configured model
will actually do with the list:

```console
✅ Vocabulary biasing:          3 phrase(s) → a hotwords file at /run/user/1000/mavor-hotwords.txt, with boost 1.5, decoded by modified beam search
```

`boost` is a per-token score added while decoding whenever a hypothesis extends
a listed phrase. It applies to transducers only. 1.5 to 3.0 is the useful
range; above that, listed words start appearing where they were not said, and
`doctor` reports the value as likely to do so.

**There is no decoding-method key.** Greedy is the default, beam search is what
hotwords require, and mavor makes that switch itself. On LibriSpeech the
difference between the two is 0.02% absolute word error rate for several times
the decoder work — see
[§7 of the configuration design](./design/configuration-surface.md#7-vocabulary-and-decoding).

An unreadable `vocabulary.file` is a warning, not a failure: mavor proceeds
with `words` alone.

### 7.5 `[overlay]`

`top_margin` is the gap in pixels between the overlay and the top of the
*usable* area — which is below your bar, not the screen edge. The overlay never
claims an exclusive zone, so the compositor places it inside the space Waybar
has already reserved; a bar of any height, or no bar at all, needs no change
here. Negative values are clamped to 0.

### 7.6 `[advanced]` — chosen for you

A key belongs here only if mavor cannot compute the right value.

| Key | Values | Default |
|---|---|---|
| `placement` | `"auto"`, or `"subprocess"` for a whisper model | `"auto"` — see [§2](#2-how-a-model-runs-runtime-and-placement) |
| `server` | An `http://` URL of a whisper server you run | unset. Setting it makes `placement` irrelevant |
| `threads` | A thread count | This machine's **physical** core count |
| `gpu` | `"auto"` or `"off"` | `"auto"` |

**Threads** default to physical cores rather than logical ones because that is
where the measured scaling curve flattens: on a 6-core/12-thread machine, 6
threads was best or within noise for every model and 8 bought nothing. A value
above the logical core count is honored and warned about in `doctor`; a value
of zero or less autodetects.

**GPU** is `auto` or `off`, and applies to whisper models only. whisper.cpp
uses a GPU when its build has a backend for one, for the whole model or not at
all; `off` maps to its `-ng` flag, which is what you want for a broken driver.
There is no layer count to set. Sherpa models run on the CPU whatever this
says, because the ONNX Runtime vendored by the Go binding is a CPU-only build.
`mavor doctor` reports which backend actually loaded, which is the only
reliable answer.

> [!WARNING]
> **`gpu_layers` is gone, and it was never a knob — it was a bug.** Any
> non-zero value made mavor pass `-ngl` to whisper.cpp, which rejects the flag,
> so every transcription failed. If you are carrying an old config, this is the
> key to look for.

### 7.7 `[paths]`

`models` is the model cache directory, `log` is the daemon log destination (the
`--log-file` flag overrides it for one run), and `socket` is the daemon's IPC
socket. All three accept `~` and `$VAR`.

### 7.8 Keys mavor does not recognize

An unknown key is warned about at daemon start, listed by name, and otherwise
ignored — it is never fatal at load. `mavor doctor` reports the same keys as an
error. A file in which **every** key is unknown is a config written against the
pre-rewrite schema: `doctor` says so plainly and points at
`mavor config init --force`. Downloaded models are untouched by that; the model
rename was a catalog-name change, and the files on disk keep upstream's names.

---

## 8. Model Management & Supported Architectures

`mavor` runs batch **Whisper GGML** models on whisper.cpp and **sherpa-onnx**
models in-process through cgo. The catalog is 26 models; `mavor models list`
prints it with sizes, languages, streaming support, and what is already
downloaded.

> [!TIP]
> **[`choosing-a-model.md`](./choosing-a-model.md) is the page that answers
> "which one?"** The table below is a summary of it. Both come from
> [`model-benchmarks.md`](./reports/model-benchmarks.md), which is generated by
> `just bench` — every figure was measured, none is a manufacturer claim.

### 8.1 The catalog

```console
$ mavor models list
Model cache: /home/you/.cache/mavor/models

NAME                     ENGINE       SIZE  LANGUAGES            STREAM  STATUS
whisper-tiny             whisper   74.1 MB  multi (99)           no      –
whisper-tiny.en          whisper   74.1 MB  en                   no      –
whisper-base             whisper  141.1 MB  multi (99)           no      –
whisper-base.en          whisper  141.1 MB  en                   no      –  ★
whisper-small            whisper  465.0 MB  multi (99)           no      –
whisper-small.en         whisper  465.0 MB  en                   no      –
whisper-medium           whisper   1.43 GB  multi (99)           no      –
whisper-medium.en        whisper   1.43 GB  en                   no      –
whisper-large-v3         whisper   2.88 GB  multi (99)           no      –
whisper-large-v3-turbo   whisper   1.51 GB  multi (99)           no      –
whisper-distil-large-v3  whisper   1.42 GB  en                   no      –
fastconformer-streaming  sherpa   429.4 MB  en                   yes     –
parakeet-tdt-0.6b        sherpa   464.6 MB  multi (25)           no      –
parakeet-unified-en      sherpa   478.1 MB  en                   no      –
parakeet-ctc             sherpa   582.4 MB  en                   no      –
canary-1b                sherpa    1.07 GB  multi (25)           no      –
canary-180m              sherpa   146.6 MB  en, es, de, fr       no      –
moonshine-tiny           sherpa   102.6 MB  en                   no      –
moonshine-base           sherpa   239.2 MB  en                   no      –
sensevoice-small         sherpa   999.3 MB  zh, en, ja, ko, yue  no      –
paraformer               sherpa   950.4 MB  zh                   no      –
zipformer-streaming      sherpa   296.0 MB  en                   yes     –
zipformer-streaming-20m  sherpa   122.0 MB  en                   yes     –
zipformer-offline        sherpa   293.4 MB  en                   no      –
zipformer-ctc            sherpa   365.4 MB  en                   no      –

★ active   ✓ downloaded   – not downloaded
SIZE is the download; sherpa archives expand to roughly twice that on disk.
Download one with `mavor models pull <name>`.
```

That is a fresh machine: nothing downloaded, and ★ marking the model the
config names. `--installed` narrows the listing to what is in the cache,
`--verbose` prints a block per model adding speed,
vocabulary biasing, GPU support and the source URL, and `--json` emits the same
catalog machine-readably.

### 8.2 Supported model matrix

Times are for 20 seconds of speech on CPU; **Format** is whether the model
returns punctuated, capitalised text or a bare lowercase word stream.

| Model | Runtime | Architecture | Time | RAM | Format | Use it for |
|---|---|---|---:|---:|---|---|
| `whisper-base.en` | whisper.cpp | Whisper GGML | 1.63 s | 302 MB | Full | **The default.** Best accuracy measured. |
| `whisper-tiny.en` | whisper.cpp | Whisper GGML | 1.05 s | 196 MB | Full | The lightest option that still formats. |
| `whisper-small.en` | whisper.cpp | Whisper GGML | 5.10 s | 768 MB | Full | Little gain over `whisper-base.en` here. |
| `whisper-large-v3-turbo` | whisper.cpp | Whisper GGML | 21.01 s | 1.81 GB | **None** | Not recommended — see the warning below. |
| `canary-180m` | sherpa-onnx | NeMo Canary | 4.40 s | 457 MB | Full | Best sherpa model; en/es/de/fr. |
| `parakeet-tdt-0.6b` | sherpa-onnx | NeMo transducer | 5.82 s | 1.56 GB | Full | 25 languages, and hotwords work on it. |
| `sensevoice-small` | sherpa-onnx | SenseVoice | 3.88 s | 1.46 GB | Good | zh, en, ja, ko, yue. |
| `zipformer-streaming` | sherpa-onnx | Zipformer (online) | 4.65 s | 150 MB | Minimal | Streaming: first token in 107 ms. |

> [!WARNING]
> **The largest Whisper models return unpunctuated lowercase text.**
> `whisper-large-v3`, `whisper-large-v3-turbo`, `whisper-distil-large-v3` and
> `whisper-medium.en` all emit `lux is in the pit he cannot sit still` where
> `whisper-base.en` emits `Lux is in the pit. He cannot sit still.` Word error
> rate is the same; the output is not. `whisper-large-v3` is also 20x slower
> than `whisper-base.en` on CPU and wants 3.9 GB of RAM.
> [Details](./choosing-a-model.md#do-not-reach-for-the-biggest-model).

GPU changes the calculation for the larger models but not their formatting: a
Vulkan build (`just bench-gpu-build`) runs `whisper-medium.en` **12.8x** faster
and drops host memory from 2.07 GB to 174 MB, because the weights move to the
card. Sherpa models have no GPU path — the vendored ONNX Runtime ships no
execution providers.

### 8.3 Downloading and switching models

`mavor models pull` downloads, verifies, and extracts model archives into your
cache directory. Names are the prefixed catalog names:

```console
# The default, and the best measured model
$ mavor models pull whisper-base.en

# Best sherpa model: en/es/de/fr, formats well
$ mavor models pull canary-180m

# The preview companion, which `mavor setup` also pulls
$ mavor models pull zipformer-streaming-20m
```

See [`choosing-a-model.md`](./choosing-a-model.md) before pulling one of the
large Whisper models; they are slower and format worse than `whisper-base.en`.

To switch models, change one line and restart. Nothing else moves: the runtime
and its placement follow from the name.

```toml
model = "canary-180m"
```

```console
$ mavor models pull canary-180m
$ mavor service restart    # or: pkill -f 'mavor daemon' && mavor daemon
```

`mavor setup` is the shortcut for the two steps together — it pulls whatever
the current config names, including the preview companion, and skips what is
already there.

### 8.4 Installing a custom sherpa model

A model that is not in the catalog is configured by **where you put it**, not by
naming its files. The five `sherpa_*` file-path keys are gone; there is no
config surface that describes a model layout.

**1. Put the model directory under the sherpa model directory.** The directory
name is the name you will write in `config.toml`:

```console
$ mkdir -p ~/.cache/mavor/models/sherpa/my-custom-model
$ cp tokens.txt encoder.onnx decoder.onnx joiner.onnx \
     ~/.cache/mavor/models/sherpa/my-custom-model/
```

**2. Name it in `config.toml`:**

```toml
model = "my-custom-model"
```

**3. Restart the daemon and check `mavor doctor`.**

mavor resolves a name that is not in the catalog by trying, in order:

1. The value itself as a directory path, after `~` and `$VAR` expansion — so an
   absolute path works as a `model` value too.
2. `$XDG_DATA_HOME/mavor/models/sherpa/<name>`
3. `$XDG_DATA_HOME/mavor/models/<name>`
4. `<paths.models>/sherpa/<name>`
5. `<paths.models>/<name>`

If none of those is a directory, the daemon refuses to start and names both the
paths it searched and the catalog entries closest to what you wrote.

**The architecture is read from the files, not from the name.** An encoder,
decoder and joiner is a transducer; an encoder and decoder with no joiner is
Canary (or Whisper, if the name says so); a separate preprocessor is Moonshine;
a `config.yaml` beside a `tokens.json` is a paraformer; a `words.txt` beside
the tokens is a zipformer CTC. Filenames carrying a training run — the
`encoder-epoch-99-avg-1.onnx` form sherpa-onnx publishes — are matched too, and
a chunked encoder filename is what tells a streaming transducer from an offline
one.

Where the layout is genuinely ambiguous — SenseVoice, NeMo CTC and a bare
zipformer CTC can all ship as nothing but `model.onnx` plus `tokens.txt` — the
directory name breaks the tie, so include `sensevoice`, `paraformer` or
`zipformer` in it. A directory mavor cannot identify at all is an error naming
the directory, and the message says what this section says: there is no config
key that describes a layout. Install it the way `mavor models pull` installs
one.

Two consequences worth knowing before you rely on a hand-installed model:

- **It is never assumed to stream.** The catalog is what records incremental
  decoding, and a model that is not in it has no entry to consult — so
  `preview.source = "auto"` will load the companion or fall back to phrase mode
  rather than reading partials from your model.
- **`mavor doctor` cannot say in advance whether vocabulary reaches it.** It
  reports that the phrases become a hotwords file *if* the model turns out to
  be a transducer, and nothing otherwise.

---

## 9. Development & Quality Gate

All development commands are unified in the [`Justfile`](../Justfile):

```console
$ just check-ci   # Read-only quality gate (format check + lint + unit tests)
$ just check      # Local dev gate (auto-formats + lints + runs tests)
$ just format     # Formats all Go files in-place
$ just lint       # Runs static analysis and linter checks
$ just test       # Runs fast unit tests
$ just test-int   # Runs headless Sway + PipeWire integration test harness
$ just test-e2e   # End-to-end smoke test with real whisper transcription
$ just storybook  # Generates HTML visual storybook report with real screenshots
$ just build      # Builds bin/mavor and its sherpa-onnx shared objects
$ just install    # Installs the binary to ~/.local/bin and its libs to ~/.local/lib
$ just deploy     # Installs binary and sets up systemd user service
$ just bench      # Benchmarks every installed model; regenerates the report
$ just doctor     # Runs environment health check (mavor doctor)
$ just dev        # Runs daemon in foreground with verbose debug logging
$ just done       # Pre-commit quality verification
```

There is one build and it is cgo, so there is no `build-sherpa` recipe and no
`sherpa` build tag.

---

## 10. Troubleshooting

| Symptom | Cause | Solution |
|---|---|---|
| `model "whisper-base.en" not found at …` | Model file has not been downloaded | Run `mavor models pull whisper-base.en`, or `mavor setup` to install everything the config names |
| `model "base.en" is not in the catalog … did you mean "whisper-base.en"?` | A pre-rewrite name; bare names and aliases were deleted | Use the prefixed catalog name that `mavor models list` prints |
| Daemon refuses to start over `preview.source` | The named preview model is not installed, which is fatal by design | Pull it, or set `preview.source = "auto"` to let mavor degrade |
| Every setting appears to be ignored | The config predates the schema rewrite, so every key is unknown | `mavor doctor` reports it; `mavor config init --force` scaffolds the new file |
| `doctor` says the placement fell back to `subprocess` | No `whisper-server` on `$PATH`, so the model cannot be kept warm | Dictation still works, slower. Install a whisper.cpp that ships the server, or set `advanced.placement = "subprocess"` to silence it |
| `toggle: connect: no such file or directory` | Daemon is not running or socket mismatch | Run `mavor daemon -v` or `mavor doctor` to inspect status |
| Overlay does not appear | Compositor does not implement `wlr-layer-shell` | Ensure a wlroots session (sway, hyprland, river) is active; `mavor daemon -v` logs the reason it fell back to a silent overlay |
| Audio volume does not duck | Ducking is off by default | Set `enabled = true` under `[ducking]`, and check `apps` if you narrowed it |
| No text in the overlay while speaking | The preview is off, or fell back to phrase mode | `mavor doctor`'s `Live preview source` line names the mode and the reason; pull `zipformer-streaming-20m` for the low-latency preview |
| Preview shows words that were never said | Phrase mode feeding whisper short clips, which it fills with plausible text | Pull `zipformer-streaming-20m` so `auto` uses the companion instead, or turn the preview off with `enabled = false` |
| Vocabulary words still misheard | The model cannot be biased at all | `mavor doctor`'s `Vocabulary biasing` line says which mechanism applies; CTC, paraformer, moonshine and sensevoice have none |
| Ghost words typed during silence | Speech quiet enough to pass the energy gate, then hallucinated by whisper | Raise the input gain, or move closer to the microphone; the gate is an RMS threshold and cannot tell quiet speech from room noise |
| Text typed in wrong window | Focus shifted during transcription | Keep window focused until overlay closes |
| Systemd service fails to start | Audio socket or Wayland display not ready | Ensure `PartOf=graphical-session.target` and PipeWire is running |
