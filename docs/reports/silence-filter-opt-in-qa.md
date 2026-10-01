---
status: accepted
stage: CURRENT
---

# Optional silence filter QA evidence

Verified 2026-10-01 in the detached silence-filter-opt-in worktree based on
`a2ba579`. No commit created in the Implement phase.

The implementation-phase results below are retained as that phase's record;
this reviewer did not independently observe the pre-fix failures or rerun its
full gates. Independently observed checks are recorded at the end.

## Before implementation

- `go test ./internal/daemon -run TestSilenceFilterDefaultOff -count=1` failed:
  six quiet/silent/header-only cases skipped final transcription; the held-tail
  case timed out waiting for the final model while preview evidence was pending.
- `go test ./internal/config -run TestSilenceFilterConfig -count=1` failed to
  compile because the new configuration field did not yet exist.
- `go test ./cmd/mavor -run TestTemplateExamplesStateTheDefaults -count=1`
  failed because the scaffold did not document `silence_filter = false`.

## After implementation

- Targeted suites passed, uncached:
  `go test ./internal/config ./internal/daemon ./cmd/mavor ./internal/audio ./internal/speech -count=1`.
- `just check` passed: formatting, vet, staticcheck, tagged-suite type checking,
  and the full unit suite. This includes existing preview, latency, audio
  detection, and chunking tests.
- `just done` passed after `just format`: "Quality gate passed. Ready to
  commit." Its first attempt caught an unformatted final comment edit; formatting
  fixed it, and the complete gate was rerun successfully.
- `git diff --check` passed.
- Vantage checker 0.7.1 passed all eight changed Markdown files. Its style guide
  was read before writing; 0.8 planning-index checks are unavailable in that
  version and were not claimed.
- Existing preview-evidence tests now explicitly enable rejection: partial,
  phrase, tail-only, stale/canceled, annotation, and reset cases remain green.
- New tests show default-off final routing without preview or recognized words,
  no held-tail wait, enabled rejection without preview, detection errors allowing
  inference, and empty/annotated final text neither typed nor recorded.
- Startup plumbing and the `silence_filter` log attribute were inspected in the
  diff; no live daemon startup was performed.

## Limits and a corrected fixture

An initial nine-byte malformed-file fixture did not raise a detection error:
files shorter than 44 bytes intentionally count as no speech. The fixture was
corrected to 64 malformed bytes and the targeted suites then passed. Production
error behavior was not changed.

No real-model silence accuracy or hallucination rate was measured. No live audio,
Wayland integration runtime, or real-model end-to-end suite was run. Tagged suites
were type-checked. The implementation phase deferred landing to final verification.

## Independent review

Reviewed the actual uncommitted production, test, and documentation changes on
2026-10-01. HEAD remains `a2ba579`. Only this report was edited during review;
no production code, tests, branches, or commits were changed.

**Blocking findings: none.** Review identified one nonblocking documentation inconsistency:
[the troubleshooting row](../user-guide.md#L916) attributes missing quiet phrases
to the loudness check without qualifying that it now requires
`advanced.silence_filter = true`. Qualify that cause and recommend disabling the
filter before suggesting preview installation or input-gain changes.

### Independently observed evidence

- The configuration boolean defaults to false and is copied into the daemon.
  The sole final-rejection guard bypasses both `DetectSpeech` and the
  preview-evidence wait when false. Recorder errors and final annotation/empty
  text handling remain intact; this is not a promise that every capture succeeds.
- Enabled behavior retains the existing energy and recognition logic: valid
  partial/phrase/tail evidence overrides rejection, stale or annotated evidence
  does not, and detection errors proceed to inference. Preview evidence resets
  per recording. Tail decoding remains asynchronous, and restarting the preview
  still waits for the previous decode to finish before reusing its recognizer.
- The loader begins with defaults, so existing current-schema files lacking the
  new key need no rewrite and now disable rejection. Pre-rewrite unknown keys
  retain the existing stale-schema diagnostic, not a new migration path.
- Tests exercise real daemon transitions, socket commands, generated WAV files,
  energy checks, configuration parsing, scaffold parsing, and history reads.
  Capture, transcription, preview recognition, and output are mocks. Held-tail
  tests verify ordering, not actual model decoding or accuracy.
- Schema recognition, omitted/explicit values, marshaled round trips, scaffold
  defaults, and uncommented examples pass. Startup wiring and the effective log
  attribute were inspected, not exercised through a live daemon startup.

Commands run successfully in this review:

```bash
go test ./internal/config ./internal/daemon ./cmd/mavor ./internal/audio ./internal/speech -count=1
go test -race ./internal/daemon -run 'Test(SilenceFilter|EnabledSilenceFilter|QuietRecordingWithPreview|PreviewSpeechEvidence|QuietPreviewDoesNotWait|DefaultOffQuiet)' -count=1
git -C /workspace/.yolo/durable/worktrees/silence-filter-opt-in diff --check
```

The targeted race run passed. No microphone, real model, live Wayland session,
or end-to-end transcription was tested. No silence-accuracy inference follows
from these routing tests. The full `just check` and `just done` results above
remain implementation-phase evidence, not independent reruns.

The Vantage 0.7.1 style guide was read and its checker passed this updated report;
0.8 planning checks are unavailable and were not claimed.


## Final verification and landing

The troubleshooting row now limits loudness rejection to
`advanced.silence_filter = true` and recommends disabling it and restarting
before suggesting preview installation or input-gain changes. Final diff review
found no blocking issues; no production changes were needed in this phase.

Observed in the final phase:

- `just format` and full `just check` passed (the unit suite reused cached results).
- `go test -race ./internal/daemon -count=1` passed the entire daemon suite,
  uncached, not only the targeted review cases.
- Vantage 0.7.1 passed all eight edited Markdown files after the final edits.
  Its style guide was read; 0.8 planning-index validation remains unavailable.
- `git diff --check` passed and `just done` reported
  "Quality gate passed. Ready to commit."

This report and the completed task checklist accompany the single landing
commit in the detached worktree. Parent workflow integration is separate;
no branch, tag, publishing, or main-checkout modification is part of this phase.
No live daemon startup, microphone, Wayland runtime, or real-model silence
verification was performed. These tests establish routing and concurrency
behavior, not model accuracy.
