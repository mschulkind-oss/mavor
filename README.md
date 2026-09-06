# mavor — local, low-latency voice dictation

Tap a hotkey, talk, tap again: the words are transcribed on your own machine
and typed into whatever window has focus. Your voice never leaves the box —
no cloud API, no account, nothing to sign up for. The text is copied to the
clipboard too, and a small "● Recording" pill sits at the top of the screen,
clear of your bar, showing a live waveform and a running preview of the words
while you speak. The preview is never what gets typed — the text you keep is
transcribed once, when you let go.

```
$mod + ` ──▶  ● Recording   ▂▃▅▆          (HUD overlay with live audio meter)
              talking talking talking
$mod + ` ──▶  ⟳ Transcribing  ● ● ●        (in-process CGO / whisper.cpp)
              ──▶ wtype + wl-copy         (text lands in focused app)
              overlay closes
```

CLI subcommands:

- `mavor setup` — one-shot first run: scaffold the config, install missing
  runtime tools, and download every model the config names.
- `mavor daemon` — long-lived process. Owns the overlay, audio capture,
  speech-to-text, PipeWire audio ducking, and a Unix-socket IPC server.
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
a supervised warm `whisper-server`, a one-shot `whisper-cli`, and in-process
sherpa-onnx behind it. You do not choose between them: switching models is the
one line `model = "…"` in a config file, and the model decides the rest — a
whisper model runs on whisper.cpp, everything else on ONNX Runtime through
sherpa-onnx. `mavor models list` shows the whole catalog — size, languages,
whether it streams — and `mavor models pull` puts it where mavor will find it.
Trying a new model should cost a minute, not an afternoon.

**Everything runs on your machine.** Your voice never leaves it. Transcription
is whisper.cpp or sherpa-onnx running locally against a model on your own disk
— there is no cloud API behind it, no account, no API key, and nothing to sign
up for. The only network call in the program is `mavor models pull`, which you
invoke, to fetch a model from Hugging Face or a GitHub release. Whisper models
do go over HTTP, and it is loopback: mavor starts and supervises its own
`whisper-server` child to keep the model warm. `advanced.server` can point that
at a server you run yourself instead — still yours, still not a vendor. No
telemetry, no analytics, no crash reporting. Unplug the network after
`mavor setup` and dictation still works.

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
The tarball holds three files, and `mavor` needs the other two: it is
dynamically linked against sherpa-onnx. Keep them together — either beside the
binary, or one directory up in `lib/`, which is where the binary looks.

```bash
tar -xzf mavor_v0.1.0_linux_amd64.tar.gz
install -m 0755 mavor ~/.local/bin/mavor
install -m 0644 -D -t ~/.local/lib lib*.so     # ~/.local/bin/../lib
```

`linux/arm64` is not published yet: mavor links sherpa-onnx through cgo, which
the amd64 release builder cannot cross-compile. Build from source on the
machine instead.

### From source

```bash
git clone https://github.com/mschulkind-oss/mavor
cd mavor
mise install                 # gets the right toolchain
just install                 # binary to ~/.local/bin, libraries to ~/.local/lib

# Or deploy binary + install systemd user service in one step:
just deploy
```

## Quick Start & Verification

The five-step version, with what each command prints and what to do when a
step does not land, is [`docs/quickstart.md`](docs/quickstart.md). The short
form:

```bash
mavor setup      # config, missing tools, every model the config names, systemd unit
mavor doctor     # what this machine will actually do with that config
```

`mavor setup` makes the current config runnable: it downloads the main model
*and* the small streaming model the live preview runs alongside it, skips
whatever is already in the cache, and is safe to re-run after you edit
`config.toml`. `mavor config init` scaffolds the file on its own if you would
rather start there.

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
have reserved. `overlay.top_margin` is therefore a gap below Waybar, not an
offset from the screen edge — a bar of any height, or no bar at all, works
without configuring anything.

## Systemd User Service

Alternatively, run the daemon as a systemd user unit that starts automatically with your graphical session:

```bash
mavor service install --start
mavor service status
```

## Configuration

`$XDG_CONFIG_HOME/mavor/config.toml` (defaults to `~/.config/mavor/config.toml`).
All paths support `~` and `$ENVIRONMENT_VARIABLES`. Run `mavor config show` to
inspect the resolved values, and `mavor config init` to scaffold the commented
file with every default in it.

One top-level key and six tables. The first line is the one a first-time user
touches; everything below it has a working default, and deleting a line gets
that default back.

```toml
model = "whisper-base.en"   # `mavor models list` shows every choice

[preview]
enabled = true              # live text in the overlay while you speak
source = "auto"             # "auto" | "phrases" | a model name

[ducking]
enabled = false             # lower other audio while recording
volume = "0%"               # "0%" mutes; "25%" merely lowers
# apps = ["spotify", "firefox"]

[overlay]
top_margin = 8              # px below the top of the usable area

[vocabulary]
# words = ["mavor", "wlroots", "Schulkind"]

# Chosen for you. Override only if `mavor doctor` gives you a reason to.
[advanced]
# threads = 6               # default: this machine's physical core count
# gpu = "auto"              # "auto" or "off"; whisper only

[paths]
# models = "~/.cache/mavor/models"
```

There is no `engine` key. The model decides its runtime, and where that runtime
runs is derived too — a warm supervised `whisper-server` for whisper models,
in-process sherpa-onnx for the rest. `mavor doctor` prints what it picked and
why.

Every key, with its units and failure modes, is in the
[User Guide](docs/user-guide.md).

## Models

Models are downloaded explicitly, never at runtime — the daemon fails at
startup with a `mavor models pull` hint rather than stalling a dictation on a
multi-gigabyte fetch.

`mavor models list` shows everything available, with what is already in the
cache marked:

```
Model cache: /home/you/.cache/mavor/models

NAME                     ENGINE       SIZE  LANGUAGES            STREAM  STATUS
whisper-tiny.en          whisper   74.1 MB  en                   no      –
whisper-base.en          whisper  141.1 MB  en                   no      ✓ 141.1 MB  ★
whisper-large-v3-turbo   whisper   1.51 GB  multi (99)           no      –
fastconformer-streaming  sherpa   429.4 MB  en                   yes     –
parakeet-tdt-0.6b        sherpa   464.6 MB  multi (25)           no      –
sensevoice-small         sherpa   999.3 MB  zh, en, ja, ko, yue  no      –
zipformer-streaming-20m  sherpa   122.0 MB  en                   yes     ✓ 130.1 MB
…

★ active   ✓ downloaded   – not downloaded
SIZE is the download; sherpa archives expand to roughly twice that on disk.
Download one with `mavor models pull <name>`.
```

That is seven of twenty-five rows; `mavor models list` prints them all.

- **Every name carries its model family**, and there are no aliases — one name
  per model. `whisper-base.en`, not `base.en`; a name that is not in the
  catalog is an error naming the closest entries, never a silent fallback.
- **STREAM** marks models that decode incrementally as you speak. Whisper is
  encoder-decoder over 30-second windows, so it always transcribes after you
  stop; the streaming sherpa transducers do not.
- **SIZE** is the download. The sherpa archives expand to roughly twice that
  on disk.

```bash
mavor models list                          # the catalog above
mavor models list --installed              # only what is downloaded
mavor models list --verbose                # a block per model, with the detail below
mavor models pull whisper-base.en          # production default
mavor models pull whisper-tiny.en          # smallest; what the test suite uses
mavor models pull zipformer-streaming-20m  # the live-preview companion
```

`--verbose` adds the properties that do not fit a column:

```
zipformer-streaming-20m
  Streaming Zipformer transducer, 20M parameters — small enough to run alongside another model as the live-preview source
  engine      sherpa (in-process sherpa-onnx, CGO)
  download    122.0 MB
  languages   en
  streaming   yes — decodes incrementally while you speak
  speed       fast (relative tier, not measured)
  vocabulary  hotwords supported (transducer)
  gpu         none in practice — the bundled ONNX Runtime is a CPU-only build
  status      ✓ downloaded (130.1 MB)
  source      https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-en-20M-2023-02-17.tar.bz2
```

- **speed** is a relative tier across the catalog, estimated from architecture
  and parameter count. Where a real benchmark exists it is labelled `measured`
  and carries the real-time factor. Every model in the catalog has now been
  benchmarked for speed, memory and accuracy —
  [`docs/choosing-a-model.md`](docs/choosing-a-model.md) says which to use, and
  [`docs/reports/model-benchmarks.md`](docs/reports/model-benchmarks.md) has the
  numbers. Rerun them on your hardware with `just bench`.
- **vocabulary** is what biasing the model can take from the `[vocabulary]`
  table. Whisper models take it as an initial prompt; transducers (parakeet,
  zipformer) get it as hotwords boosted while decoding, because that is the
  only place sherpa-onnx implements biasing. The CTC, paraformer, moonshine
  and sensevoice models can use none of it, and `mavor doctor` says so rather
  than failing.
- **gpu** depends on the build you are running, not on the model. Run
  `mavor doctor`, which reports what your whisper.cpp and ONNX Runtime can
  actually use rather than what the config asks for.

Whisper models are fetched from the whisper.cpp GGML repository and land in
`paths.models` under the name upstream serves them by — `whisper-base.en`
becomes `ggml-base.en.bin`. Sherpa models come from the sherpa-onnx release
assets and unpack into `paths.models/sherpa/<name>/`.

## Documentation

[`docs/`](docs/README.md) is indexed, and the index says which tree to trust
for what — guides describe use, `reference/` describes the built system,
`reports/` are generated measurements, and `design/` and `planning/` are
proposals rather than descriptions.

- [Quickstart](docs/quickstart.md) · [User Guide](docs/user-guide.md) ·
  [Choosing a Model](docs/choosing-a-model.md)
- [How mavor works](docs/reference/how-mavor-works.md) — the daemon as built
- [Measured model benchmarks](docs/reports/model-benchmarks.md) — regenerate
  with `just bench`
- [Roadmap](docs/roadmap.md) — open decisions and known blockers

## Development

### Dev container

[`yolo-jail.jsonc`](./yolo-jail.jsonc) is a committed [yolo-jail](https://github.com/mschulkind-oss/yolo-jail)
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
| `just test-e2e` | e2e: real whisper transcription with `whisper-tiny.en`        |
| `just storybook`| runs UI storybook test and produces HTML screenshot report    |
| `just install`  | installs to `~/.local/bin/mavor`, libraries to `~/.local/lib` |
| `just deploy`   | installs binary and sets up systemd user service              |
| `just doctor`   | runs environment health check (`mavor doctor`)                |
| `just build`    | compiles `bin/mavor` plus the shared objects it needs         |
| `just dev`      | runs the daemon against your live Wayland session, verbose    |

### Test layout

- **Unit tests** (`go test ./...`): mocked Recorder/Transcriber/
  Overlay/Output. Fast, run under `-race`.
- **Integration tests** (`go test -tags=integration ./test/integration/...`):
  spin up a real headless wlroots sway, optionally waybar, the real daemon
  binary, a host PipeWire null-sink, and assert against grim screenshots
  and `wl-paste` output.
- **End-to-end test** (`go test -tags=e2e ./...`): real whisper-cli plus
  a downloaded model.

The integration test rig lives in
[`test/integration/harness.go`](./test/integration/harness.go). Each
test gets its own `XDG_RUNTIME_DIR`, dbus session, headless sway, and
optionally waybar + null-sink. Cleanup happens in `t.Cleanup`.

### Project layout

```
cmd/mavor/                   # CLI entrypoint & subcommands (daemon, doctor, config, service, models)
internal/state/              # Idle ⇄ Recording ⇄ Transcribing FSM
internal/audio/              # Recorder interface + parec impl + VAD + PipeWire ducking
internal/speech/             # STT runtimes: whisper.cpp (server/cli) and in-process sherpa-onnx
internal/overlay/            # Layer-shell HUD: paint.go renders, overlay_wl.go presents
internal/wayland/            # Minimal hand-written Wayland client (wire protocol, layer-shell, shm)
internal/ipc/                # JSON-over-Unix-socket protocol
internal/output/             # wtype + wl-copy dispatch
internal/config/             # XDG_CONFIG_HOME/mavor/config.toml loader with ~/$VAR expansion
internal/daemon/             # wires everything; main.go is a thin caller
test/integration/            # headless sway + audio-stack test harness
```

### The build is cgo

There is one build and it links the in-process sherpa-onnx recognizers, so a C
compiler is required, `CGO_ENABLED=0` does not work, and cross-compiling needs
a cross toolchain. The two shared objects sherpa-onnx brings with it are
vendored in the Go module cache and copied next to the binary by `just build`;
the binary is linked with an `$ORIGIN` rpath so it finds them beside itself or
in a sibling `lib/`. `bin/` is the artifact, not `bin/mavor`.

The remaining build tags are test-only:

- `integration`: build the headless-sway test harness.
- `e2e`: opt in to tests that exercise real whisper-cli + a downloaded model.

## Built on

`mavor` is a thin daemon around other people's hard work. In rough order of how
much of the heavy lifting they do:

**Speech recognition**

- [whisper.cpp](https://github.com/ggml-org/whisper.cpp) — the `whisper-server`
  binary mavor supervises for every whisper model, the `whisper-cli` it falls
  back to, and the GGML model format the catalog pulls.
- [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) and its Go bindings,
  [sherpa-onnx-go](https://github.com/k2-fsa/sherpa-onnx-go) — the in-process
  runtime behind every non-whisper model, including the streaming transducers
  the live preview uses.
  ([ONNX Runtime](https://github.com/microsoft/onnxruntime) rides along inside
  the platform modules, and is the 26 MB `libonnxruntime.so` that every mavor
  release ships beside the binary.)
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
