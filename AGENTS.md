# mavor Developer Guide for Agents

`mavor` is a local, low-latency voice-to-text dictation daemon and CLI. Transcription
is entirely local — no cloud API, no account, no telemetry — and that is a
property worth preserving: the only outbound request in the program is
`mavor models pull`.

Its first and current backend is Linux on a wlroots Wayland compositor — sway,
Hyprland, river, Wayfire, niri, labwc — which is what `wlr-layer-shell` (the
overlay) and `virtual-keyboard-v1` (typing) require. Treat that as the platform
that exists rather than the platform the design assumes: `audio.Recorder`,
`speech.Transcriber`, `overlay.Overlay` and `output.Emitter` are four
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
    ├── speech.Transcriber (in-process sherpa-onnx CGO / whisper.cpp / HTTP server)
    ├── overlay.Overlay (wlr-layer-shell HUD, painted in Go, live waveform)
    └── output.Emitter (wtype synthetic keyboard injection + wl-copy)
```

## Directory Layout

- `cmd/mavor/` — CLI entry point, subcommands (`setup`, `daemon`, `start`, `stop`, `toggle`, `status`, `logs`, `doctor`, `config`, `service`, `models`, `history`, `version`).
- `cmd/mavor-bench/` — the benchmark harness. Drives `modelCatalog` through
  `mavor models list --json`, so a model added to the catalog is benchmarked
  without editing a list here. Writes `docs/reports/model-benchmarks.md`.
- `internal/state/` — Thread-safe FSM tracking daemon lifecycle state.
- `internal/audio/` — Audio capture via `parec` / PipeWire stream, volume meter, VAD gating, and audio ducking.
- `internal/speech/` — Pluggable speech-to-text (STT) engines (`whisper-cli`, in-process `sherpa-onnx` CGO, remote HTTP server).
- `internal/overlay/` — Layer-shell HUD: `paint.go` turns state into pixels with no compositor involved, `overlay_wl.go` puts them on screen.
- `internal/wayland/` — Minimal hand-written Wayland client: the wire protocol, wlr-layer-shell, and shared-memory buffers. No cgo.
- `internal/ipc/` — JSON-over-Unix-socket IPC server and client.
- `internal/output/` — Synthetic keystroke typing (`wtype`) and clipboard synchronization (`wl-copy`).
- `internal/history/` — Append-only JSONL log of completed transcripts, for recovery when typing does not land.
- `internal/config/` — Configuration loader (`~/.config/mavor/config.toml`) supporting `~` and `$VAR` expansion.
- `internal/daemon/` — Main daemon event loop wiring all subsystems.
- `test/integration/` — Headless Wayland/Sway test harness, screenshot verification, and storybook report.

## Dev Container

`yolo-jail.jsonc` at the repo root defines the container the project is
developed in, and is committed so it is the same for everyone. It carries the
integration harness's dependencies — sway, waybar, grim, PipeWire, `wtype`,
`wl-clipboard`, `whisper-cpp` — which is why `just test-int` and `just storybook`
work in here with no host setup. Edit it via the `configuring-the-jail` skill;
changing `packages` forces an image rebuild and needs a human to restart.

## Benchmarks

`docs/reports/model-benchmarks.md` is generated — rerun `just bench` rather
than editing it. Every number in it came from a process that ran on the
machine named in its header.

Two rules the harness enforces, and any replacement for it should keep:

- **A backend that did not run is named, with the reason.** Absent rows are
  reported as absent. A gap in a table reads as a model that scored nothing.
- **A GPU column requires proof of a GPU.** The build must actually bring up
  a GPU backend and a device must enumerate, or the column is refused. Four
  earlier reports published CPU numbers under GPU headings; see the roadmap.

## Build Tags

The default build is pure Go and needs no system headers: `CGO_ENABLED=0`
works, and so does cross-compilation.

- `sherpa`: links the in-process sherpa-onnx recognizers. The one variant that
  needs cgo, and so the one that cannot be cross-compiled.
- `integration`: Enables the headless Sway + PipeWire integration test harness.
- `e2e`: Enables end-to-end transcription tests with real downloaded models.

## Key CLI Commands

- `mavor setup [--force]` — One-shot first-run setup: scaffold the config, install missing runtime tools, and download the default model.
- `mavor daemon [--verbose] [--log-file PATH]` — Run the long-lived dictation service.
- `mavor start` / `mavor stop` — Push-to-talk keybind controls (hold to speak).
- `mavor toggle` — Toggle recording/transcription state.
- `mavor status` — Output current daemon state (`idle`, `recording`, `transcribing`).
- `mavor logs [-f|--follow] [-n <lines>]` — View or stream daemon logs, from journald when available and the daemon log file otherwise.
- `mavor doctor [--fix]` — Run environment diagnostic check (Wayland, audio, tools, models); `--fix` runs the setup flow.
- `mavor config init` — Scaffold default `~/.config/mavor/config.toml`.
- `mavor config show` — Print active resolved configuration.
- `mavor service install [--start]` — Install and enable systemd user service (`mavor.service`).
- `mavor history [-n N] [--json] [--copy]` — List past transcripts (newest first) or recover one to the clipboard.
- `mavor models pull <name>` — Download a Whisper GGML or sherpa-onnx model into the cache.
- `mavor models list [--installed] [--verbose] [--json]` — List the model catalog with size, languages, and streaming support, marking what is downloaded and active. `--installed` narrows it to the cache; `--verbose` prints a block per model adding speed, vocabulary biasing, GPU support, and the source URL; `--json` emits the same catalog machine-readably, which is how the benchmark harness reads it.

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
- `just storybook` — Generate UI Storybook HTML report with real headless screenshots (`test/reports/ui-storybook.html`).
- `just bench` — Benchmark every installed model: speed, peak memory, accuracy, CPU and GPU. Regenerates `docs/reports/model-benchmarks.md`.
- `just bench-sherpa` — The same sweep with the in-process sherpa engines linked in (needs cgo).
- `just bench-models` — Download the whole catalog so there is something to benchmark (~16 GB).
- `just bench-gpu-build` — Build the Vulkan whisper.cpp the GPU column needs; the packaged whisper-cpp is CPU-only.
- `just build` — Compile the static, pure-Go binary to `bin/mavor`.
- `just build-sherpa` — Compile with the in-process sherpa-onnx engines (cgo).
- `just install` — Compile and copy binary to `~/.local/bin/mavor`.
- `just deploy` — Install binary and set up systemd user service.
- `just doctor` — Run environment health check (`mavor doctor`).
- `just dev` — Run daemon in debug mode against active Wayland session.
