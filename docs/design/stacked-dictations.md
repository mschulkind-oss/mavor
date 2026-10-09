---
next: "Retire the implementation sketch against what shipped"
status: accepted
stage: BUILT
depends-on: [visible-model-initialization.md, incremental-final-transcription.md]
---

# Stacked dictations: let the next recording start while the last one types

**Status:** 2026-10-09, built by `4877cfa` (daemon), `1208a3b` (overlay), and the
changes in this landing commit that wired the daemon to `ShowStack`.
**MEASURED:** the state/IPC/CLI transitions, the single- and two-pill overlay
request sequences, the failed cycle's error row, and a headless-Sway screenshot
of a stacked scene all ran, with `just check`, `just lint-tagged` and the
headless-Sway integration suite green. **UNMEASURED:** no real microphone, model
inference, GPU, or non-Sway desktop ran; a three-deep stack and the `+N`
collapse were exercised only by unit tests and the storybook painter, and the
GTK storybook recipes could not run in this environment.

> **In short.** `mavor toggle` while a transcription is in flight is a deliberate
> no-op today, so a follow-up thought waits for the previous dictation to finish
> typing. Stacking reverses that: capture restarts immediately, at most one
> recording exists at a time, and finished transcripts are typed in spoken order.

**Why it matters.** The user's own words: "sometimes I have another thought that
I want to add on to the end." Today the key silently does nothing for three to
seven seconds, and the thought is lost.

**The shape.** A per-cycle FSM snapshot replaces the single-state enum, a FIFO of
pending decodes feeds one ordered output dispatcher, and the overlay paints one
pill per live cycle, stacked, inside the canvas it already allocates.

**Cost.** The one-state FSM, every `onTransition` side effect, and every test
asserting the no-op all change. Ducking restore moves from `Idle` to
capture-stop. Live-final modes do not stack.

**Start at [§4](#4-proposed-fsm-shape-and-invariants)** — every other section
falls out of the FSM shape.

**Needs your ruling:** None. All nine questions are ruled and compacted into
the [Decision Ledger](#decision-ledger).

**Reads with:** [`stacked-dictations-plan.md`](./stacked-dictations-plan.md) (the
implementation sketch — ruled, but still to be turned into a real plan), [how mavor
works](../reference/how-mavor-works.md) (the FSM and its invariants), [the
overlay resize design](./overlay-geometry.md) (the fixed surface this leans on),
[the config surface](./configuration-surface.md) (the one-writer overlay rule).

---

## 1. The current FSM and every consumer

### 1.1 The machine

`internal/state/state.go` is a five-state enum with six events:

- `State` enum and `String()`: `internal/state/state.go:14-37`.
- Events, including the comment that `EventToggle` "start[s] recording, stop[s]
  transcrib[es], or [is a] no-op while a transcription is in flight":
  `internal/state/state.go:40-56`.
- `Machine{mu, state, nextID, listeners}`: `internal/state/state.go:59-63`.
- `Machine.State()`: `internal/state/state.go:73-77`.
- `Machine.Apply()` — snapshots listeners under the lock, runs them **off** the
  lock, returns `(State, changed)`: `internal/state/state.go:80-100`.
- `Machine.Subscribe()` — returns an unsubscribe closure:
  `internal/state/state.go:106-117`.
- `transition()`: `internal/state/state.go:122-148`.

The load-bearing line is the last case of `transition`:

```
case Transcribing:
    if e == EventTranscribeDone || e == EventTranscribeFailed {
        return Idle
    }
    // EventToggle, EventRecordStart, and EventRecordStop while transcribing are intentionally no-ops.
```

`internal/state/state.go:141-145`. That comment is the product decision this
design reverses.

`transition` is also lossy for a fifth reason: it has **no cycle identity**. The
only reason `Idle` can follow a `Transcribing` is that exactly one transcription
can exist, so the single `EventTranscribeDone` unambiguously means "the
transcription". Stacking breaks that assumption, so completion must carry an id
([§4](#4-proposed-fsm-shape-and-invariants)).

### 1.2 Every consumer of the machine

`state.Machine`, `State()`, `Apply` and `Subscribe` are used in exactly these
places (verified by `rg` over `*.go`):

| Consumer | Site | Uses |
| :--- | :--- | :--- |
| daemon construction | `internal/daemon/daemon.go:189` | `state.New()` |
| daemon subscribe | `internal/daemon/daemon.go:225-227` | `Subscribe(func(State))` → `onTransition` |
| initialization start | `internal/daemon/daemon.go:233` | `Apply(EventInitialize)` |
| initialization success | `internal/daemon/daemon.go:267` | `Apply(EventReady)` |
| initialization failure | `internal/daemon/daemon.go:277` | `Apply(EventInitializeFailed)` |
| shutdown capture check | `internal/daemon/daemon.go:303, 318` | `State() == Recording` |
| init notice gate | `internal/daemon/daemon.go:363` | `State() == Initializing` |
| request logging | `internal/daemon/daemon.go:368` | `State()` |
| toggle | `internal/daemon/daemon.go:371` | `Apply(EventToggle)` |
| start | `internal/daemon/daemon.go:375` | `Apply(EventRecordStart)` |
| stop | `internal/daemon/daemon.go:379` | `Apply(EventRecordStop)` |
| status | `internal/daemon/daemon.go:383` | `State().String()` |
| transcribe failure | `internal/daemon/daemon.go:407` | `Apply(EventTranscribeFailed)` |
| diagnostic retention | `internal/daemon/daemon.go:843` | `State() == Failed` |
| empty transcript | `internal/daemon/daemon.go:1009` | `Apply(EventTranscribeDone)` |
| silence skip | `internal/daemon/daemon.go:1114` | `Apply(EventTranscribeDone)` |
| normal completion | `internal/daemon/daemon.go:1146` | `Apply(EventTranscribeDone)` |
| upgrade watch | `internal/daemon/upgrade.go:103` | `State() != Idle` |
| warning hide timer | `internal/daemon/backup_adapter.go:100` | `State().String() == "idle"` |

There is no consumer outside `internal/daemon` except tests. The IPC layer
(`internal/ipc/ipc.go`) and the CLI (`cmd/mavor/main.go:282-311`) see only the
**string** in `ipc.Response.State` (`internal/ipc/ipc.go:27-32`), which is the
seam that keeps this change off the wire protocol if we keep the coarse state
name ([§6](#6-cli--ipc-surface)).

Tests that exercise the machine directly or assert its sequence are enumerated in
[`stacked-dictations-plan.md`](./stacked-dictations-plan.md), which carries the
build order and the test-inversion list.

### 1.3 What is actually load-bearing

`docs/reference/how-mavor-works.md` states the invariants that will change:

- **I4** — "At most one transcription goroutine exists at a time, enforced only
  by the shape of the FSM … the sole `wg.Add(1)` is on the `Recording →
  Transcribing` edge, and `Recording` is unreachable again until that goroutine
  has driven the machine back to `Idle`. There is no explicit guard."
  (`docs/reference/how-mavor-works.md:100-103`)
- **I3** — listeners run off the lock so a listener may re-enter `Apply`
  (`reportError` does): `docs/reference/how-mavor-works.md:89-92`. The new
  machine must preserve this.
- The "one dictation cycle" sequence and "toggling while transcribing does
  nothing" narration: `docs/reference/how-mavor-works.md:161-172` and the
  sequence diagram at `:184-224`. These need edits in the same commit as the
  behavior.

The reference doc [already records this edge as a real
bug](../reference/how-mavor-works.md#why-its-this-way) — a failed capture start
used to leave the overlay claiming RECORDING — so reversing it is a deliberate
product change, not a regression of that fix.

---

## 2. `Daemon.onTransition` and the per-state side effects

`onTransition` is `internal/daemon/daemon.go:412-499`. It runs on the FSM
listener, inline on the IPC handler goroutine (except where it explicitly
forks). Per state:

**Common prologue (`:417-437`)**

- `provenanceMu` lock; on entering `Recording`: clear `source`/`warning`,
  reset `warningUntil`, `warningGen++`, `cycleID++` (`:418-427`).
- `overlay.Show(visualFor(s))` unless `keepWarning` (an `Idle` inside a live
  warning window) (`:428-437`). `visualFor` is `:1164-1176`.

**`Idle` (`:439-450`)**

- `stopLevelMonitoring()` (`:500-535`) — cancels the level ticker and pushes
  level 0.
- `stopStreamingMonitoring()` (`:841-914`) — cancels the preview driver and
  schedules the preview recognizer's final decode as a drain.
- `ducker.Restore()` if `cycleID != 0` (`:443-450`).

**`Recording` (`:452-475`)**

- `ducker.Duck()` (`:453-458`) — captures the current sink/stream level.
- `recorder.Start(ctx)` (`:459-464`).
- `startLevelMonitoring(ctx)` (`:465`, impl `:500-535`).
- `startFinalCycle(ctx)` if `incrementalFinal()`, else
  `startStreamingMonitoring(ctx)` (`:466-475`).

**`Transcribing` (`:476-499`)**

- `stopLevelMonitoring()` (`:477`).
- `stopFinalPump()` or `stopStreamingMonitoring()` (`:478-483`).
- `ducker.Restore()` (`:484-489`) — "capture is finished by now … bring
  background media back up" (`:481-486`).
- `wg.Add(1)`; `go d.runTranscription(ctx)` (`:490-498`). `runTranscription` is
  `:953-1160`.

### 2.1 Classification

| Side effect | Class | Why / what must change |
| :--- | :--- | :--- |
| `recorder.Start` / `recorder.Stop`, `Level()` | **(a) single-instance** | `ParecRecorder` refuses a second `Start` ("audio: capture already in progress", `internal/audio/audio.go:128-130`), and `Stop()` returns the one file. There is one capture, one WAV path, one level meter. Cannot be duplicated; must be hand-off-serialized ([§5.4](#54-the-recorder-single-instance-hand-off)). |
| `ducker.Duck` / `ducker.Restore` | **(a) single-instance** | `CommandDucker` remembers one pre-duck volume (`internal/audio/ducking.go:296-297`). Two overlapping Duck/Restore pairs would clobber the saved level. |
| `startLevelMonitoring` / `levelCancel` | **(a) one writer, per recording** | Already cancels any previous ticker (`:502-506`), so it is effectively one-at-a-time; correct while at most one recording exists. `Level()` is recorder-wide, not per-cycle. |
| `overlay.Show` / `SetLevel` / `SetText` | **(b) shared, must become multi-row** | One overlay, one scene. [§7](#7-overlay-n-stacked-pill-rows) turns it into an ordered stack. There must still be exactly one writer (the daemon loop), but the *content* is now N rows. |
| `cycleID` (`:38`, `:425`) | **(b) must become per-cycle id** | It is a monotone counter used as provenance/backup identity (`usableBackup`, `backup_adapter.go:107-108`). It stays monotone but must be captured per cycle, not read as `d.cycleID` at completion time (`daemon.go:1069` reads the *current* value — with stacking that is the wrong cycle). |
| `warning`/`source`/`warningUntil`/`warningGen` (`:41-44`) | **(b) shared provenance** | One slot today; with stacking it must describe the *most recent completed* cycle, and each cycle carries its own candidate. `warningGen` guards the deferred overlay hide (`backup_adapter.go:80-104`). |
| `streamGen`, `streamHistory`, `streamHadSpeech`, `streamSource`, `streamUsesMain`, `streamDrain`, `streamFeedDone` (`:85-97`) | **(b) must become per-cycle** | These describe *the* recording. `streamGen` is the one-writer guard (`previewLive`, `:837-839`). With stacking, a second recording will move `streamGen` while the first cycle's preview/drain is still finishing; per-cycle ids replace the single generation. |
| `finalCycle *incrementalCycle` (`:49`) | **(b) must become per-cycle** | It is a single pointer to one live final session; `startFinalCycle` overwrites it (`final_cycle.go:75`) and `stopFinalPump`/`cancelFinalCycle` act on it. |
| `backup *BackupCycle` (`:37`) | **(b) must become per-recording/cycle** | `startBackup` explicitly "replaces companion preview ownership" by cancelling the previous one (`backup_adapter.go:14-28`). It cannot be two. |
| `controlMu` (`:73`) | **(c) already fine, keep** | Serializes concurrent IPC control requests so a transition finishes starting/stopping capture before the next. It must also now cover the recorder hand-off. |
| `wg *sync.WaitGroup` (passed into `onTransition`) | **(b) stays, but must count N** | Today it counts the one transcription goroutine. Shutdown `wg.Wait()` (`:326`) is what stops a goroutine writing to `wl-copy` after exit. It must count every in-flight transcription/dispatch. |
| `provenanceMu` (`:39`) | **(c) keep** | Independent of per-cycle data; still guards the "last completed" provenance exported to status. |
| `previewMode`, `transcriber`, `companion` (`:53-77`) | **(c) already per-daemon config** | Decided once at start (`speech.ResolvePreview`). Not per-cycle; see [§5.5](#55-the-preview-companion) for the companion contention. |
| phrase buffers (`runPhrasePreview`, `phraseBuffer.take`, `daemon.go:708-763`, `phrasebuffer.go`) | **(b) per-recording** | Each preview driver makes its own local `phraseBuffer` inside `runPhrasePreview` (`:722`), so it is already per-call — but the driver is a single daemon-owned goroutine with daemon-owned `streamFeedDone`/`streamCancel`. With two recordings the buffers must be owned by the recording's cycle state. |

### 2.2 `final_cycle.go`, `backup_cycle.go`, `backup_adapter.go`

- `incrementalCycle` (one `speech.FinalSession` + cancel + done + once):
  `final_cycle.go:14-19`. `startFinalCycle` (`:24-97`) overwrites
  `d.finalCycle`; `stopFinalPump` (`:99-113`) cancels and joins it;
  `cancelFinalCycle` (`:115-123`) also cancels `d.backup` and the session.
- `BackupCycle` (one companion stream, its own queue/digest/coverage):
  `backup_cycle.go:36-58`. It is created with `CycleID` in
  `BackupCycleOptions` (`:26-31`) and stamps the result (`:210`).
- `startBackup`/`feedBackup`/`runBackupPump`/`usableBackup`:
  `backup_adapter.go:14-28`, `:30-38`, `:42-73`, `:107-109`.

`BackupCycle` is already an isolated value with its own lifecycle, so the
mechanical change is to move the `d.backup` *pointer* into the cycle that owns
the recording, and to key `startBackup`/`usableBackup` on the cycle id rather
than `d.cycleID`. `BackupResult.CycleID` already exists for exactly that.

---

## 3. Per-cycle bookkeeping: what moves and where

Introduce one value, owned by the daemon, describing one dictation from key-down
to dispatch. Proposed home: a new `internal/daemon/cycle.go` (no cgo, testable
like `backup_cycle.go`).

```go
// dictationCycle is one key-down..emit run. Exactly one may be Recording; any
// number may be queued/finalizing. It owns everything that was a Daemon field
// when only one could exist.
type dictationCycle struct {
    id        uint64          // monotone, from Machine
    wav       string          // capture file once stopped
    provenance cycleProvenance // source, warning, model

    // live preview (whatever mode this daemon resolved)
    cancel    context.CancelFunc
    feedDone  chan struct{}
    drain     chan struct{}
    src       speech.StreamTranscriber // main-preview or companion source
    usesMain  bool
    hadSpeech bool                     // recognition evidence for the energy filter
    history   string                   // phrase-mode accumulation
    backup    *BackupCycle
    final     *incrementalCycle
}
```

| Today's field | Site | Becomes |
| :--- | :--- | :--- |
| `cycleID uint64` | `daemon.go:38, 425, 1069` | `dictationCycle.id`; a `nextCycleID` counter on the daemon. All reads that currently use `d.cycleID` at completion (`usableBackup(r, d.cycleID)`, `daemon.go:1069`) use the cycle's own id. |
| `warningGen uint64` | `daemon.go:44, 424, 1052, backup_adapter.go:87` | Two things: the monotone id already supplies "which cycle", so the gen reduces to "is the newest completed cycle still `c.id`". Keep a small `provenance` struct. |
| `source, warning string`, `warningUntil time.Time` | `daemon.go:42-43`, `backup_adapter.go:75-104` | Daemon still exports the *last completed* values (IPC `Source`/`Warning`), but each cycle computes its candidate locally and commits on dispatch. |
| `streamGen uint64` | `daemon.go:86` | `dictationCycle.id` + a daemon `liveID uint64` naming the recording whose preview may paint. `previewLive` (`:837-839`) compares `cycle.id == liveID`. |
| `streamHistory` | `daemon.go:83` | `cycle.history` (phrase/preview accumulation). |
| `streamHadSpeech` | `daemon.go:88` | `cycle.hadSpeech`; the energy filter reads the *cycle being finalized*, not a daemon-global bit. This is what `TestPreviewSpeechEvidenceDoesNotLeakToNextRecording` (`silence_preview_test.go:134`) is guarding, so it must stay per-recording. |
| `streamSource, streamUsesMain` | `daemon.go:85, 97` | `cycle.src`, `cycle.usesMain`. |
| `streamDrain, streamFeedDone` | `daemon.go:95-96` | `cycle.drain`, `cycle.feedDone`. `awaitStreamDrain` becomes per-cycle, or a daemon `awaitCycleDrain(cycle)`. |
| `finalCycle *incrementalCycle` | `daemon.go:49` | `cycle.final`. |
| `backup *BackupCycle` | `daemon.go:37` | `cycle.backup`. |
| `previewMode`, `transcriber`, `companion`, `mainModel`, `companionModel` | `daemon.go:59-77` | **Stay daemon-global.** They are resolved once (`speech.ResolvePreview`) and are not per-cycle. This is also why two concurrent companion streams are impossible ([§5.5](#55-the-preview-companion)). |
| phrase buffers | `daemon.go:722`, `phrasebuffer.go` | Move into `dictationCycle` (or a per-cycle `previewDriver`). |
| `controlMu`, `provenanceMu`, `wg`, `warningWG` | `daemon.go:39, 73, 225, 326` | **Stay daemon-global.** Locks and the shutdown wait group are shared by construction. |

The daemon keeps:

```go
recording  *dictationCycle   // nil or the one active capture
pending    []*dictationCycle // ordered, oldest first; stopped, not yet emitted
```

Everything else in `Daemon` is either configuration, interface handles, or the
locks above.

---

## 4. Proposed FSM shape and invariants

### 4.1 Why not the flat enum

A single `State` cannot say "recording *and* two transcribing". The state that
`mavor status` and the overlay need is a **snapshot**, not an enum. So the
machine grows cycle identity and a list, and `State` remains as the *derived
coarse* value so the IPC wire (`ipc.Response.State`) and `writeStatus`
(`cmd/mavor/main.go:288-308`) keep working unchanged.

### 4.2 The snapshot

```go
// internal/state/state.go
type Snapshot struct {
    Phase       Phase   // Initializing | Ready | Failed
    RecordingID uint64  // 0 when no capture is active
    Pending     []uint64 // stopped cycles awaiting transcription, oldest first
}
```

`Machine` gains these methods (replacing direct `transition` use):

```go
func (m *Machine) RecordStart() (id uint64, snap Snapshot, changed bool)
func (m *Machine) RecordStop() (id uint64, snap Snapshot, changed bool)
func (m *Machine) Finish(id uint64) (snap Snapshot, changed bool) // Done or Failed
func (m *Machine) Snapshot() Snapshot
```

`Apply(Event)` is kept and delegates for the lifecycle events
(`EventInitialize`, `EventReady`, `EventInitializeFailed`); the toggle family
maps onto the new methods with `id = 0` (meaning "the current recording"), so
existing daemon call sites and `TestApplyTransitions` can be migrated
deliberately rather than deleted. `Subscribe` is unchanged, but listeners are
notified on the **derived** `State` and on `Snapshot` changes; the daemon
registers one listener for both.

### 4.3 Derived `State` (what `mavor status` still prints)

| Condition | `State()` |
| :--- | :--- |
| `Phase == Initializing` | `Initializing` |
| `Phase == Failed` | `Failed` |
| `RecordingID != 0` | `Recording` (regardless of `len(Pending)`) |
| `RecordingID == 0 && len(Pending) > 0` | `Transcribing` |
| otherwise | `Idle` |

This preserves every existing string in `ipc.Response`, so
`cmd/mavor/status_policy_test.go` and every `waitForState` in the daemon tests
keep their meaning for the single-dictation case.

### 4.4 Invariants (state them precisely)

1. **At most one recording.** `RecordingID` is either `0` or one id.
   `RecordStart` from `Idle` or `Transcribing` allocates a fresh id and sets it;
   `RecordStart` from `Recording` is a no-op (`changed=false`).
2. **`RecordingID` is never in `Pending`.** `RecordStop` moves the id from the
   recording slot to the tail of `Pending`.
3. **`Pending` is strictly increasing by id, no duplicates.** It is FIFO by
   cycle id, which equals spoken order because recording is single-instance.
4. **A cycle id has exactly one lifecycle owner**: the recording slot, or
   `Pending`, or retired. A retired id may be re-`Finish`ed harmlessly
   (`changed=false`) so late/cancelled completions are idempotent.
5. **`Idle` only when `RecordingID == 0 && len(Pending) == 0`.** No transcription
   can outlive the last pending id. This is the "return to Idle only when the
   last one completes" rule.
6. **`Initializing`/`Failed` unchanged.** `RecordStart`/`RecordStop` in
   `Initializing` or `Failed` is a no-op; `Ready` is the only way out of
   `Initializing`, `InitializeFailed` the only way to `Failed`. No recording or
   pending cycle can exist while `Phase != Ready`.
7. **Listeners fire only on change and off the lock** (preserves reference I3).
8. **Toggle vocabulary.** `EventToggle` means "advance the recording slot":
   start if none, stop if one. `EventRecordStart` starts only if none;
   `EventRecordStop` stops only if one. Restated: toggle/start while
   `Transcribing` (no recording) now **starts recording** — the behavior change.

### 4.5 Transition table (recording slot only; `Pending` transitions listed separately)

| From \ Event | `EventToggle` / `EventRecordStart` | `EventRecordStop` | `Finish(id)` |
| :--- | :--- | :--- | :--- |
| `Idle` (no rec) | → `Recording` (new id) | — | — (no pending) |
| `Recording` | — (if toggle: start is no-op) | → `Transcribing`, id appended | — |
| `Transcribing` (no rec) | → `Recording` (new id; `pending` untouched) | — | remove `id`; → `Idle` iff empty |
| `Recording` **and** pending | toggle = stop → `Transcribing` | → `Transcribing` | (separate; slot unaffected) |

---

## 5. Output ordering, ducking, preview, recorder, third dictation

### 5.1 Ordering rule

**Rule: finished transcripts are emitted in spoken order — strictly increasing
cycle id — never in completion order.** Justification: `output.Emit` types
synthetic keystrokes into the focused window
(`internal/output/output.go:79`, `internal/output/paste.go:208`). Typing is
sequential by nature, and the user is composing prose. If dictation 2 finishes
before dictation 1, completion-order typing would drop sentence 2 ahead of
sentence 1 and corrupt the text — a silent, unrecoverable defect. Spoken order
is the only order that preserves meaning.

**Where it belongs.** A small ordered dispatcher, proposed
`internal/daemon/dispatch.go`, owned by the daemon and consulted by every
completion path (`runTranscription`'s three `EventTranscribeDone` sites at
`daemon.go:1009, 1114, 1146`). It is not the FSM's job (the FSM does not know
about output) and not `output`'s (the dispatcher cannot see cycle ids). Shape:

```go
// dispatchQueue emits completed transcripts by ascending cycle id.
type dispatchQueue struct {
    mu      sync.Mutex
    next    uint64                 // lowest id not yet emitted
    done    map[uint64]dispatchResult // completed, waiting for `next`
}
```

A cycle that finishes early parks its `(text, provenance)` in `done`; a later
cycle that *is* `next` emits, then drains any consecutive successors. A cycle
that produced empty text or failed still advances `next` (it is a completed
step, not a gap) so one silent dictation cannot wedge the queue forever.

**History** should be appended by the dispatcher, in the same order as `Emit`,
so `mavor history` and `mavor history --pick` (newest first) match what was
typed. Move the `d.history.Append` call (`daemon.go:1125-1129`) into the
dispatcher.

### 5.2 Background-audio ducking

Today: `Duck` on `Recording` (`daemon.go:453`), `Restore` on `Transcribing`
(`:486`) and again on `Idle` (`:446`). The design rule for stacking is: **duck
exactly while a capture is active; restore when capture stops.** That is
already what the `Transcribing` restore does; the `Idle` restore is a safety net
for cycles that skip `Transcribing` (e.g. capture-start failure).

The ruling confirmed that this is the intended feel: the volume comes back the
moment the key is released, so media is audible during a transcribing-only gap,
and it ducks again the instant the next capture starts.

With stacking, `Idle` no longer means "capture stopped" — it means "everything
finished". Keeping the `Idle` restore would re-call `Restore` long after the
fact, which is harmless for `CommandDucker` (it returns the saved level) but
wrong in principle and racy with a *new* recording's `Duck` if the timing slips
(`${mod}+grave` tapped again in the Idle restore window). So:

- `Duck` on entering the recording slot; `Restore` on leaving it (→ any
  `Transcribing`), including capture-start failure.
- Delete the `Idle` restore, or gate it on "no recording was ever ducked this
  session". Shutdown keeps its best-effort restore (`daemon.go:326-334`) but
  keys it on "ducked right now", not `cycleID != 0`.
- `TestDuckingOnRecordingAndRestoreOnIdle` (`daemon_test.go:295`) asserts the
  current double-restore count and must be rewritten to "ducked while recording,
  restored as capture stops" ([§5.2](#52-background-audio-ducking)). `TestDuckingRestoresOnEnteringTranscribing`
  (`:716`) survives.

### 5.3 Output must not be blocked by a slow dispatch

`Emit` runs on the transcription goroutine today (`daemon.go:1140`). With a
dispatcher, a slow `Emit` for cycle 1 must not block the *worker* decoding
cycles 2..N — otherwise model inference stalls behind typing. Run the
dispatcher on its own goroutine (a single consumer fed by the queue), separate
from the decode worker. This also keeps the model's single recognizer free.

### 5.4 The recorder: single-instance hand-off

The hard race: `runTranscription` calls `d.recorder.Stop()` **inside the
spawned goroutine** (`daemon.go:955`), while the `Recording` edge calls
`recorder.Start` inline on the handler (`:459`). Today that is safe only because
`Recording` is unreachable until the goroutine finishes. Stacking removes that
guarantee.

**Decision:** capture stop becomes part of the recording→transcribing edge, and
the edge does not complete until `recorder.Stop()` has returned and the WAV path
is recorded in the cycle:

- `onTransition(Transcribing)` (or the new `RecordStop` path) calls
  `recorder.Stop()` inline, stores `cycle.wav`, *then* starts the decode worker.
  This costs the `mavor stop` round-trip a `parec` reap (already paid today,
  just on a different goroutine) and removes the `Start`/`Stop` interleave
  entirely.
- Alternatively keep the async stop but make `RecordStart` wait on the previous
  cycle's `stopped` channel. Rejected as the default: it puts a wait inside the
  user's key-down path.
- Either way `ParecRecorder`'s own mutex (`internal/audio/audio.go:125-131`) is
  the backstop, not the primary guard.

The recorder remains **(a) single-instance, cannot be duplicated**.

### 5.5 The preview companion

There is exactly one `d.companion` (`daemon.go:61`, resolved once). It serves
two mutually exclusive roles per recording:

- streaming preview while recording (`runStreamPreview` / `runBackupPump`), and
- the finalized backup for a qualified GPU request failure (`BackupCycle`,
  `backup_adapter.go`).

`startBackup` **cancels the previous backup** to take ownership
(`backup_adapter.go:16-21`), and `awaitStreamDrain` exists precisely because a
recognizer cannot be fed a new stream while finishing the last
(`daemon.go:916-931`, and the comment at `:543-546`).

**Decision:** the companion cannot serve a second recording while the first is
still transcribing. A new recording that wants companion preview must wait until
the previous cycle's `backup` has joined (its `drain` has closed). Concretely:
`startStreamingMonitoring` for cycle N+1 calls `awaitCycleDrain(previous)`; if
the companion is still busy, cycle N+1 records with **no preview** and logs why
(the main `previewMode == main`/`phrases` paths are unaffected because they use
the main model, which has the same single-stream constraint — see below). No
second companion instance is created; loading one per cycle is rejected (memory,
GPU, startup cost).

**Main-model live preview** has the same constraint: a streaming main recognizer
can only run one stream, and `TestFinalNewRecordingHasIndependentMainState`
(`final_session_test.go:258`) plus `TestTheNextRecordingWaitsForTheDrain`
(`preview_latency_test.go:162`) encode that a new recording waits for the drain.
Under stacking, that wait now happens *after* a second recording may already be
capturing, so the preview simply starts late (or not at all) rather than
blocking capture. The recording is never held back by preview.

### 5.6 Third dictation while two are transcribing

**Decision: allow it, bounded.** `Pending` grows at most one entry per
stopped recording. Because capture is single-instance, a third dictation means:
record 1 → stop (pending 1), record 2 → stop (pending 2), record 3 starts while
1 and 2 decode. The snapshot is `RecordingID = 3`, `Pending = [1, 2]`.

Bound it explicitly:

- **In-flight decode workers: 1.** Model recognition is the scarce resource
  (GPU/CPU, one loaded model, one stream recognizer). Queue additional cycles
  behind a single decode worker rather than running N decodes. "N in-flight
  transcriptions" is satisfied by the pending list; only one is *decoding* at a
  time. This also makes completion order equal spoken order trivially, so the
  dispatcher in [§5.1](#51-ordering-rule) mostly faces the live-final/failure reordering cases.
- **Pending cap: 8, a named constant.** Each pending cycle holds one WAV on
  disk in `$TMPDIR/mavor-recordings` (`docs/reference/how-mavor-works.md`
  recording directory). Beyond the cap, `RecordStart` is refused (no-op,
  logged, and surfaced) rather than silently growing disk usage. The overlay
  shows three rows anyway ([§7](#7-overlay-n-stacked-pill-rows)) and collapses
  the rest to a count. It is not a config key.

---

## 6. CLI / IPC surface

`cmd/mavor/status` is `runStatus` → `writeStatus` (`cmd/mavor/main.go:282-311`).
`writeStatus` deliberately prints only `resp.State` to stdout (machine-readable)
and `source:`/`warning:` to stderr (`:292-308`), asserted by
`cmd/mavor/status_policy_test.go:10-17`.

**Settled: nothing parses the stdout word.** The only consumers are two sway
binds — `bindsym $mod+v exec mavor toggle` and `bindsym Control+$alt+v exec
mavor history --pick` — and neither reads `mavor status`, so stdout
compatibility is not a constraint.

**Ruled: report the stack in `mavor status` itself.** There is no stdout
compatibility constraint to preserve — the only consumers are two sway binds,
`bindsym $mod+v exec mavor toggle` and `bindsym Control+$alt+v exec mavor
history --pick`, and neither reads `mavor status`. The output is shaped for the
human, so the stack appears alongside the derived state rather than being hidden
behind a flag. What the design fixes:

- Extend `ipc.Response` (`internal/ipc/ipc.go:27-32`) with `Recording bool` and
  `Transcribing int` (`omitempty`), and optionally `Cycles []string`. Existing
  decoders ignore unknown JSON fields.
- `writeStatus` renders the derived state and then the stack — the recording row
  first if there is one. The exact wording is the implementer's.
- Add `mavor status --json` serializing the whole `Response`.
- `cmd/mavor/status_policy_test.go:10-17` asserts stdout is exactly the state
  word and must be updated to the new contract.

**Toggle/start/stop semantics with a recording already in flight:**

| Request | No recording, no pending | Recording, no pending | No recording, N pending | Recording + pending |
| :--- | :--- | :--- | :--- | :--- |
| `toggle` | start new recording | stop → transcribe | **start new recording** (changed) | stop → transcribe |
| `start` | start new recording | no-op | **start new recording** (changed) | no-op |
| `stop` | no-op | stop → transcribe | no-op | stop → transcribe |
| `status` | `idle` | `recording` | `transcribing` (+ count) | `recording` (+ count) |

Push-to-talk (key-down `start`, key-up `stop`) therefore "just works": the
second press starts a new recording even while an earlier one transcribes. The
init gating at `daemon.go:362-366` (only `start`/`toggle` in `Initializing`
shows the notice) is unchanged; `Initializing`/`Failed` still refuse recording.

The `controlMu` around toggle/start/stop (`daemon.go:356-360`) stays; it now also
serializes the recorder hand-off of [§5.4](#54-the-recorder-single-instance-hand-off).

---

## 7. Overlay: N stacked pill rows

### 7.1 The load-bearing constraint

`FixedSurfaceSize(maxPreviewWidth) (int, int, error)` (`internal/overlay/paint.go:100-118`)
computes **one** canvas, allocated once, and the surface is **never resized**.
`overlay_wl.go:216-233` allocates it at connect; `overlay_wl.go:544-555`
explicitly refuses to resize and logs if a scene wants a different size; the X11
HUD derives its geometry from the same function (`internal/overlay/x11_geometry.go:326-338`).
The reasons are in [`overlay-geometry.md`](./overlay-geometry.md): resizing
re-centres the surface, blocks the render loop on a compositor round-trip, and
races a stale configure. **A stacked row must fit inside the surface allocated
at startup.** Therefore `FixedSurfaceSize` must reserve height for the maximum
row count up front.

### 7.2 Minimal `Scene` change

Today `Scene` (`paint.go:64-91`) carries one `Visual`, one `Levels`, one
`Preview`. Add an ordered stack while keeping the existing fields as row 0, so
every current caller and test keeps compiling:

```go
type Scene struct {
    Visual Visual        // primary/top row (unchanged)
    Levels []float64     // waveform for the primary row (unchanged)
    Preview string       // subtitle strip (unchanged)
    // Stack are additional pill rows drawn below the primary, each a whole
    // pill. Transcribing rows carry no waveform and no subtitle. Empty for
    // every single-dictation scene.
    Stack []Visual
    Phase float64
    MaxPreviewWidth int
    SurfaceW, SurfaceH int
}
```

The daemon's mapping, given a `Snapshot`, is: `Visual = Recording` if
`RecordingID != 0` else `Transcribing`; `Stack = []Visual{Transcribing, ...}`
once per pending id; when not recording, `Visual` is the first pending and
`Stack` is the rest (or draw all pending as stacked transcribing rows with no
recording row). `Recording` is always row 0 when present because it owns the
waveform and is the live one.

### 7.3 SceneSize / Render / damage

- `SceneSize` (`paint.go:547-568`) currently returns `pillH` plus the subtitle
  height. New height when `SurfaceW/H` are unset (tests, storybook): `rows*(pillH+rowGap)` plus the subtitle if `hasSubtitle`. With `SurfaceW/H` pinned, it returns them unchanged (`:551-554`) — **already correct**, which is why the pinned case needs no resize.
- `RenderInto` (`paint.go:658-756`) draws the pill at `image.Rect(pillX, 0, …)`;
  wrap the existing body in a loop over rows, offset by
  `row*(pillH+rowGap)`, and move the `hasSubtitle` strip below the last row.
- `SceneBounds` (`paint.go:610-637`) must union the rows; it feeds the Wayland
  damage region, and underestimating it leaves stale pixels. `scenebounds_test.go:17-53`
  proves every drawn pixel is inside the reported bounds and must gain stacked
  cases.
- `FixedSurfaceSize` (`:100-118`): `h = maxStackRows*(pillH+rowGap) + previewGap + previewSize + 2*previewPadY + 4`. `maxStackRows` is a new constant (three: one recording plus two visible
transcribing rows). Rows beyond it are collapsed into a final "…" row or the count is shown on the last pill; see open questions.
- `render` width: the widest pill already fits (`:107-114`), and each pill is centred independently, so no width change is needed.

`pillSize` (`:571-588`) stays per-visual; `rowGap` is a new small constant
(proposed `previewGap` reuse or a dedicated 4-6 px).

### 7.4 The two backends

- **wl** (`overlay_wl.go`): `Show(v)` sets `desired.visual` (`:700-729`). Add a
  `desired.stack` and a scene translation in `paint` (`:514-570`) that copies it
  into `st.scene.Stack`. Because the surface height is fixed, no other change.
- **X11** (`overlay_x11.go`): same `desired` pattern (`:46-90`);
  `x11_geometry.go:326-338` automatically gets the taller HUD from
  `FixedSurfaceSize`, no per-row math.
- **Noop / Mock** (`internal/overlay/noop.go`, `mock.go`): Mock appends
  `Show` calls to `calls []Visual` (`mock.go:28-35`). Add a `ShowStack`/stack
  capture, or have `Show` accept the primary and a new `SetStack`. Proposed
  interface addition:

```go
// Overlay interface (overlay.go:60-70)
ShowStack(pills []Visual) error // pills[0] is the primary row
```

with `Show(v)` kept and implemented as `ShowStack([]Visual{v})`. This keeps the
existing `Show` call sites and tests (`overlay_test.go`, `paint_test.go`) valid.

### 7.5 Storybook / report / integration assertions to update

- `test/desktop/report.go:84-258` builds `Scenes()` (Recording variants,
  Transcribing, Error, Initializing, Degraded). Add a `stacked` scene and a
  `stacked` motion scene. `docs/reports/desktop-storybooks-qa.md` and
  `docs/reports/desktop-storybooks.md` are generated — rerun the recipe.
- `test/integration/ui_storybook_test.go` and `test/gnome/ui_storybook_test.go`
  render those scenes; GNOME's Python harness is
  `test/gnome/test_storybook.py`.
- `test/integration/preview_overlay_test.go:50-63` `overlayBand` assumes the
  overlay is one contiguous band; with two pills and no preview it returns the
  union, so its "does the overlay grow for the preview" assertion
  (`:140-160`) still holds but the band arithmetic should be revisited for the
  stacked case. `test/integration/daemon_overlay_test.go:49` (recording pill
  reaches the screen) is unaffected.
- `internal/overlay/resize_lag_test.go:20-35` pins the pill/strip sizes
  ("~329x56 … 1280x91"); `internal/overlay/scenebounds_test.go:17-53` and
  `internal/overlay/preview_fit_test.go` pin scene geometry. All need updated
  numbers or new stacked cases.

### 7.6 A failed cycle inside the stack

A cycle that fails shows its error pill **alongside** the other rows rather than
instead of them, for the standard `errorDuration`; when that expires the stack
re-renders. History records the failure as it does today, and a failed cycle
still advances the ordered dispatcher's `next` ([§5.1](#51-ordering-rule)), so
one silent dictation cannot wedge the queue.

---

## 8. Risks

**Risks**

1. **Output out-of-order (recoverable only via history).** Mitigated by the
   ordered dispatcher ([§5.1](#51-ordering-rule)), but only if *every* completion path goes through
   it. A missed `EventTranscribeDone` site (`daemon.go:1009, 1114, 1146`) types
   out of order. Highest-severity risk.
2. **Recorder start/stop interleave.** `ParecRecorder` errors on double start
   (`internal/audio/audio.go:128`) and could stop the *new* recording. The
   hand-off in [§5.4](#54-the-recorder-single-instance-hand-off) is the only clean fix; a naive implementation will produce
   intermittent "capture already in progress" errors or truncated WAVs.
3. **Recognizer reuse while a stream is still finishing.** The companion and the
   main streaming model are single-stream. `TestFinalNewRecordingHasIndependentMainState`
   and `TestTheNextRecordingWaitsForTheDrain` exist because getting this wrong
   corrupts audio or text. Stacking moves when the wait happens, not whether it
   exists; missing it crashes or silently merges two recordings.
4. **Fixed-surface height.** If `FixedSurfaceSize` does not reserve rows, the
   second pill is clipped or, worse, triggers the "scene wants a size the fixed
   surface does not have" warning (`overlay_wl.go:550-554`) and nothing appears.
   The reservation is invisible on single dictation (transparent rows).
5. **Disk/memory growth.** N pending WAVs and N preview buffers. Bounded by the
   pending cap; unbounded is a resource leak.
6. **Shutdown.** `wg.Wait()` (`daemon.go:326`) now waits for N transcriptions and
   a dispatcher; a wedged decode delays `wl-copy` cleanup and process exit. The
   existing "join before delete" rules apply per cycle.
7. **Status/CLI compatibility.** Changing `ipc.Response.State` or stdout breaks
   keybinds and scripts; the design deliberately keeps the coarse strings.
8. **Live-final modes.** `streaming`/`segments` already contend for the main
   recognizer during capture; stacking interacts badly. Ruled: stacking ships
   for `after-stop` only, and the live-final modes keep today's behavior.

## Open questions for the human

None. Every question has been ruled and compacted into the
[Decision Ledger](#decision-ledger) below. The ledger's `Built` column records
what shipped.

### Decision Ledger

| ID | Ruling / Decision | Date | Settled in | Built |
| :--- | :--- | :--- | :--- | :--- |
| OQ-1 | Finished transcripts are typed in spoken order — strictly increasing cycle id — never in completion order | 2026-10-09 | [§5.1](#51-ordering-rule) | yes |
| OQ-2 | Three visible rows (one recording, two transcribing); pending cap of 8, collapsed to a count beyond that; named constants, not config keys | 2026-10-09 | [§5.6](#56-third-dictation-while-two-are-transcribing) | yes |
| OQ-3 | Stacking ships for `after-stop` only; `streaming` and `segments` keep today's behavior | 2026-10-09 | [§8](#8-risks) | yes |
| OQ-4 | `mavor status` reports the stack itself, alongside the derived state; there is no stdout compatibility constraint to preserve | 2026-10-09 | [§6](#6-cli--ipc-surface) | yes |
| OQ-5 | Duck while a capture is active and restore when it stops, so media returns on key release even while typing is still queued | 2026-10-09 | [§5.2](#52-background-audio-ducking) | yes |
| OQ-6 | The second recording's live preview may start late or not at all; capture is never delayed for it | 2026-10-09 | [§5.5](#55-the-preview-companion) | yes |
| OQ-7 | Failure is per-cycle: its error pill shows alongside the recording and transcribing rows for `errorDuration`, then the stack re-renders, and history records the failure | 2026-10-09 | [§7.6](#76-a-failed-cycle-inside-the-stack) | yes |
| OQ-8 | No cancel verb; out of scope, tracked separately | 2026-10-09 | [§8](#8-risks) | no |
| OQ-9 | Push-to-talk unchanged: key-down starts, key-up stops, and `stop` while transcribing stays a no-op | 2026-10-09 | [§6](#6-cli--ipc-surface) | yes |
