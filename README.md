# mavor — low-latency voice dictation

Tap a hotkey, talk, tap again. The transcribed text is typed into whatever
window has focus and copied to the clipboard. Everything runs locally — your
voice never leaves the machine. A small "● Recording" pill appears at the top
of the screen, clear of your bar, while you speak, with a live audio waveform
meter, and flips to an amber indicator while whisper.cpp or Sherpa-ONNX runs.

```
$mod + ` ──▶  ● Recording   ▂▃▅▆          (HUD overlay with live audio meter)
              talking talking talking
$mod + ` ──▶  ⟳ Transcribing…             (in-process CGO / whisper.cpp)
              ──▶ wtype + wl-copy         (text lands in focused app)
              overlay closes
```

CLI subcommands:

- `mavor daemon` — long-lived process. Owns the overlay, audio capture,
  the speech-to-text (STT) engine, PipeWire audio ducking, and a Unix-socket
  IPC server.
- `mavor start` / `mavor stop` — push-to-talk keybind controls (hold to speak).
- `mavor toggle` — toggle mode control (press once to start, press again to stop).
- `mavor doctor` — self-diagnostic health check for Wayland, audio, and tools.
- `mavor config init` — scaffold `~/.config/mavor/config.toml` with documented defaults.
- `mavor service install` — install and enable systemd user service (`mavor.service`).

> [!NOTE]
> **Where it runs today:** Linux, on a Wayland compositor implementing
> `wlr-layer-shell` (the overlay) and `virtual-keyboard-v1` (typing, via
> `wtype`) — sway, Hyprland, river, Wayfire, niri, labwc. Not GNOME, which
> implements neither.
>
> This is the first backend, not the design. Capture, transcription, overlay
> and text output are four independent interfaces (§[Project layout](#project-layout)),
> and only the overlay and the output emitter are Wayland-specific. Other
> compositors and platforms are a matter of writing those two, not of
> rearchitecting.

## Why this exists

Dictation on Linux tends to arrive as an appliance: one vendor's model, one
opinionated UI, a tray icon, and a service that wants to own your microphone.
Meanwhile the interesting work is happening in the models — whisper.cpp,
Parakeet, Zipformer, Moonshine, SenseVoice, Paraformer, a new one every few
months, each with its own build, its own model layout, and its own idea of what
an API is.

`mavor` is the boring layer under that. Three goals, in order:

**One interface in front of every model.** A single `Transcriber` contract with
an out-of-process `whisper-cli`, a warm HTTP or Unix-socket server, and
in-process sherpa-onnx behind it, so switching engines is a line in a config
file rather than a different program. `mavor models list` shows the whole
catalog — size, languages, whether it streams — and `mavor models pull` puts it
where the engine will find it. Trying a new model should cost a minute, not an
afternoon.

**Everything runs on your machine.** Your voice never leaves it. Transcription
is whisper.cpp or sherpa-onnx running locally against a model on your own disk
— there is no cloud API behind it, no account, no API key, and nothing to sign
up for. The only network call in the program is `mavor models pull`, which you
invoke, to fetch a model from Hugging Face or a GitHub release. The `server`
engine posts to an endpoint you configure, and that endpoint defaults to a Unix
socket: it is a `whisper-server` you run yourself to keep a model warm, not a
vendor. No telemetry, no analytics, no crash reporting. Unplug the network
after `mavor setup` and dictation still works.

**Minimal and unintrusive.** No tray icon, no window, no background service
listening for a wake word. A daemon that idles until you press a
key, a floating pill that appears while you speak and disappears when you stop,
and text in the window you were already typing in. It holds the microphone only
between `start` and `stop`, and the one piece of UI it draws is deliberately
the smallest thing that still tells you it is listening.

**A good citizen of a Wayland tiling session.** Built for compositors rather
than in spite of them: a `wlr-layer-shell` overlay that floats clear of your
bar instead of stealing focus or spawning a window your tiler has to place,
`wtype` for input so text lands in the focused surface through the compositor's
own protocols, and PipeWire ducking so whatever you were listening to gets out
of the way while you talk. It is driven entirely by keybinds and a Unix socket,
which is what makes it scriptable rather than clickable.

## Install

### Go

```bash
go install github.com/mschulkind-oss/mavor/cmd/mavor@latest
```

### Release binary

Each tagged release publishes a `linux/amd64` tarball and a `checksums.txt`
on the [releases page](https://github.com/mschulkind-oss/mavor/releases).

```bash
tar -xzf mavor_v0.1.0_linux_amd64.tar.gz
install -m 0755 mavor ~/.local/bin/mavor
```

### From source

```bash
git clone https://github.com/mschulkind-oss/mavor
cd mavor
mise install                 # gets the right toolchain
just install                 # builds and copies binary to ~/.local/bin/mavor

# Or deploy binary + install systemd user service in one step:
just deploy
```

## Quick Start & Verification

Run the built-in diagnostic tool to verify your Wayland and audio environment:

```bash
mavor doctor
```

Initialize your configuration file:

```bash
mavor config init
```

## Compositor integration

### Push-to-Talk Mode (Recommended)

```
# ~/.config/sway/config
exec mavor daemon
bindsym $mod+grave exec mavor start
bindsym --release $mod+grave exec mavor stop
```

### Toggle Mode

```
# ~/.config/sway/config
exec mavor daemon
bindsym $mod+grave exec mavor toggle
```

The overlay is a `wlr-layer-shell` surface on the `top` layer and does **not** request
an exclusive zone, which means two things: it floats over your content without
resizing windows, and the compositor places it *inside* the space other bars
have reserved. `top_margin` is therefore a gap below Waybar, not an offset from
the screen edge — a bar of any height, or no bar at all, works without
configuring anything.

## Systemd User Service

Alternatively, run the daemon as a systemd user unit that starts automatically with your graphical session:

```bash
mavor service install --start
mavor service status
```

## Configuration

`$XDG_CONFIG_HOME/mavor/config.toml` (defaults to `~/.config/mavor/config.toml`).
All paths support `~` and `$ENVIRONMENT_VARIABLES`. Run `mavor config show` to inspect.

```toml
top_margin   = 8                             # px between screen top and overlay
engine       = "cli"                         # "cli" (whisper.cpp) or "sherpa" (CGO ONNX)
model        = "base.en"                     # whisper ggml model name
model_dir    = "~/.cache/mavor/models"       # where downloaded models live
duck_audio   = true                          # duck music/browser audio while recording
duck_volume  = "0%"                          # level while recording ("0%" mutes; raise to merely lower)
duck_streams = ["spotify", "firefox"]        # target specific media apps
socket       = "$XDG_RUNTIME_DIR/mavor.sock" # daemon IPC socket
```

## Models

Models are downloaded explicitly, never at runtime — the daemon fails at
startup with a `mavor models pull` hint rather than stalling a dictation on a
multi-gigabyte fetch.

`mavor models list` shows everything available, with what is already in the
cache marked:

```
NAME                 ENGINE       SIZE  LANGUAGES            STREAM  STATUS         ALIASES
tiny.en              whisper   74.1 MB  en                   no      –              whisper-tiny.en
base.en              whisper  141.1 MB  en                   no      ✓ 141.1 MB  ★  whisper-base.en
large-v3-turbo       whisper   1.51 GB  multi (99)           no      –              whisper-large-v3-turbo
parakeet             sherpa   429.4 MB  en                   yes     –              parakeet-tdt
sensevoice-small     sherpa   999.3 MB  zh, en, ja, ko, yue  no      –              sensevoice
zipformer-streaming  sherpa   296.0 MB  en                   yes     –              zipformer

★ active   ✓ downloaded   – not downloaded
```

- **STREAM** marks models that decode incrementally as you speak. Whisper is
  encoder-decoder over 30-second windows, so it always transcribes after you
  stop; the streaming sherpa transducers do not.
- **SIZE** is the download. The sherpa archives expand to roughly twice that
  on disk.
- **ALIASES** are alternate names accepted by `mavor models pull` and by
  `model` / `sherpa_model` in `config.toml`.

```bash
mavor models list                # the catalog above
mavor models list --installed    # only what is downloaded
mavor models list --verbose      # a block per model, with the detail below
mavor models pull base.en        # production default
mavor models pull tiny.en        # smallest; what the test suite uses
```

`--verbose` adds the properties that do not fit a column:

```
parakeet
  NeMo FastConformer transducer, 80ms chunk — decodes while you speak
  engine      sherpa (in-process sherpa-onnx, CGO)
  download    429.4 MB
  languages   en
  streaming   yes — decodes incrementally while you speak
  speed       fast (relative tier, not measured)
  vocabulary  hotwords via sherpa_hotwords_file
  gpu         none in practice — the bundled ONNX Runtime is a CPU-only build
  aliases     parakeet-tdt
  status      ✓ downloaded (850.7 MB)
  source      https://github.com/k2-fsa/sherpa-onnx/releases/...
```

- **speed** is a relative tier across the catalog, estimated from architecture
  and parameter count. Where a real benchmark exists it is labelled `measured`
  and carries the real-time factor from [`docs/reports/`](docs/reports/).
- **vocabulary** is what biasing the model can take. sherpa-onnx implements it
  by boosting paths during transducer beam search, so only the transducers can
  use a `sherpa_hotwords_file`; the CTC and encoder-decoder models cannot.
  Whisper models take none today — mavor does not pass an initial prompt.
- **gpu** depends on the build you are running, not on the model. Run
  `mavor doctor`, which reports what your whisper.cpp and ONNX Runtime can
  actually use rather than what the config asks for.

Whisper models are fetched from the whisper.cpp GGML repository and land in
`model_dir` as `ggml-<name>.bin`. Sherpa models come from the sherpa-onnx
release assets and unpack into `model_dir/sherpa/<name>/`.

## Development

### Dev container

`yolo-jail.jsonc` is a committed [yolo-jail](https://github.com/mschulkind-oss/yolo-jail)
definition: `yolo` from the repo root drops you in a container with the whole
toolchain already present — sway and waybar for the headless integration tests,
grim for the screenshot assertions, PipeWire and `pulseaudio` utilities for
audio capture, `wtype` and `wl-clipboard`, and `whisper-cpp`. Optional; nothing
in the build depends on it.

`just --list` for the full set. The interesting ones:

| target          | what it runs                                                  |
|-----------------|---------------------------------------------------------------|
| `just check`    | format + vet + unit tests (fast dev gate)                     |
| `just check-ci` | read-only CI / pre-commit verification                        |
| `just test`     | unit tests only — fast, no Wayland required                   |
| `just test-int` | integration tests: spawns headless sway + waybar + daemon     |
| `just test-e2e` | e2e: real whisper transcription with the `tiny.en` model      |
| `just storybook`| runs UI storybook test and produces HTML screenshot report    |
| `just install`  | builds and installs binary to `~/.local/bin/mavor`            |
| `just deploy`   | installs binary and sets up systemd user service              |
| `just doctor`   | runs environment health check (`mavor doctor`)                |
| `just build`    | compiles binary at `bin/mavor`                                |
| `just dev`      | runs the daemon against your live Wayland session, verbose    |

### Test layout

- **Unit tests** (`go test ./...`): pure Go, mocked Recorder/Transcriber/
  Overlay/Output. Fast, run under `-race`.
- **Integration tests** (`go test -tags=integration ./test/integration/...`):
  spin up a real headless wlroots sway, optionally waybar, the real daemon
  binary, a host PipeWire null-sink, and assert against grim screenshots
  and `wl-paste` output.
- **End-to-end test** (`go test -tags=e2e ./...`): real whisper-cli plus
  a downloaded model.

The integration test rig lives in `test/integration/harness.go`. Each
test gets its own `XDG_RUNTIME_DIR`, dbus session, headless sway, and
optionally waybar + null-sink. Cleanup happens in `t.Cleanup`.

### Project layout

```
cmd/mavor/                   # CLI entrypoint & subcommands (daemon, doctor, config, service, models)
internal/state/              # Idle ⇄ Recording ⇄ Transcribing FSM
internal/audio/              # Recorder interface + parec impl + VAD + PipeWire ducking
internal/speech/             # Pluggable STT engines (whisper-cli, sherpa-onnx CGO, HTTP server)
internal/overlay/            # Layer-shell HUD: paint.go renders, overlay_wl.go presents
internal/wayland/            # Minimal hand-written Wayland client (wire protocol, layer-shell, shm)
internal/ipc/                # JSON-over-Unix-socket protocol
internal/output/             # wtype + wl-copy dispatch
internal/config/             # XDG_CONFIG_HOME/mavor/config.toml loader with ~/$VAR expansion
internal/daemon/             # wires everything; main.go is a thin caller
test/integration/            # headless sway + audio-stack test harness
```

### Build tags

The default build is pure Go. `CGO_ENABLED=0` works, cross-compilation works,
and no system development headers are needed to build it.

- `sherpa`: link the in-process sherpa-onnx recognizers. This is the only
  variant that needs cgo, and therefore the only one that cannot be
  cross-compiled.
- `integration`: build the headless-sway test harness.
- `e2e`: opt in to tests that exercise real whisper-cli + a downloaded model.

## Built on

`mavor` is a thin daemon around other people's hard work. In rough order of how
much of the heavy lifting they do:

**Speech recognition**

- [whisper.cpp](https://github.com/ggml-org/whisper.cpp) — the `whisper-cli`
  and `whisper-server` binaries behind the `cli` and `server` engines, and the
  GGML model format the catalog pulls.
- [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) and its Go bindings,
  [sherpa-onnx-go](https://github.com/k2-fsa/sherpa-onnx-go) — the in-process
  `sherpa` engine, including the streaming transducers.
  ([ONNX Runtime](https://github.com/microsoft/onnxruntime) rides along inside
  the platform modules; a `sherpa`-tagged build links roughly 90 MB of it.)
- [OpenAI Whisper](https://github.com/openai/whisper),
  [NVIDIA NeMo](https://github.com/NVIDIA/NeMo) (Parakeet),
  [k2-fsa/icefall](https://github.com/k2-fsa/icefall) (Zipformer),
  [Useful Sensors](https://github.com/usefulsensors/moonshine) (Moonshine) and
  [FunASR](https://github.com/modelscope/FunASR) (SenseVoice, Paraformer) — the
  model families the catalog carries.

**Desktop integration**

- [wlroots](https://gitlab.freedesktop.org/wlroots/wlroots) and the
  [wlr-layer-shell](https://github.com/swaywm/wlr-protocols) protocol — what
  lets the overlay be an anchored surface rather than a window your tiler has
  to place. mavor speaks the protocol directly rather than through a library.
- [golang.org/x/image](https://pkg.go.dev/golang.org/x/image) — the rasterizer
  the pill is drawn with, and the Go font it is typeset in. Embedding the font
  is what makes the overlay render identically on every machine.
- [wtype](https://github.com/atx/wtype) — synthetic keystrokes over
  `virtual-keyboard-unstable-v1`.
- [wl-clipboard](https://github.com/bugaevc/wl-clipboard) — `wl-copy` and
  `wl-paste`.
- [PipeWire](https://pipewire.org/) and
  [PulseAudio](https://www.freedesktop.org/wiki/Software/PulseAudio/) utilities
  — `parec` for capture and `pactl` for ducking.
- [sway](https://swaywm.org/) and [wlroots](https://gitlab.freedesktop.org/wlroots/wlroots)
  — the compositor this is built for, and the headless one the integration
  tests run against.

**Go modules**

| Module | License |
|---|---|
| [`golang.org/x/image`](https://pkg.go.dev/golang.org/x/image) | BSD-3-Clause |
| [`golang.org/x/sys`](https://pkg.go.dev/golang.org/x/sys) | BSD-3-Clause |
| [`github.com/k2-fsa/sherpa-onnx-go`](https://github.com/k2-fsa/sherpa-onnx-go) | Apache-2.0 |
| [`github.com/k2-fsa/sherpa-onnx-go-linux`](https://github.com/k2-fsa/sherpa-onnx-go-linux) | Apache-2.0 |
| [`github.com/pelletier/go-toml/v2`](https://github.com/pelletier/go-toml) | MIT |
| [`golang.org/x/text`](https://pkg.go.dev/golang.org/x/text) | BSD-3-Clause |

**Testing**

- [grim](https://sr.ht/~emersion/grim) for the screenshots the integration
  suite and the UI storybook assert against, and
  [Waybar](https://github.com/Alexays/Waybar) as the bar the overlay has to
  stay clear of.
