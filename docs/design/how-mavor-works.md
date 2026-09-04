---
title: "mavor Architecture: Three-State Machine with Four Subprocesses"
author: "Matthew Schulkind"
date: 2026-08-15
status: accepted
tags: [design, architecture, fsm, ipc, daemon, wayland]
summary: "Comprehensive as-built design document for the mavor daemon describing state machine invariants, subprocess dispatch, and integration seams."
---

# `mavor` is a three-state machine with four subprocesses bolted to its edges

**Status:** DESIGN REFERENCE (as-built), 2026-08-15. Describes the code as of this date; every behavioral claim below was read out of the source, not inferred from the README.

**The short version.** `mavor` is one long-lived daemon whose entire mutable
state is a three-value enum. A hotkey-launched CLI pokes it over a Unix socket;
the enum advances; a listener fires side effects; four external programs
(`parec`, `whisper-cli`, `wtype`, `wl-copy`) do all the actual work behind four
Go interfaces. The interfaces are real seams — but they are *test* seams, not
extension points: nothing in the config file can select an implementation, and
their shapes (`Stop() (path, error)`, `Transcribe(ctx, path) (string, error)`)
foreclose streaming, partial results, and anything that wants to see audio
while it is being captured.

**The most important section is §6** — the four seams and where each one is too
narrow. §7 (failure modes) is where the live bugs are.

**Reads with:** [`../user-guide.md`](../user-guide.md) (what the thing does from
the outside — this doc is the inside), [`../research/`](../research/) (domain
notes on whisper.cpp, Wayland input, and audio capture that back the choices
described here).

---

## 1. Verdict up front

The system is smaller than it looks. 3,177 lines of Go across ten packages
(`find cmd internal test -name '*.go' | xargs wc -l`, 2026-08-15), and the
genuinely load-bearing part is about 300 lines: `internal/state/state.go` (121
lines) and `internal/daemon/daemon.go` (166 lines). Everything else is a
carefully-wrapped subprocess.

That is the right shape for what it does today, and I would not restructure it.
The interesting question is not "is this architecture good" but "which of its
five hardcoded decisions do you want to unpick first" — because they are all
unpicked in different places:

- **The pipeline is batch, not streaming.** Enforced by two type signatures
  (`audio.Recorder.Stop`, `speech.Transcriber.Transcribe`), not by any policy code.
- **The pipeline is one-shot, not cancellable.** Enforced by the FSM having no
  edge for it and the IPC protocol having no verb for it.
- **The stack is hardcoded.** Enforced by ~15 lines of constructor calls in
  `cmd/mavor/main.go:119-133`.
- **The UI can express three things.** Enforced by `overlay.Visual` being a
  closed enum of three constants (`internal/overlay/overlay.go:12-19`).
- **The daemon never speaks to the user about errors.** Enforced by every
  failure path terminating in an `slog` call.

Five load-bearing principles I can read out of the code as written:

- **P1. The FSM is the only shared mutable state.** Every other component is
  either stateless or owns state nobody else touches. There is exactly one
  mutex that matters (`state.Machine.mu`).
- **P2. Side effects hang off state *transitions*, never off IPC requests.**
  `handleRequest` only calls `Apply`; all doing happens in the listener
  (`daemon.go:97-119`). This is why `status` can never have a side effect.
- **P3. Listeners run off the lock** (`state.go:79-83`), which makes the
  listener re-entrant — it is allowed to call `Apply` again, and does.
- **P4. Every external dependency is behind an interface with a mock in the
  same file.** `audio.MockRecorder`, `speech.Mock`, `output.Mock`, `overlay.Noop`
  all ship in the production package, not in `_test.go` files.
- **P5. Degrade, don't die.** No compositor, or one without layer-shell → Noop
  overlay (`main.go:184-187`);
  `wtype` failure → clipboard still gets the text (`output.go:52-65`); output
  error → the cycle still completes (`daemon.go:143-149`).

P5 is the one I would put under the most scrutiny — see §7, where "degrade"
mostly means "silently do nothing and log it where nobody is looking."

---

## 2. The moving parts

```
   Sway config:  bindsym $mod+grave exec mavor toggle
        │
        ▼
  ┌───────────────────┐   config.Load()   ┌──────────────────────┐
  │ mavor toggle      │──────────────────▶│ ~/.config/mavor/     │
  │ cmd/mavor/main.go │                   │        config.toml   │
  │ :151-165          │                   └──────────────────────┘
  └─────────┬─────────┘
            │  ipc.Send, 2s budget, one connection, one JSON object
            │  {"action":"toggle"}  ──▶  {"state":"recording"}
════════════╪════════════════ process boundary ═══════════════════════
            │  $XDG_RUNTIME_DIR/mavor.sock
            ▼
┌──────────────────────────────────────────────────────────────────────┐
│  mavor daemon                 (cmd/mavor/main.go:69-149 builds this)  │
│                                                                       │
│   ┌──────────────┐   Request     ┌───────────────────────┐            │
│   │ ipc.Server   │──────────────▶│ daemon.handleRequest  │            │
│   │ ipc.go:41-83 │◀──Response────│ daemon.go:80-93       │            │
│   └──────────────┘               └───────────┬───────────┘            │
│    1 goroutine per conn                      │ Apply(EventToggle)     │
│    5s conn deadline                          ▼                        │
│                                  ┌───────────────────────┐            │
│                                  │   state.Machine       │  ◀── P1    │
│                                  │   state.go:45-101     │            │
│                                  │   Idle|Rec|Transcribe │            │
│                                  └───────────┬───────────┘            │
│                                              │ listeners, OFF the lock│
│                                              ▼                        │
│                                  ┌───────────────────────┐            │
│                                  │  daemon.onTransition  │  ◀── P2    │
│                                  │  daemon.go:97-119     │            │
│                                  └──┬─────────┬────────┬─┘            │
│                    always           │         │ Rec    │ Transcribing │
│              ┌──────────────────────┘         │        │ (spawns      │
│              ▼                                ▼        │  goroutine)  │
│   ┌────────────────────┐        ┌────────────────────┐ │              │
│   │ overlay.Overlay    │        │ audio.Recorder     │ │              │
│   │ Show(Visual)       │        │ Start / Stop       │ │              │
│   └─────────┬──────────┘        └─────────┬──────────┘ │              │
│             │                             │            ▼              │
│             │                             │  ┌──────────────────────┐ │
│             │                             │  │ daemon.runTranscrip- │ │
│             │                             │  │ tion  :121-152       │ │
│             │                             │  └────┬──────────┬──────┘ │
│             │                             │       │          │        │
│             │                             │       ▼          ▼        │
│             │                    ┌────────────────────┐ ┌────────────┐│
│             │                    │ speech.Transcriber │ │ output.    ││
│             │                    │ Transcribe(ctx,wav)│ │ Dispatcher ││
│             │                    └─────────┬──────────┘ └─────┬──────┘│
└─────────────┼──────────────────────────────┼──────────────────┼───────┘
              ▼                              ▼                  ▼
     wlr-layer-shell surface       whisper-cli (exec)     wtype (exec)
     painted in Go on its own       reads  rec-N.wav       wl-copy (exec)
     goroutine                      writes rec-N.wav.txt   BOTH, always
     (overlay_wl.go)                        ▲              (output.go:59-64)
                                            │
     parec (exec) ──────────────────────────┘  ← the WAV file on disk is the
     launched by audio.Recorder above          ONLY channel between capture
     writes $TMPDIR/mavor-recordings/         and ASR. Nothing else can see
     rec-<unixnano>.wav, 16 kHz mono s16le    the audio. (§6.1)
     (audio.go:36-45, :80)
```

| Package | Lines | Owns | Depends on |
|---|---|---|---|
| `internal/state` | 121 | the FSM, listener registry | stdlib only, deliberately |
| `internal/daemon` | 166 | wiring, side-effect dispatch, the transcription goroutine | all five interfaces |
| `internal/audio` | 199 | `parec` process lifecycle, WAV paths | `os/exec`, `syscall` |
| `internal/speech` | 117 | `whisper-cli` process, sidecar read | `os/exec` |
| `internal/output` | 98 | `wtype` + `wl-copy` | `os/exec` |
| `internal/overlay` | 66 + 330 (paint) + 250 (wl) | the status pill | `internal/wayland`, `golang.org/x/image` |
| `internal/ipc` | 125 | socket protocol, stale-socket handling | `net`, `encoding/json` |
| `internal/config` | 102 | four fields, XDG resolution | go-toml |
| `cmd/mavor` | 233 | subcommand dispatch, model download, **the wiring decisions** | everything |
| `test/integration` | 780 | headless Sway + PipeWire harness | `integration` build tag |

(Counts are non-test source, except `test/integration`, which is all test code.
The remaining ~930 lines of the 3,177 total are `_test.go` files in the
`internal/` packages.)

Note the shape of the dependency graph: it is a star, not a chain.
`internal/daemon` imports all five leaf packages; no leaf package imports
another. `internal/state` imports nothing but `sync`. That is what makes the
FSM independently testable, and it is worth preserving.

---

## 3. One dictation cycle, end to end

What follows is the full path from keypress to typed text. Every step names the
function and the file:line where it lives.

**Step 0 — the daemon is already up.** `runDaemon` (`cmd/mavor/main.go:69`) has:
loaded config (`main.go:84`), built a `slog` logger at Info or Debug
(`main.go:88-105`), **stat'ed the model file** (`main.go:107-110`) and bailed
with `model %q not found at %s — run mavor models pull %s` if absent, chosen
`$TMPDIR/mavor-recordings` as the recording directory (`main.go:112`),
constructed the overlay with a fallback to `Noop` on error (`main.go:113-117`),
constructed the other three implementations (`main.go:119-124`), handed all
four to `daemon.New` (`main.go:126-133`), and installed a SIGINT/SIGTERM
context (`main.go:135`). `d.Run(ctx)` then blocks.

Inside `Run` (`daemon.go:61`): a `sync.WaitGroup` is created, the side-effect
listener is subscribed **before** the IPC server starts (`daemon.go:64-68` —
the comment says why: "so the first toggle can't race the listener
registration"), and `srv.Serve(ctx)` takes over (`daemon.go:71`).

`ipc.Serve` (`ipc.go:41`) calls `prepareSocket` (`ipc.go:88`), which is the
one-daemon guard — see §7. It binds, spawns a goroutine that closes the
listener when `ctx` is done (`ipc.go:52-55`), defers `os.Remove` of the socket
file (`ipc.go:56`), and loops on `Accept`, running each connection in
`wg.Go` (`ipc.go:59-69`).

**Step 1 — the first keypress.** Sway execs `mavor toggle`. `runToggle`
(`main.go:151`) loads config again (the CLI shares no state with the daemon
other than the socket path) and calls `ipc.Send(cfg.Socket, {Action:"toggle"},
2*time.Second)` (`main.go:156`). `Send` (`ipc.go:104`) dials, sets one deadline
for the whole round trip, writes one JSON object, reads one, closes.

**Step 2 — the daemon advances the FSM.** `Server.handle` (`ipc.go:72`) sets a
5-second connection deadline, decodes, and calls the handler.
`daemon.handleRequest` (`daemon.go:80`) logs, and for `"toggle"` calls
`d.machine.Apply(state.EventToggle)` (`daemon.go:84`).

`Machine.Apply` (`state.go:68`) takes the lock, computes `transition(Idle,
EventToggle) = Recording` (`state.go:105-108`), stores it, **snapshots the
listener slice while still holding the lock**, releases the lock, then invokes
each listener (`state.go:73-83`). This ordering is the whole trick: the FSM is
never locked while a side effect runs.

**Step 3 — the side effects for `Recording`.** `onTransition` (`daemon.go:97`)
runs, on the IPC connection's goroutine:

1. `d.overlay.Show(overlay.Recording)` (`daemon.go:99`, mapping at
   `daemon.go:154-162`). The layer-shell overlay posts this to its render
   goroutine and returns without waiting.
2. `d.recorder.Start(ctx)` (`daemon.go:107`), synchronously.

`ParecRecorder.Start` (`audio.go:71`) refuses a double start (`audio.go:74-76`),
`MkdirAll`s the recording dir, mints a filename
`rec-<time.Now().UnixNano()>.wav` (`audio.go:80`), builds the command
(`DefaultCommand`, `audio.go:36-45`: `parec --format=s16le --rate=16000
--channels=1 --file-format=wav <out>`), starts it, and spawns a goroutine that
SIGINTs the process when `ctx` is done (`audio.go:98-103`).

Only after all that does `Apply` return, `handleRequest` return, and the JSON
response `{"state":"recording"}` get written. **The user's `mavor toggle` blocks
on the `parec` fork/exec.** Its 2-second budget is the only
thing bounding that.

`mavor toggle` prints `recording` (`main.go:163`) and exits.

**Step 4 — the user talks.** Nothing in the Go process is doing anything.
`parec` is writing WAV bytes. The daemon has no timer, no level meter, no
knowledge of how long this has been going on. See §9.

**Step 5 — the second keypress.** Same path to `Apply`, `transition(Recording,
EventToggle) = Transcribing` (`state.go:109-112`). `onTransition` shows
`overlay.Transcribing` and then, crucially, **spawns a goroutine** rather than
working inline (`daemon.go:111-118`):

```
wg.Add(1)
go func() { defer wg.Done(); d.runTranscription(ctx) }()
```

So the second toggle returns `{"state":"transcribing"}` immediately.

**Step 6 — the transcription pipeline.** `runTranscription` (`daemon.go:121`),
on its own goroutine:

1. `d.recorder.Stop()` (`daemon.go:123`). `ParecRecorder.Stop` (`audio.go:107`)
   swaps the in-flight state out under the mutex, then **sends SIGINT, not
   SIGKILL** (`audio.go:119`) — the comment at `audio.go:117-118` states the
   reason: "SIGINT lets parec flush its WAV header. SIGKILL would leave a
   truncated file that whisper can't read." Then `cmd.Wait()`, then a stat. A
   non-zero exit is *tolerated* (`audio.go:141-143` only errors on a non-exit
   wait error); a **file size of ≤ 0 is not** (`audio.go:144-146`).
2. `d.transcriber.Transcribe(ctx, wav)` (`daemon.go:130`). `WhisperCli.Transcribe`
   (`speech.go:73`) runs `whisper-cli -m <model> -f <wav> -otxt -nt -np`
   (`speech.go:43-56`) via `CombinedOutput`, then **reads `wavPath + ".txt"`**
   (`speech.go:111-117`) and `TrimSpace`s it. That sidecar filename is the entire
   contract between the two programs — see §6.2.
3. Empty check (`daemon.go:137-141`): an empty transcript short-circuits to
   `EventTranscribeDone` with no emission.
4. `d.output.Emit(ctx, text)` (`daemon.go:143`). `Wayland.Emit`
   (`output.go:52`) runs `wtype -- <text>` and then `wl-copy` with the text on
   stdin, **unconditionally both**, and returns `errors.Join(typeErr, copyErr)`
   (`output.go:59-64`). The daemon logs a non-nil result as a Warn and carries
   on (`daemon.go:143-149`).
5. `d.machine.Apply(state.EventTranscribeDone)` (`daemon.go:150`) →
   `transition(Transcribing, Done) = Idle` (`state.go:113-115`) → listener →
   `overlay.Show(Hidden)` → the pill disappears.

**Step 7 — the residue.** `rec-<ns>.wav` and `rec-<ns>.wav.txt` remain on disk.
Forever. See §7.9.

---

## 4. The state machine

```
              EventToggle                     EventToggle
            ┌──────────────┐                ┌──────────────┐
   ────────▶│     Idle     │───────────────▶│  Recording   │────────┐
            └──────────────┘                └──────────────┘        │
                   ▲                                                ▼
                   │                                       ┌────────────────┐
                   │                                       │  Transcribing  │
                   │                                       └────────┬───────┘
                   │   EventTranscribeDone / EventTranscribeFailed  │
                   └────────────────────────────────────────────────┘

   events with NO edge — silently absorbed, changed=false, no listener fires:

     Idle          ✗ Done, ✗ Failed      (harmless: nothing sends them here)
     Recording     ✗ Done, ✗ Failed   ◀── the §7.2 bug lives in this cell:
                                          daemon.go:109 sends Failed here
     Transcribing  ✗ Toggle           ◀── deliberate no-op, state.go:117-118
```

`transition` is a 19-line pure function (`state.go:103-121`) and the full truth
table is:

| From \ Event | `EventToggle` | `EventTranscribeDone` | `EventTranscribeFailed` |
|---|---|---|---|
| `Idle` | → `Recording` | — | — |
| `Recording` | → `Transcribing` | — | **— (see §7.2)** |
| `Transcribing` | **— (deliberate)** | → `Idle` | → `Idle` |

Three things about this table are load-bearing:

**The deliberate no-op.** Toggling while transcribing does nothing
(`state.go:117-118`, with the provenance recorded in the comment: "per handover
spec: 'for v1, just ignore toggles while transcribing'"). `Apply` returns
`changed=false`, no listener fires, and `handleRequest` replies
`{"state":"transcribing"}` (`daemon.go:84-86`). The user's second impatient tap
is swallowed silently — not queued, not treated as a cancel. This is the single
most consequential product decision in the codebase, because it means **there
is no way to abort a transcription** short of killing the daemon.

**The dashes are not errors.** An event with no edge is silently absorbed;
`Apply` returns `(currentState, false)`. There is no "invalid transition"
signal anywhere.

**`Recording` has no failure edge.** That is not a design choice, it is a gap
— `daemon.go:107-110` sends `EventTranscribeFailed` from `Recording` expecting
it to unwind, and the table above eats it. §7.2.

The listener mechanism (`state.go:90-101`) is a `map[uint64]func(State)` with
monotonic IDs and a returned unsubscribe closure. Listeners fire **only on
change** (`state.go:74-78`) and **only off the lock** (`state.go:81-83`). The
daemon registers exactly one listener (`daemon.go:65-67`); nothing else
subscribes.

---

## 5. Invariants and one-writer rules

**I1. `state.Machine` is the sole owner of the state word.** No component holds
a copy. `daemon.Daemon` has no state fields beyond its five injected
collaborators (`daemon.go:25-33`). The overlay keeps a `Scene` describing what it is
drawing, but only writes it, never reads it back for decisions.

**I2. Three call sites may call `Apply`, and they are all in `internal/daemon`.**
`handleRequest` on a toggle (`daemon.go:84`); `onTransition` on a recorder-start
failure (`daemon.go:109`); `runTranscription` on each of its three exits
(`daemon.go:126`, `:139`, `:150`). `Apply` is exported and any package could
call it — nothing enforces this — but today nothing else does.

**I3. Listeners run off the lock so the listener may re-enter `Apply`.** This
is not incidental: `onTransition` *does* call `Apply` from inside a listener
invocation (`daemon.go:109`). Holding the lock across listeners would deadlock
instantly. The comment at `state.go:88-89` and `daemon.go:95-96` both say so.

**I4. At most one transcription goroutine exists at a time**, and this is
enforced *only* by the FSM shape: the sole `wg.Add(1)` (`daemon.go:114`) is on
the `Recording → Transcribing` edge, and you cannot re-enter `Recording` until
that goroutine has driven the machine back to `Idle`. There is no explicit
guard. If a future event ever produced a second `→ Transcribing` transition,
you would get two goroutines both calling `recorder.Stop()` and the second
would get `"audio: not started"` (`audio.go:112-114`).

**I5. Shutdown ordering is `Serve → wg.Wait → overlay.Close`** (`daemon.go:71-77`).
Each step is necessary:
- `ipc.Serve` does its own `wg.Wait()` on connection handlers before returning
  (`ipc.go:62`), so no new `Apply` can start after it returns.
- `daemon`'s `wg.Wait()` (`daemon.go:75`) then drains any in-flight
  transcription — the comment says "so we don't leave a goroutine writing to
  wl-copy after the binary exits."
- Only then `overlay.Close()` (`daemon.go:76`), which matters because the
  transcription goroutine's final `Apply` triggers `overlay.Show(Hidden)`.
  Closing first would make that call error, since a closed overlay refuses
  posts.

**I6. `ParecRecorder` guards its own single-capture invariant** with a mutex and
a nil check (`audio.go:74-76`), independently of the FSM. Two overlapping
guards for the same property; the recorder's is the backstop.

**I7. One daemon per socket path.** `prepareSocket` (`ipc.go:88-100`) dials the
existing socket file with a 100 ms timeout: a successful dial means a live
daemon and returns an error; a failed dial means a stale file and unlinks it.

**I8. The `mavor toggle` CLI is stateless.** It re-reads config on every
invocation (`main.go:152`) and holds nothing between runs. Two rapid keypresses
are two independent processes, and their ordering at the daemon is whatever the
socket accept order happens to be — see R2.

---

## 6. The four seams — and where each is too narrow

All four interfaces are ~2 methods, defined in the package that also holds the
production implementation *and* the mock. That co-location is why the daemon's
unit tests run in milliseconds with no Wayland, no audio, and no cgo.

### 6.1 `audio.Recorder` — `audio.go:20-27`

```go
Start(ctx context.Context) error
Stop() (wavPath string, err error)
```

*Abstracts:* the existence and lifecycle of a capture subprocess, and the fact
that captured audio is a file.

*Second implementation:* easy in the shallow sense. A direct-PipeWire recorder,
a `ffmpeg`/`arecord` recorder, or a replay-from-fixture recorder are each ~60
lines. `CommandFunc` (`audio.go:31`) already provides a sub-seam for "same
lifecycle, different argv", which is what the unit tests use
(`audio_test.go:49-55`).

*Too narrow, three ways:*

1. **Nothing can observe audio during capture.** The only output is a path,
   produced at the end. This forecloses, in one signature: a live level meter,
   VAD/auto-endpointing, streaming to a chunked transcriber, and an
   "is the mic actually working" indicator. Any of those requires the interface
   to grow a channel or a callback.
2. **There is no discard path.** `Stop` is the only exit and always means
   "finish and hand me the file". There is no `Abort()`, so a cancel feature
   cannot be built without either widening this or leaning on `ctx` (which today
   means "the whole daemon is shutting down").
3. **The format contract is invisible.** 16 kHz mono s16le is chosen in
   `DefaultCommand` (`audio.go:36-45`) because whisper wants it — the comment
   says so — but the `Transcriber` never states that requirement. A second
   `Recorder` that produced 48 kHz stereo would type-check and fail at runtime.
   The input device is likewise not a parameter: `parec` picks it up from
   `PULSE_SOURCE` in the ambient environment, which the daemon only *logs*
   (`main.go:143`, `audio.go:86`) and never sets.

### 6.2 `speech.Transcriber` — `speech.go:20-25`

```go
Transcribe(ctx context.Context, wavPath string) (string, error)
```

*Abstracts:* one batch ASR run.

*Second implementation:* a `whisper-server` HTTP client, a cloud API, or
`faster-whisper` are all writable against this — with one constraint: **the
argument is a local filesystem path**, so a network backend has to read and
upload the file itself.

*Too narrow, four ways:*

1. **One string, at the end.** No partial results, no streaming. This is the
   single most consequential signature in the codebase; it is the reason the
   overlay can only say "Transcribing…" and not show text appearing.
2. **No segments, no timestamps, no confidence.** `-nt` (`speech.go:47`) actively
   strips timestamps at the source.
3. **No per-call parameters.** Model, GPU layers, and thread count are fixed at
   construction (`speech.go:58-71`); language, initial prompt, temperature, and
   translate-vs-transcribe are all unreachable. Adding any one of them means
   changing the interface or smuggling it through the constructor.
4. **The `.txt` sidecar contract is load-bearing and untested against the real
   binary.** `WhisperCli` runs the command and then reads `wavPath + ".txt"`
   (`speech.go:111-116`). Whether the real `whisper-cli -otxt` writes exactly there
   is asserted only by fakes written to match the assumption — the unit test's
   `sh` stub (`speech_test.go:39-43`) and the integration harness's shim
   (`harness.go:266-287`) both write `"${wav}.txt"` because the code expects it.
   No test in the repo exercises the real binary (§8). The `CommandFunc` seam
   (`speech.go:30`) lets you change the argv but *not* where the output is looked
   for — so a backend using `-of` or writing to stdout cannot use `WhisperCli`
   at all; it must be a whole new `Transcriber`.

### 6.3 `output.Dispatcher` — `output.go:18-20`

```go
Emit(ctx context.Context, text string) error
```

*Abstracts:* where transcribed text goes.

*Second implementation:* the easiest of the four. `ydotool` for X11, stdout, a
file, an HTTP hook — all trivial. `Runner` (`output.go:25`) is a second, finer
seam for "same policy, different process launcher", which is what the unit
tests use (`output_test.go:16-23`).

*Too narrow, two ways:*

1. **The dual-emission *policy* lives inside the implementation**
   (`output.go:59-64`), not in a composable place. "Clipboard only", "type
   only", or "type, and only fall back to clipboard on failure" are three
   different `Dispatcher`s today, not three configurations. A `MultiDispatcher`
   wrapping single-purpose dispatchers would be the natural refactor and does
   not exist.
2. **`error` is the only feedback**, and the daemon throws it away
   (`daemon.go:143-149`). `Emit` cannot report *partial* success in a way the
   caller can act on — the joined error (`output.go:64`) is inspectable with
   `errors.Is`, but nothing inspects it.

### 6.4 `overlay.Overlay` — `overlay.go:33-38`

```go
Show(v Visual) error
Close() error
```

with `Visual ∈ {Hidden, Recording, Transcribing}` (`overlay.go:12-19`).

*Abstracts:* the user-visible status indicator. This is the seam with the most
machinery behind it, split in two so that most of it needs no compositor:
`paint.go` is a pure function from a `Scene` to an image, and `overlay_wl.go`
owns a `wlr-layer-shell` surface and puts that image on screen through
`internal/wayland`. The surface is anchored to the top edge with **no exclusive
zone**, so it floats over content instead of pushing a bar around, and requests
no keyboard interactivity, so it cannot take focus from whatever is being
dictated into.

*Second implementation:* `Noop` already is one (`overlay.go:45-66`). A
`notify-send` overlay or a Waybar-module writer would be a few dozen lines.

*Too narrow, three ways:*

1. **`Visual` is a closed enum that carries no data.** It cannot express
   elapsed recording time, audio level, an error, a partial transcript, or
   "which model is loading". Every richer UI idea dies at this type.
2. **`Show` is asynchronous, so failure is invisible to the caller.** It posts
   to the render goroutine and returns, which means a wedged compositor can
   never wedge the toggle path — but it also means an overlay that has stopped
   painting reports success on every call. The error surfaces only at `Close`.
3. **There is no "the daemon has a problem" visual.** `Hidden` is the only
   terminal state, so failure and success look identical from the user's chair.

### 6.5 The seam that is missing: choosing an implementation

There is no runtime selection anywhere. `cmd/mavor/main.go:119-133` names all four
concrete types by hand:

```go
recorder := audio.NewParecRecorder(recDir)
transcriber := speech.NewWhisperCli(modelPath)
outDispatch := output.NewWayland()
ov, err := overlay.NewDefault(cfg.TopMargin)
```

`config.Config` has exactly four fields (`config.go:17-31`) — `top_margin`,
`model`, `model_dir`, `socket` — and none of them names a backend. The overlay
no longer has a compile-time switch either: there is one implementation, and
the fallback to `Noop` is a runtime decision taken when the compositor turns
out not to support layer-shell (`main.go:184-187`).

So the honest summary of §6 is: **these are excellent test seams and not yet
extension points.** Making any of them user-selectable requires a factory keyed
on config, and that is a design decision, not a refactor — see OQ-5.

---

## 7. Failure modes, honestly

Every row below was traced through the code, not assumed.

**7.1 Empty transcript.** `daemon.go:137-141`: logged at Warn ("empty
transcript — skipping emit (whisper found no speech?)"), `Emit` is never
called, `EventTranscribeDone` → `Idle`. The pill vanishes, nothing is typed, the
clipboard is untouched (so it still holds whatever it held before), and the user
gets no signal distinguishing this from success. Covered by
`TestEmptyTranscriptionSkipsOutput` (`daemon_test.go:166-182`).

**7.2 `parec` fails to start — the daemon gets stuck showing RECORDING.** This
is the sharpest live bug. `recorder.Start` returns an error (`audio.go:89-92`,
e.g. binary not on PATH), `onTransition` logs "recorder start failed —
returning to idle" and applies `EventTranscribeFailed` (`daemon.go:107-110`) —
**but the machine is in `Recording`, and `transition` has no edge for that
event from `Recording`** (`state.go:109-112`). The event is swallowed. The FSM
stays in `Recording`, the overlay keeps saying RECORDING, and the log line
claims a return to idle that did not happen. The integration suite already
knows: `overlay_test.go:38-39` comments "if parec fails the FSM stays in
Recording (failure events are ignored from that state)." It self-heals on the
*next* toggle: `Recording → Transcribing → recorder.Stop()` returns "audio: not
started" (`audio.go:112-114`) → `EventTranscribeFailed` → `Idle`. Two keypresses
to recover, with a lying overlay in between.

**7.3 `parec` starts but captures nothing** (bad `PULSE_SOURCE`, no such
source, no PipeWire). `Start` succeeds — fork/exec is all it checks. `parec`
exits non-zero on its own; `Stop` tolerates the non-zero exit
(`audio.go:141-143`) and falls to the size check. If the file is absent or zero
bytes, `size` is `-1` or `0` and `Stop` errors with the stderr text embedded
(`audio.go:144-146`) → `EventTranscribeFailed` from `Transcribing` → `Idle`,
cleanly. **But the check is `size <= 0`** — a WAV that got a 44-byte header
written and no samples passes it, and whisper receives a silent clip, which
lands in 7.1. Covered (for the zero-byte case) by
`TestParecRecorderEmptyWAVIsAnError` (`audio_test.go:98-108`).

**7.4 `whisper-cli` errors.** `speech.go:108-110` wraps the exit error with the
command's combined output, `runTranscription` logs it at Error and applies
`EventTranscribeFailed` (`daemon.go:131-134`) → `Idle`. Note the precondition
check in `main.go:107-110` is **existence-only** — it stats the path and never
validates the file. A truncated or corrupt model passes startup and fails here,
once per dictation. (The integration harness relies on exactly this: it writes
a 4-byte file containing `"stub"` to satisfy the check, `harness.go:358-362`.)

**7.5 Sidecar missing.** `whisper-cli` exits 0 but no `.txt` appears →
`speech.go:111-116` errors → same path as 7.4. Covered by
`TestWhisperCliReportsMissingSidecar` (`speech_test.go:69-81`).

**7.6 `wtype` fails** (no focused window, not installed, compositor refuses the
virtual-keyboard protocol). `Emit` still runs `wl-copy` (`output.go:62`) and
returns the joined error (`output.go:64`); `runTranscription` logs it at **Warn
and continues** (`daemon.go:143-149`, with the rationale in the comment: "the
user already heard themselves and clipboard fallback may have worked"). The FSM
completes normally. The user's text is on the clipboard and they are not told.
If *both* fail, the behavior is identical — the transcript is gone, the log has
it, the user sees a pill disappear. Covered by
`TestEmitRunsBothEvenIfTypingFails` and `TestEmitJoinsErrors`
(`output_test.go:40-71`).

**7.7 A second daemon starts.** `prepareSocket` (`ipc.go:88-100`) stats the
socket path; if the file exists it dials with a 100 ms timeout. Live listener →
returns `ipc: socket %s is already in use by another daemon`, which propagates
out of `Serve` → `Run` → `runDaemon` → `exit()` prints it to stderr and exits 1
(`main.go:61-67`). Stale file (crashed daemon that never unlinked) → `os.Remove`
and bind normally. Both branches are tested
(`ipc_test.go:111-149`). *Wart:* `ipc.ErrAddrInUse` is declared and documented
as the return value (`ipc.go:123-125`) but **nothing ever returns it** — the
error is a bare `fmt.Errorf`, so `errors.Is` against it always fails. Same for
`daemon.ErrNotRunning` (`daemon.go:164-166`): declared, documented as what
`mavor toggle` returns, never used. `runToggle` returns a wrapped dial error with
the hint text appended inline instead (`main.go:158`).

**7.8 Toggling while transcribing.** No-op, §4. `mavor toggle` prints
`transcribing` and exits 0. Nothing queues, nothing cancels.

**7.9 WAV accumulation — verified, nothing cleans them up.** I checked this
specifically. The recording directory is `$TMPDIR/mavor-recordings`
(`main.go:112`); each capture mints `rec-<unixnano>.wav` (`audio.go:80`); and
`rg -n 'os\.Remove|RemoveAll' cmd internal` returns exactly four hits, none of
them touching that directory: the model-download temp file (`main.go:225,229`)
and the IPC socket (`ipc.go:56,99`). There is no retention policy, no max-age,
no max-count, no cleanup at startup or shutdown. **Furthermore**, whisper writes
`rec-<unixnano>.wav.txt` next to each WAV (`speech.go:111`) and nothing removes
those either. So every completed dictation leaves *two* files behind
permanently: the audio and the transcript. On a system where `TMPDIR` is a
reboot-cleared tmpfs this is a slow leak; where it is persistent, it is an
unbounded and rather personal archive of everything the user has ever dictated.
Nobody decided this — it is the absence of a decision. See OQ-3.

**7.10 Shutdown while recording.** SIGINT/SIGTERM cancels the root context
(`main.go:135`). The per-capture goroutine (`audio.go:98-103`) SIGINTs `parec`,
so the WAV is flushed and left on disk — but `Stop()` is never called, so it is
never transcribed and never deleted. `Serve` returns nil, `wg.Wait()` has
nothing to wait for, `overlay.Close()` runs. Clean exit, orphaned recording.

**7.11 Shutdown while transcribing.** The same root context is passed into
`Transcribe` (`daemon.go:130` → `exec.CommandContext`, `speech.go:55`), so
`whisper-cli` is killed mid-run, `Transcribe` errors, `EventTranscribeFailed`
fires, `wg.Wait()` releases, the daemon exits. **The transcript is lost**, and
so is the user's speech in any usable form. Narrower window, same root cause: if
cancellation lands between `Transcribe` returning and `Emit` running, both
`wtype` and `wl-copy` get a dead context (`output.go:29`) and the text is logged
but never delivered. There is no "flush on shutdown" path.

**7.12 The per-capture context goroutine leaks.** `Start` spawns a goroutine
that blocks on `<-ctx.Done()` (`audio.go:98-103`) and never exits early — `Stop`
does not cancel it. One goroutine per recording accumulates for the daemon's
whole life. At shutdown they all wake and call `r.signal`, which re-reads the
*current* `r.cmd` (`audio.go:157-164`), so they either SIGINT the live capture
(correct) or find `nil` (harmless). It is a leak of a few hundred bytes per
dictation, not a correctness bug — but it is the kind of thing that makes a
future "10-minute recording" feature surprising.

---

## 8. Build tags, tests, and the `justfile`

Three real build tags plus one that does nothing:

| Tag | Files | Effect |
|---|---|---|
| *(none)* | — | the default build is pure Go: the overlay, the Wayland client and the whisper-cli engine all compile without cgo |
| `sherpa` | `sherpa_cgo.go:1`, `build_tags_sherpa.go:1` | links in-process sherpa-onnx recognizers; the only variant needing cgo, and so the only one that cannot be cross-compiled |
| `integration` | all five files in `test/integration/` | headless-Sway harness |
| `e2e` | **none** | see below |

**The `e2e` tag is documented but unimplemented.** `just test-e2e`
(`justfile:44-45`) downloads `tiny.en` and runs `go test -tags=e2e ./...`, and
the README describes it as "real whisper-cli plus a downloaded model"
(`README.md:82-84`). But `rg '^//go:build' cmd internal test` finds no file
carrying the `e2e` tag. The recipe therefore just re-runs the ordinary unit
suite after a model download. **Nothing in this repo has ever exercised the real
`whisper-cli`** — which is what makes the sidecar contract (§6.2.4) an
unverified assumption rather than a tested one.

The test pyramid that *does* exist:

- **Unit** (`go test ./...`, `justfile:35-36`): every package. Mocks everywhere;
  no Wayland, no audio, no cgo needed. `daemon_test.go` drives the whole
  pipeline through `MockRecorder` + `speech.Mock` + `output.Mock` + `overlay.Noop`
  (`daemon_test.go:21-36`) and asserts the exact overlay call sequence
  `[Recording, Transcribing, Hidden]` (`daemon_test.go:141`).
- **Integration** (`-tags=integration`, `justfile:40-41`): `harness.go` spawns a
  private `dbus-daemon` (`:90-122`), a headless wlroots Sway with a pixman
  renderer (`:124-145`), optionally Waybar (`:182-213`), optionally a PipeWire
  `module-null-sink` on the *host* daemon (`:238-260`), and a whisper shim on a
  prepended PATH (`:266-287`). It builds the real binary once in `TestMain`
  (`main_test.go:17-37`) and runs it as a child with a synthesized config file
  (`:364-372`). Assertions are pixel bands from `grim` screenshots
  (`overlay_test.go:103-136`) and real clipboard reads via `wl-paste`
  (`harness.go:335-343`). This is a genuinely impressive rig — the
  overlay-doesn't-overlap-Waybar test (`overlay_test.go:26-74`) is checking a
  property that no unit test could.
- The `just check` gate (`justfile:24`) is `fmt + vet + test + test-int`.

---

## 9. What is NOT here

Everything in this section I verified by search before asserting it. The point
is not to complain — it is to make the empty space visible, because that is
where the shaping happens.

- **No streaming or partial results.** Foreclosed by `Transcriber` returning one
  `string` (§6.2) and `Recorder` returning one path (§6.1).
- **No VAD, silence detection, or auto-endpointing.** No timer, no threshold, no
  level analysis anywhere; `rg -ni 'vad|silence|endpoint'` over `cmd internal`
  hits only doc comments about `-np` "silencing progress". Recording ends when
  and only when a human presses the key again.
- **No push-to-talk.** The only IPC verb that changes anything is `"toggle"`
  (`daemon.go:82-92`). There is no `start`/`stop` pair, so a hold-to-talk
  binding is not expressible without a protocol change.
- **No cancel or abort.** Same reason, plus §4's deliberate no-op.
- **No history, no persistence, no undo.** Nothing writes transcripts anywhere
  by design — the `.txt` sidecars (§7.9) are whisper's litter, not a feature,
  and nothing indexes or reads them back. There is no "type that again".
- **No backend or device selection.** Four config fields (`config.go:17-31`),
  none of which chooses an implementation (§6.5) or an audio source. Input
  device is whatever `PULSE_SOURCE` says, which the daemon never sets.
- **No text post-processing.** The transcript goes from `strings.TrimSpace`
  (`speech.go:117`) directly into `Emit` (`daemon.go:143`). No punctuation
  restoration, no capitalization, no custom vocabulary, no find-and-replace, no
  filler-word stripping, no trailing space or newline policy.
- **No packaging.** No systemd unit, no `.desktop` file, no Nix derivation or
  flake, no Dockerfile, no goreleaser config — verified by search across the
  repo. Startup is `exec mavor daemon` in the Sway config (`README.md:24-28`), so
  there is no restart-on-crash and no log rotation. `--log-file`
  (`main.go:77-82,96-104`) appends forever.
- **No CI.** No `.github/`, no `.gitlab-ci.yml`. `just check` exists and is good;
  nothing runs it but a human.
- **No metrics or health endpoint.** `slog` only. There is no way to ask the
  daemon "how many dictations, how long did whisper take" other than grepping
  the log — though the timing *is* logged (`speech.go:102-107`, `output.go:60-63`).
- **No `mavor` hotkey of its own.** Binding is entirely the compositor's job.
- **No multi-instance or multi-seat story.** One socket path derived from
  `XDG_RUNTIME_DIR`, falling back to `/tmp/mavor-<uid>` (`config.go:81-87`).
- **No flag validation.** `runDaemon` hand-rolls its argument loop
  (`main.go:72-83`); unrecognized flags are silently ignored, so `mavor daemon
  --verbsoe` runs quietly at Info level. `toggle` and `status` accept no flags.
- **No model integrity check.** `models pull` (`main.go:183-233`) downloads to
  `.part` and renames — good — but does no checksum, no size check, and no
  resume. The daemon's precondition is `os.Stat` (`main.go:108`).

---

## 10. Risks

| # | Risk | Evidence | Mitigation |
|---|---|---|---|
| R1 | **Stuck in `Recording` when capture fails to start** — overlay lies, takes two more keypresses to clear. | `daemon.go:107-110` vs `state.go:109-112`; acknowledged in `overlay_test.go:38-39` | Add a `Recording × EventTranscribeFailed → Idle` edge, or introduce an explicit `EventAbort`. One line in `transition`; needs a test that the daemon returns to `Idle` when `Recorder.Start` errors. |
| R2 | **Concurrent toggles can deliver listener callbacks out of order.** `Apply` serializes the state word under the lock but releases it before running listeners (`state.go:79-83`), so two racing toggles can run `onTransition(Transcribing)` before `onTransition(Recording)` — meaning `recorder.Stop()` before `recorder.Start()`. | `state.go:68-85`, `ipc.go:68` (one goroutine per connection) | Unreachable at human keypress speed, and `Recorder` errors safely if it happens (`audio.go:112-114`). If it ever matters, serialize side effects on a single dispatch goroutine rather than running them on the caller's. |
| R3 | **Unbounded, personal data accumulation** in `$TMPDIR/mavor-recordings` — every dictation's audio *and* text kept forever (§7.9). | `main.go:112`, `audio.go:80`, `speech.go:111`; no `os.Remove` covering that dir | Decide a retention policy (OQ-3). Cheapest honest version: delete both files after a successful emit, keep them on failure. |
| R4 | **Silent failure is the default user experience.** Six of the failure modes in §7 look identical from the user's chair: the pill disappears and nothing is typed. | `daemon.go:99-151` — every error path ends at a logger | Needs an error `Visual` (OQ-2). The overlay is the only channel that exists. |
| R5 | **`overlay.Show` cannot report a failure to draw.** It posts to the render goroutine and returns nil, so a compositor that has stopped accepting frames looks identical to one that is working. The error is held until `Close`. | `overlay_wl.go` (`post`, `Close`) | Deliberate: this is what stops a wedged compositor from wedging the toggle path. If the overlay ever becomes load-bearing, surface the error asynchronously instead. |
| R6 | **The whisper sidecar contract is unverified against the real binary** — the only tests are fakes written to match the assumption, and the `e2e` tag that would check it is documented but empty (§8). | `speech.go:111-116`, `speech_test.go:39-43`, `harness.go:271-281`, no `//go:build e2e` anywhere | Write one `e2e`-tagged test that runs the real `whisper-cli` against a real `tiny.en` and a 1-second fixture. The `justfile` recipe already exists and downloads the model. |
| R7 | **Two exported sentinel errors are dead**, and their doc comments assert behavior the code does not have. | `ipc.ErrAddrInUse` (`ipc.go:123-125`), `daemon.ErrNotRunning` (`daemon.go:164-166`) | Either return them (and let callers `errors.Is`) or delete them. Today they are a trap for the next person who tries to match on them. |

---

## Open Questions

These are the decisions I cannot make for you. Each one changes a type
signature or a protocol, so they are worth settling before the next feature
lands on top of the current shape.

1. **OQ-1. Is batch transcription the design, or a v1 compromise?**

   `Transcriber.Transcribe(ctx, wavPath) (string, error)` and
   `Recorder.Stop() (string, error)` together make the pipeline
   record-then-transcribe by construction (§6.1, §6.2). Streaming, live partial
   text in the overlay, VAD-based auto-stop, and a level meter are *all* blocked
   behind the same two signatures. This is the question that decides whether the
   other five matter: if the answer is "streaming eventually", then the
   `Recorder` should grow a chunk channel *before* anyone adds features on top
   of the file-path shape, because every one of those features will have to be
   rewritten.

   _Leaning:_ Keep batch. Whisper's own quality is meaningfully worse on short
   streaming chunks, the tap-talk-tap interaction genuinely does not need
   partials, and the current shape is the reason the daemon is 166 lines. But
   decide it *explicitly* and write it down, because right now it is an
   accident of the first implementation rather than a choice.

   **Answer:**
   > _(empty — fill in when decided)_

2. **OQ-2. What is the error channel to the user?**

   Today: there isn't one. Every failure in §7 terminates in `slog`, and the
   overlay's `Visual` enum has no way to say "something went wrong"
   (`overlay.go:12-19`). A user whose `wtype` is broken, whose mic is muted, or
   whose model is corrupt sees exactly what a user who said nothing sees. This
   blocks R1 and R4 both, and it decides whether `Visual` stays a bare enum or
   becomes a struct that carries a message.

   _Leaning:_ Add a fourth `Visual` — an `Error` state carrying a short string —
   and hold it on screen for ~3 seconds before returning to `Hidden`. That
   requires `Show` to take data, which is the real change; `notify-send` would
   be the cheap alternative but adds a fifth subprocess and a second UI idiom.

   **Answer:**
   > _(empty — fill in when decided)_

3. **OQ-3. Who owns the recordings, and for how long?**

   Nothing deletes `$TMPDIR/mavor-recordings/rec-*.wav` or the `.wav.txt`
   sidecars beside them (§7.9, verified). That is simultaneously a disk leak, a
   privacy exposure, and — read generously — the only debugging artifact the
   system produces. It cannot be all three. The answer decides whether the
   recording directory becomes a config field, whether `mavor` grows a
   `history`/`replay` subcommand, and whether a failed dictation is recoverable.

   _Leaning:_ Delete both files after a successful emit; keep the last N (say
   10) failures for debugging, pruned at daemon startup. That makes the
   directory a bounded diagnostic buffer rather than an archive, and it is a
   ~20-line change contained inside `runTranscription`.

   **Answer:**
   > _(empty — fill in when decided)_

4. **OQ-4. Should the FSM grow an error/cancel vocabulary, or should the
   pipeline stop asking it to?**

   Two symptoms, one root: `Recording` has no failure edge (R1), and
   `Transcribing` deliberately ignores toggles (§4), so there is neither a
   recovery path nor an abort. Options are (a) add edges — `Recording ×
   Failed → Idle`, plus a real `EventCancel` reachable from both non-idle
   states; (b) keep the FSM minimal and move failure handling into the daemon,
   which then needs its own error state and stops being a pure reflection of
   the machine. This decides whether `state.go` stays a 121-line file with no
   dependencies that you can read in one sitting — which is currently one of
   the best things about the codebase.

   _Leaning:_ (a), but narrowly — add the missing `Recording × Failed` edge now
   because it is a bug, and defer `EventCancel` until OQ-1 is settled, since a
   cancel needs `Recorder`/`Transcriber` to support abandonment anyway (§6.1.2).

   **Answer:**
   > _(empty — fill in when decided)_

5. **OQ-5. Do the four interfaces become user-selectable, or stay test seams?**

   `cmd/mavor/main.go:119-133` hardcodes one stack, and `config.Config` has no
   field that could choose another (§6.5). Making them selectable means a
   registry keyed on config strings, which means every implementation needs a
   uniform constructor signature and a config sub-table for its own options
   (model paths, API keys, device names) — a real amount of machinery for a
   single-user tool. The stakes: this decides whether "add a cloud whisper
   backend" is a config change or a recompile, and whether the Noop fallback stays the
   only switch in the system.

   _Leaning:_ Don't build a registry. Add the *specific* config fields you
   actually want (`audio_source`, `whisper_args`, `output_mode`) and let them
   parameterize the existing implementations. A plugin architecture for a
   personal dictation tool is the wrong trade; a `output_mode = "clipboard"`
   string is the right one.

   **Answer:**
   > _(empty — fill in when decided)_

6. **OQ-6. Is `mavor toggle` allowed to be slow?**

   The first toggle's IPC response is not written until `overlay.Show` has
   round-tripped through the overlay *and* `parec` has been forked
   (§3 step 3), because side effects run inline on the listener, which runs
   inline on the handler goroutine. The client budget is 2 seconds
   (`main.go:156`), after which the user's keypress reports failure even though
   the daemon may well have started recording. Making `onTransition` fully
   asynchronous would fix that but would break the current property that
   `handleRequest`'s returned state is the state *after* side effects were
   attempted — which is exactly what makes the integration tests deterministic.

   _Leaning:_ Leave it synchronous. The latency is a fork/exec plus a channel
   callback — single-digit milliseconds in practice — and the determinism is
   worth more than the margin. But if the recorder ever grows a slow start
   (device negotiation, a network backend), this flips immediately, so it is
   worth knowing it is a deliberate trade rather than an oversight.

   **Answer:**
   > _(empty — fill in when decided)_

---

## Appendix: verification notes

Claims in this document were checked against the working tree on
**2026-08-15**. Specifically:

- Line counts: `find cmd internal test -name '*.go' | xargs wc -l` → 3,177.
- Absence claims in §9 were each checked by search (`rg -ni` over `cmd internal
  test justfile README.md`, plus `fd` for `.github`, systemd units, Nix files,
  and Dockerfiles) before being asserted. Where a search returned only false
  positives, that is noted inline.
- §7.9 (nothing cleans up recordings) was verified by enumerating every
  `os.Remove`/`os.RemoveAll` call in non-test code: `main.go:225`, `main.go:229`
  (model download temp file), `ipc.go:56`, `ipc.go:99` (socket file). None
  touches `mavor-recordings`.
- §8 (empty `e2e` tag) was verified by `rg '^//go:build' cmd internal test`,
  which returns eight lines, none mentioning `e2e`.
- The one thing I could **not** verify from the repo is whether the real
  `whisper-cli -otxt` writes to exactly `<wav>.txt` (§6.2.4, R6). Every test
  that exercises that path uses a fake written to match the assumption. It is
  presumably correct — the daemon evidently works for its author — but it is an
  assumption, not a tested fact, and I have flagged it rather than asserted it.
