---
status: accepted
stage: CURRENT
---

# Evidence for optional final-recording rejection

Source inspection and mock-backed routing tests, 2026-10-01, based on `a2ba579`.

- `runTranscription` in [the daemon](../../internal/daemon/daemon.go) previously
  checked every nonempty recording path before final transcription. A failed
  energy check consulted preview recognition, potentially waiting for its tail.
- `LoadFile` in [the configuration loader](../../internal/config/config.go)
  starts from `Default()`. A boolean needs no migration or normalization.
- The new default-off regression tests failed before implementation: six
  quiet/silent/header-only cases did not call the final model; holding a
  companion tail blocked final transcription.
- Older claims about neural silence detection or guaranteed prevention of
  invented text are not evidence for this change. No real-model silence
  accuracy was measured.

See [the QA report](../reports/silence-filter-opt-in-qa.md) for commands and limits.
