# mavor Developer Guide for Agents

`mavor` is a local, low-latency voice-to-text dictation daemon and CLI. Transcription
is entirely local — no cloud API, no account, no telemetry — and that is a
property worth preserving: the only outbound request in the program is
`mavor models pull`.

Its first and current backend is Linux on a wlroots Wayland compositor — sway,
Hyprland, river, Wayfire, niri, labwc — which is what `wlr-layer-shell` (the
overlay) and `virtual-keyboard-v1` (typing) require. Treat that as the platform
that exists rather than the platform the design assumes: `audio.Recorder`,
`speech.Transcriber`, `overlay.Overlay` and `output.Dispatcher` are four
independent interfaces, and only the last two are Wayland-specific. Porting is
a matter of implementing those, not of restructuring.

## Architecture

```
User Keybind ($mod+grave)
          │
          ▼
   `mavor start` / `mavor stop` / `mavor toggle` (CLI)
          │
          ▼ (Unix Domain Socket: $XDG_RUNTIME_DIR/mavor.sock)
   `mavor daemon`
    ├── state.Machine (Idle ⇄ Recording ⇄ Transcribing FSM)
    ├── audio.Recorder (parec / PipeWire audio capture) + audio.VAD
    ├── audio.Ducker (automatic background media ducking via pactl)
    ├── speech.Transcriber (the model that produces the text you get typed)
    │     └── speech.LoadedPreview (a second, streaming model — overlay only)
    ├── overlay.Overlay (wlr-layer-shell HUD, painted in Go, live waveform)
    └── output.Dispatcher (wtype synthetic keyboard injection + wl-copy)
```

Two facts about that tree are easy to get wrong:

- **There is no `engine` key, and no code that reads one.** The model name in
  `config.toml` decides the *runtime* (whisper.cpp, or ONNX Runtime through
  sherpa-onnx) because the catalog records it, and the runtime plus
  `[advanced]` decides the *placement* (`in-process`, `local-server`,
  `subprocess`, `remote`). `models.Select` is where that derivation happens and
  `speech.Resolve` is what the daemon calls.
- **The preview never emits.** A companion model — a small streaming
  recognizer loaded alongside the main one — paints the overlay while you
  speak, and the text you actually get always comes from the single final
  `Transcribe` by the main model. `speech.ResolvePreview` picks between reading
  the main model's own partials, running the companion, and phrase mode.

## Directory Layout

- `cmd/mavor/` — CLI entry point, subcommands (`setup`, `daemon`, `start`, `stop`, `toggle`, `status`, `logs`, `doctor`, `config`, `service`, `models`, `history`, `version`).
- `cmd/mavor-bench/` — the benchmark harness. Reads `models.Catalog` through
  `mavor models list --json`, so a model added to the catalog is benchmarked
  without editing a list here. Writes [`docs/reports/model-benchmarks.md`](./docs/reports/model-benchmarks.md).
- `internal/models/` — the model catalog, plus the runtime and placement each
  model implies (`catalog.go`, `runtime.go`). It lives under `internal/` rather
  than in `cmd/mavor` precisely so `internal/speech` can import it: which
  runtime owns a model is a fact the daemon needs, and a catalog only `package
  main` could see would have forced that fact to be duplicated.
- `internal/state/` — Thread-safe FSM tracking daemon lifecycle state.
- `internal/audio/` — Audio capture via `parec` / PipeWire stream, volume meter, VAD gating, and audio ducking.
- `internal/speech/` — Speech-to-text. `modelpath.go` is the single resolver
  from a catalog name to a file on disk, `factory.go` turns a resolved model
  into a transcriber, `companion.go` decides where the preview text comes from,
  `vocabulary.go` turns the `[vocabulary]` table into a whisper prompt or a
  sherpa hotwords file, and `sherpa*.go` / `server.go` / `supervisor.go` are
  the implementations.
- `internal/overlay/` — Layer-shell HUD: `paint.go` turns state into pixels with no compositor involved, `overlay_wl.go` puts them on screen.
- `internal/wayland/` — Minimal hand-written Wayland client: the wire protocol, wlr-layer-shell, and shared-memory buffers. No cgo in this package (the binary as a whole is cgo — see below).
- `internal/ipc/` — JSON-over-Unix-socket IPC server and client.
- `internal/output/` — Synthetic keystroke typing (`wtype`) and clipboard synchronization (`wl-copy`).
- `internal/history/` — Append-only JSONL log of completed transcripts, for recovery when typing does not land.
- `internal/config/` — Configuration loader (`~/.config/mavor/config.toml`):
  one top-level `model` key plus the `[preview]`, `[ducking]`, `[vocabulary]`,
  `[overlay]`, `[advanced]` and `[paths]` tables, with `~` and `$VAR`
  expansion. `Default()` is the single source of the defaults — `mavor config
  init` scaffolds its file from it, and a test asserts the two agree.
- `internal/daemon/` — Main daemon event loop wiring all subsystems.
- `scripts/` — [`scripts/sherpa-libs.sh`](./scripts/sherpa-libs.sh), the one place that locates the
  sherpa-onnx shared objects the cgo build links against.
- `test/integration/` — Headless Wayland/Sway test harness, screenshot verification, and storybook report.

## Dev Container

[`yolo-jail.jsonc`](./yolo-jail.jsonc) at the repo root defines the container the project is
developed in, and is committed so it is the same for everyone. It carries the
integration harness's dependencies — sway, waybar, grim, PipeWire, `wtype`,
`wl-clipboard`, `whisper-cpp` — which is why `just test-int` and `just storybook`
work in here with no host setup, and the C toolchain and `pkg-config` the cgo
build needs. Edit it via the `configuring-the-jail` skill; changing `packages`
forces an image rebuild and needs a human to restart.

## Benchmarks

[`docs/reports/model-benchmarks.md`](./docs/reports/model-benchmarks.md) is generated — rerun `just bench` rather
than editing it. Every number in it came from a process that ran on the
machine named in its header.

Two rules the harness enforces, and any replacement for it should keep:

- **A backend that did not run is named, with the reason.** Absent rows are
  reported as absent. A gap in a table reads as a model that scored nothing.
- **A GPU column requires proof of a GPU.** The build must actually bring up
  a GPU backend and a device must enumerate, or the column is refused. Four
  earlier reports published CPU numbers under GPU headings; see the roadmap.

## The build is cgo, always

There is one build of mavor and it links the in-process sherpa-onnx
recognizers through cgo. There is no pure-Go variant and no `sherpa` build
tag: both were deleted, because thirteen of the catalog's models are
unreachable without the ONNX runtime. So a C toolchain is a build
requirement, `CGO_ENABLED=0` does not work, and cross-compiling needs a cross
toolchain (which is why releases are `linux/amd64` only for now — see the
`goarch` comment in [`.goreleaser.yaml`](.goreleaser.yaml)).

**The distribution unit is a directory, not a file.** `bin/mavor` is
dynamically linked against `libsherpa-onnx-c-api.so` and `libonnxruntime.so`,
which are vendored in the Go module cache rather than committed here.
[`scripts/sherpa-libs.sh`](./scripts/sherpa-libs.sh) is the one place that finds them; `just build`,
`just install` and the goreleaser `before` hook all call it. The binary is
linked with `-r '$ORIGIN:$ORIGIN/../lib'`, so it finds those libraries beside
itself (the release tarball, `bin/`) or one directory up in `lib/`
(`~/.local/lib`, a Homebrew prefix). Without that rpath the linker bakes in an
absolute path into the *build host's* module cache and the binary runs
nowhere else.

Two build tags remain, and both are test-only:

- `integration`: Enables the headless Sway + PipeWire integration test harness.
- `e2e`: Enables end-to-end transcription tests with real downloaded models.

## Key CLI Commands

- `mavor setup [--force]` — Make the current config fully runnable: scaffold
  `config.toml` if it is missing, install missing runtime tools, and download
  **every model the config names** — the main `model` and the preview
  companion. It is idempotent: a second run downloads nothing and exits zero,
  and it is the right command to re-run after editing `model` or
  `preview.source`. `--force` re-scaffolds the config and re-downloads.
- `mavor daemon [--verbose] [--log-file PATH]` — Run the long-lived dictation service.
- `mavor start` / `mavor stop` — Push-to-talk keybind controls (hold to speak).
- `mavor toggle` — Toggle recording/transcription state.
- `mavor status` — Output current daemon state (`idle`, `recording`, `transcribing`).
- `mavor logs [-f|--follow] [-n <lines>]` — View or stream daemon logs, from journald when available and the daemon log file otherwise.
- `mavor doctor [--fix]` — Run the environment diagnostic (Wayland, audio,
  tools, models); `--fix` runs the setup flow. It is also the second half of
  the config file: it reports the *derived* facts a user cannot read off
  `config.toml` — which runtime and placement the model got, the thread count
  and where it came from, whether a GPU backend actually loaded, where the
  preview text will come from, and whether this model can use the
  `[vocabulary]` table at all. A file written against the pre-rewrite schema
  is reported as entirely stale rather than as a list of unknown keys.
- `mavor config init` — Scaffold default `~/.config/mavor/config.toml`.
- `mavor config show` — Print active resolved configuration.
- `mavor service install [--start]` — Install and enable systemd user service (`mavor.service`).
- `mavor history [-n N] [--json] [--copy]` — List past transcripts (newest first) or recover one to the clipboard.
- `mavor models pull <name>` — Download a Whisper GGML or sherpa-onnx model
  into the cache. `<name>` is a catalog name, and **every catalog name now
  begins with its model family**: `whisper-base.en`, not `base.en`. The old
  bare aliases (`tiny`, `base.en`, `zipformer`, `parakeet-tdt`) were deleted
  rather than kept resolving, so a stale name is an error naming the nearest
  entries. The file on disk keeps upstream's name — `whisper-base.en` is
  `ggml-base.en.bin` — and `speech.WhisperModelPath` is the only place that
  mapping lives.
- `mavor models list [--installed] [--verbose] [--json]` — List the model catalog with size, languages, and streaming support, marking what is downloaded and active. `--installed` narrows it to the cache; `--verbose` prints a block per model adding speed, vocabulary biasing, GPU support, and the source URL; `--json` emits the same catalog machine-readably, which is how the benchmark harness reads it. `--json` and `--verbose` are refused together.

## Committing

- One coherent idea per commit — a feature, a fix, a refactor, a docs change.
  Do not batch unrelated changes, and do not end a task with a dirty tree.
- Conventional commit subjects: `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`,
  `test:`, scoped by area when it helps (`fix(overlay):`, `refactor(speech):`).
- Run `just check` before committing. A `pre-commit` hook runs `just check-ci`
  and will reject the commit if the gate fails — fix the cause rather than
  passing `--no-verify`.
- New or changed behaviour ships with a test in the same commit. For a bug,
  write the failing test first and watch it fail. A pure refactor, rename or
  docs change is the only case where running the existing suite is the whole
  QA step; say so in the commit body when a change genuinely cannot be tested,
  and name what a human should look at instead.
- Work on the current branch. Do not open a PR or create a branch unless asked.
- At the end of a task, `git status` must be clean and `just done` must pass.

## Justfile Recipes

- `just check` — Format, lint, and run unit tests.
- `just check-ci` — Read-only quality gate (format check, lint, unit tests).
- `just test` — Run fast unit tests (`go test ./...`).
- `just test-int` — Run headless Wayland integration tests (`go test -tags=integration ./test/integration/...`).
- `just test-e2e` — Run real whisper transcription test.
- `just storybook` — Generate UI Storybook HTML report with real headless screenshots ([`test/reports/ui-storybook.html`](./test/reports/ui-storybook.html)).
- `just bench` — Benchmark every installed model on every backend, whisper.cpp and the in-process sherpa engines alike: speed, peak memory, accuracy, CPU and GPU, plus thread scaling and warm-server-versus-cold-CLI sweeps. Regenerates [`docs/reports/model-benchmarks.md`](./docs/reports/model-benchmarks.md).
- `just bench-models` — Download the whole catalog so there is something to benchmark (~16 GB).
- `just bench-gpu-build` — Build the Vulkan whisper.cpp the GPU column needs; the packaged whisper-cpp is CPU-only.
- `just build` — Compile `bin/mavor` and copy its two sherpa-onnx shared objects in beside it. `bin/` is the artifact; the binary alone will not start.
- `just install` — Compile, then copy the binary to `~/.local/bin/mavor` and the shared objects to `~/.local/lib`.
- `just deploy` — Install binary and set up systemd user service.
- `just doctor` — Run environment health check (`mavor doctor`).
- `just dev` — Run daemon in debug mode against active Wayland session.
- `just done` — The end-of-session gate: `check-ci`, then a ready-to-commit line.
- `just release <version>` — Verify, tag and push; `release.yml` and goreleaser
  take it from there. Do not also run `gh release create` — both would race to
  create the same release.

> [!NOTE]
> `build-sherpa` and `bench-sherpa` no longer exist. There is one build, it is
> cgo, and `build` and `bench` are what those two recipes became.
