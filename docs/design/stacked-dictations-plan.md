---
status: draft
stage: SKETCH
depends-on: [stacked-dictations.md#OQ-1, stacked-dictations.md#OQ-2, stacked-dictations.md#OQ-3, stacked-dictations.md#OQ-4, stacked-dictations.md#OQ-5, stacked-dictations.md#OQ-6, stacked-dictations.md#OQ-7, stacked-dictations.md#OQ-8, stacked-dictations.md#OQ-9]
---

# Stacked dictations — implementation sketch

**Status:** 2026-10-09 — incomplete. The design is ruled (all nine questions
settled), but this is still a parking lot, not a plan: it has to be completed
against the tree before anyone builds from it.

> [!IMPORTANT]
> This is a **sketch**, not a plan. It parks material that surfaced while
> designing [stacked dictations](./stacked-dictations.md) and that the design
> does not need to carry: the build order, the test-inversion list, and the traps
> worth keeping. The design is ruled, but that does not make this a plan — an
> agent must not build from it while it is stamped `SKETCH`.
> [The design](./stacked-dictations.md) wins on behavior wherever the two
> disagree.

**Reads with:** [`stacked-dictations.md`](./stacked-dictations.md) — the design
this serves, now ruled in full.

---

## Tests that must be inverted rather than deleted

These encode the behavior being changed and must be rewritten **in the same
commit** (the project rule: new/changed behavior ships with a test).

**`internal/state/state_test.go`**

- `TestApplyTransitions` — the table cases `"toggle in transcribing is a no-op"`,
  `"start in transcribing is a no-op"`, `"stop in transcribing is a no-op"`
  (`state_test.go:22-25`) directly assert the reversal and must become `→ Recording`.
- `TestSubscribeReceivesTransitions` (`:59-81`) asserts the notification
  sequence `Recording, Transcribing, Idle` after `Toggle, Toggle, Toggle,
  TranscribeDone`; the third toggle now starts a recording, so the sequence
  changes.
- `TestNewIsIdle`, `TestUnsubscribeStopsNotifications`,
  `TestConcurrentApplyIsRaceFree` (`:9-13`, `:83-96`, `:98-107`) should survive,
  but `TestConcurrentApplyIsRaceFree` should gain concurrent
  `RecordStart`/`RecordStop`/`Finish(id)` to cover the new state.

**`internal/daemon/daemon_test.go`**

- `TestToggleDrivesFullPipeline` (`:86`) and `TestStartIdempotency` (`:431`,
  esp. `:471-477` "start while transcribing is a no-op") and
  `TestStopIdempotency` (`:492`, esp. `:535-541`) — all assert the no-op.
- `TestStartAndStopTransitions` (`:393`), `TestOverlayTracksStateChanges`
  (`:215`, asserts `[Recording, Transcribing, Hidden]`), `TestStatusActionExposesState`
  (`:201`), `TestConcurrentTogglesWaitForRecorderStart` (`:129`).
- `TestDuckingOnRecordingAndRestoreOnIdle` (`:295`) — restore-count assertion
  changes per [§5.2](./stacked-dictations.md#52-background-audio-ducking). `TestDuckingRestoresOnTranscribeFailure` (`:335`) and
  `TestDuckingRestoresOnEnteringTranscribing` (`:716`) survive.
- `TestLevelMonitoringDuringRecording` (`:357`), `TestTranscriptIsRecordedToHistory`
  (`:740`), `TestTranscriptRecordedEvenWhenOutputFails` (`:766`),
  `TestHistoryRecordsTheTextThatWasTyped` (`:864`), the non-speech tests
  (`:795`, `:830`), `TestEmptyTranscriptionSkipsOutput` (`:250`) — survive, but
  gain a stacked sibling.
- `TestTranscribeFailureReturnsToIdle` (`:237`),
  `TestTranscribeFailureShowsErrorOverlay` (`:268`) — must verify a failure in
  cycle 1 still lets cycle 2 finish and returns to `Idle` only at the end.

**`internal/daemon/initialization_test.go`** — `TestInitializingServesControlsWithoutQueuing`
(`:21`), `TestInitializationNoticeIsDemandOnly` (`:148`),
`TestInitializationRequestCannotOverwriteCompletion` (`:230`) rely on
`Initializing` being the only non-ready phase; they should survive unchanged,
which is a good regression signal that stacking did not leak into init.

**`internal/daemon/upgrade_test.go`** — `TestUpgradeWatchWaitsForIdle` (`:82`)
now waits for *all* transcriptions, not one. Worth a stacked variant.

**Preview / final / silence tests**

- `preview_latency_test.go:162` `TestTheNextRecordingWaitsForTheDrain` — the
  drain now gates preview, not capture; update or split.
- `final_session_test.go:258` `TestFinalNewRecordingHasIndependentMainState`,
  and `final_cycle.go`'s reuse guard.
- `silence_preview_test.go:134` `TestPreviewSpeechEvidenceDoesNotLeakToNextRecording`
  and `:178` `TestQuietPreviewDoesNotWaitForTailWhenWordsAlreadyRecognized` —
  the `hadSpeech`/drain fields they exercise become per-cycle; assert no leakage
  across *concurrent* cycles specifically.
- `preview_test.go:188` `TestPhraseResultArrivingAfterStopIsDropped` and `:227`
  `TestStreamPartialArrivingAfterStopIsDropped` — must now drop by cycle id, not
  a global generation, and must prove cycle 2's partials still paint while
  cycle 1's late partials are dropped.

**Media/compositor tests**

- `internal/overlay/scenebounds_test.go`, `paint_test.go`,
  `resize_lag_test.go`, `preview_fit_test.go`, `overlay_test.go`,
  `internal/overlay/mock.go` callers — geometry and mock expectations for the
  new stack.
- `test/integration/preview_overlay_test.go` `overlayBand` band arithmetic.

A grep for "no-op"/"transcribing" in tests is the checklist:
`rg -n "no-op|transcribing" internal/state internal/daemon --glob '*_test.go'`.

---


---

## Build order


1. **Machine: cycle identity + snapshot, no behavior change.** Add
   `Snapshot`, `RecordStart`/`RecordStop`/`Finish`, derived `State`, and keep
   `Apply` delegating. Update `TestApplyTransitions` to the new table but keep
   toggle-while-transcribing a no-op **for this step only**, so the machine
   change is proven inert. Verify: `go test ./internal/state/...`.
2. **Machine: allow start while transcribing.** Flip the toggle/start edge and
   rewrite the three no-op table cases and `TestSubscribeReceivesTransitions`.
   Verify: `go test ./internal/state/...` (daemon tests will now fail — expected,
   fixed in the next steps; do not land this without at least a coincident
   daemon fix, or land 1–4 as one commit).
3. **Daemon: `dictationCycle` extraction.** Move `streamGen`/`streamHistory`/
   `streamHadSpeech`/`streamSource`/`streamUsesMain`/`streamDrain`/
   `streamFeedDone`/`finalCycle`/`backup`/provenance into the cycle, and
   `cycleID` becomes the cycle's id. Behavior unchanged; all existing daemon
   tests green. Verify: `go test ./internal/daemon/...`.
4. **Daemon: recorder hand-off.** Stop capture on the recording→transcribing
   edge and store `cycle.wav` before spawning decode; drop the `Idle` restore.
   Update ducking tests. Verify: `go test ./internal/daemon/...`.
5. **Daemon: pending list + single decode worker.** `recording`/`pending` on the
   daemon; `RecordStart` while transcribing appends nothing but starts a cycle;
   `RecordStop` appends; a FIFO decode worker drains `pending`. `Idle` only when
   both empty. Add the "third dictation" test and the pending-cap refusal test.
   Verify: `go test ./internal/daemon/...`.
6. **Daemon: ordered dispatcher.** `internal/daemon/dispatch.go`; move `Emit` +
   `history.Append` there; advance `next` on empty/failed cycles. Add a test
   where cycle 2 finishes before cycle 1 and asserts emit order `1,2`. Verify:
   `go test ./internal/daemon/...`.
7. **Overlay `Scene.Stack` + `ShowStack` + fixed-surface height.** Painter
   first (`paint.go`, constants), then `FixedSurfaceSize`, then Noop/Mock, then
   wl and X11. Update geometry/bounds tests. Verify:
   `go test ./internal/overlay/...`.
8. **Daemon→overlay wiring.** Derive the stack from `Snapshot` and call
   `ShowStack`. Add a daemon test asserting two pills during
   record-over-transcribe. Verify: `go test ./internal/daemon/... ./internal/overlay/...`.
9. **IPC + `mavor status`.** Extend `ipc.Response`, `writeStatus` diagnostics,
   `--json`. Verify `cmd/mavor/status_policy_test.go` plus a new stacked case.
10. **Reference doc + storybook.** Update
    `docs/reference/how-mavor-works.md` (I4, the transition table, the "toggling
    while transcribing" warning, and the
    [ruling ledger](../reference/how-mavor-works.md#why-its-this-way))
    and add stacked storybook scenes; rerun
    `just storybook`. Verify: `just check` and the report render.
11. **Integration.** Update `preview_overlay_test.go` band arithmetic and add a
    real-compositor stacked check. Verify: `just test-int`.

---

