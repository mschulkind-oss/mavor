# mavor Developer Guide for Agents

`mavor` is a low-latency voice-to-text dictation daemon and CLI.

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
- `mavor models list [--installed] [--verbose]` — List the model catalog with size, languages, and streaming support, marking what is downloaded and active. `--installed` narrows it to the cache; `--verbose` prints a block per model adding speed, vocabulary biasing, GPU support, and the source URL.

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
- `just build` — Compile the static, pure-Go binary to `bin/mavor`.
- `just build-sherpa` — Compile with the in-process sherpa-onnx engines (cgo).
- `just install` — Compile and copy binary to `~/.local/bin/mavor`.
- `just deploy` — Install binary and set up systemd user service.
- `just doctor` — Run environment health check (`mavor doctor`).
- `just dev` — Run daemon in debug mode against active Wayland session.
