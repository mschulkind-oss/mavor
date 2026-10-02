---
status: accepted
stage: BUILT
next: "Host installation and physical GPU acceptance require separate authorization"
---

# Initialization must be visible; backup must be final

**Status:** 2026-10-02. Source inspected at `dbb44ff`; implementation and isolated desktop measurements are recorded in [QA](../qa/visible-model-initialization-qa.md).

> **In short.** Readiness includes a real decode, not an open socket. A failed
> GPU-enabled server request may use the already-running companion, but only
> its finalized, completely covered result from that recording.

**Needs your ruling:** None; the user approved the exception and bounded readiness.
**Reads with:** [research](../research/visible-model-initialization.md),
[implementation plan](../plans/visible-model-initialization-plan.md).

## Startup ownership and visibility

- One daemon owns its listener, state subscription, initialization worker,
  loaded models, overlay, and shutdown. No temporary listener or substitute model.
- Show **initializing** before resolving/building models, including synchronous
  factories and companion loading; bind IPC before executing expensive work.
  Status and all three hotkey actions immediately return `initializing` without
  recording, ducking, output, history writes, or queued actions.
- Initialization completes only after configured main and loaded companion have
  passed actual inference. Main failure is fatal; preserve existing optional
  companion degradation to phrase mode, log why backup is unavailable, and close
  the failed companion. A named missing companion remains fatal.
- Use bounded defaults: GPU child connection readiness 120 seconds, total
  initialization 180 seconds, each inference 30 seconds. Explicit test deadlines
  override defaults; CPU opt-in policy from `dbb44ff` is preserved.
- Decode a two-second, deterministic, locally generated voiced fixture at
  16 kHz, mono, signed 16-bit PCM: a 120 Hz fundamental with 720 Hz and 1200 Hz harmonics and
  short quiet margins. It is controlled synthetic audio, not private speech or
  a recognition-accuracy benchmark. Run normal decoding/finalization, discard
  text, reset/abort streams, delete WAV/sidecars; do not check semantic equality.
  Do not reuse unattributed recordings or add a runtime synthesizer dependency.
- Probe the configured mode: streaming main and companion each start/feed/finalize
  an isolated stream; offline main performs normal `Transcribe`. No health-only
  readiness. Keep vocabulary settings; no transcript cache or history survives.
- Success becomes ready `idle` and hides initialization. Failure shows production
  Error with actionable diagnostics for 1.5 seconds by default (cancellation may dismiss),
  returns the original wrapped cause, cancels and joins owned work, closes models
  once, removes only its own socket, and reaps children. No ready-after-cancel race.
- Native model construction/decode is not preemptible inside a C call. A deadline
  invalidates readiness and suppresses output; resource release must wait for the
  call to return rather than closing native memory underneath it. Do not claim a
  hard process-exit bound for a wedged native library.

## The qualified exception

- Normal main success remains authoritative, including successful empty/non-speech
  results. Ordinary partials, main phrase previews, and main stream partials never
  emit. Default `after-stop`, opt-in `streaming`/`segments`, and
  `silence_filter = false` remain unchanged.
- Eligibility is a typed failure of an actual supervised local GPU-enabled
  **main request**, not evidence of hardware OOM. Exclude startup, readiness probe,
  canceled/deadline contexts, invalid WAV/request, unsupported mode, HTTP 4xx,
  remote, explicit CPU, non-GPU, and companion failures. Do not classify by strings.
- The request's GPU eligibility is captured when it starts, not reconstructed
  from mutable device flags afterward. Preserve CPU opt-in behavior; a failed
  CPU retry must not masquerade as an eligible GPU request.
- Show Error and retain the original diagnostic for at least 1.5 seconds before
  switching to backup (cancellation may dismiss); do not enqueue two visuals
  back-to-back into a latest-state renderer that could skip the error frame.
  No new model is loaded, and this backup path never replays the main on CPU.
- Backup requires a distinct loaded streaming companion, matching recording ID,
  successful finalization, nonempty cleaned final text, and proven full coverage.
  No companion, empty text, lost audio, feed/finalize failure, timeout, canceled
  recording, or stale result retains the original main error and emits nothing.

## Coverage, completion, and one dispatch

- One reader advances the recorder PCM offset; fan out copied chunks from it.
  The companion is independently bounded and cannot block the main consumer.
  Queue overflow or any skipped/failed chunk permanently invalidates that backup.
- Track the exact accepted live prefix (byte count and digest). After recorder
  stop, reconcile against the finalized WAV, reject mismatched/overlong/unaligned
  prefixes, then feed only its remaining tail to the same stream. No gap repair,
  second PCM reader, fresh stream, or provisional-text concatenation.
- Drain accepted audio and call `StopStream` under a five-second total backup
  completion deadline. Keep result generation separate from the HUD generation
  invalidated at stop. Next recording cannot reuse a recognizer until completion
  or safe abort has joined; cancellation invalidates the result immediately.
- Finalized text passes the same non-speech stripping and output cleanup as main.
  Select exactly one whole result before history or output. Discard partial main
  successes when a later segment fails; never append companion text to main text.
- History retains actual source (`main` or `companion-backup`), model, and warning
  as optional fields, preserving old JSONL rows. Status retains last source and
  warning until the next recording, including after ordinary Idle hiding.
- Show **degraded** labeled `BACKUP TRANSCRIPT` with the GPU request warning through
  output and for at least three seconds after completion. Idle does not immediately
  erase it; a fresh recording clears it, and stale hide timers cannot hide new work.

## Desktop evidence and non-goals

Both Sway and GNOME must capture production initializing → ready and initializing
→ error, plus degraded backup. Shared catalog counts derive from the catalog.
Add genuine timed quiet → varying speech → pause → recovery compositor frames:
verify older bars shift left and pause decays to baseline. Preserve quiet static
calibration, strict pixel comparison, production logarithmic scaling, wallpaper
SHA-256 `9419801e0c39a0db869c21a7ac4dec828ede0c4a18699d7f8530092a78074610`.
Label scenes controlled fixtures, not real microphone/model evidence; movies use
only real captured frames. Receipts prove submission, not compositor presentation.

No host installation/restart, remote CI rerun, unbounded waits, fake progress,
claimed shader warm-up/OOM proof, waveform rescaling, or GNOME focus-safety changes.

## Alternatives and risks

| Choice | Verdict / cost |
| :--- | :--- |
| Temporary startup socket | Rejected: creates two lifetime owners and handover races |
| Health request alone | Rejected: never exercises inference allocations |
| Existing preview final string alone | Rejected: discarded tail and lossy queue cannot prove coverage |
| Complete same-stream coverage | Accepted: costs bounded bookkeeping and a stop-tail drain |
| Automatic new CPU main | Rejected by default; existing explicit opt-in remains |
| Native hard cancellation | Not promised: retain ownership until C calls return |
