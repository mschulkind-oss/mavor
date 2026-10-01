---
status: accepted
stage: BUILT
next: "Parent workflow integrates and graduates the verified design"
---

# Final-recording rejection is a user choice

**Status:** 2026-10-01. Verified against `a2ba579` plus the changes in this
landing commit. UNMEASURED: no real-model silence behavior was tested;
mock-backed routing and daemon race tests passed.

> **In short.** Captured audio should reach the final model by default.
> Rejection based on recording energy and preview recognition is opt-in.

**Needs your ruling:** None.

**Reads with:** [the implementation plan](silence-filter-opt-in-plan.md).

## Contract

- `advanced.silence_filter` is a boolean, default `false`. Missing configuration,
  an omitted key, and explicit `false` have identical behavior.
- Off bypasses the entire final-recording energy check and preview-evidence
  decision, including the wait for tail recognition.
- On preserves baseline behavior: insufficient energy rejects only without
  valid preview recognition. Energy-check errors proceed to transcription.
- Restart the daemon after changing it. No automatic rewrite or alias is needed.

## Boundaries and cost

Recorder errors, preview phrase detection, long-audio chunking, final annotation
stripping, and empty-transcript handling remain independent. Previously rejected
recordings now incur inference; real model results must be observed, not
predicted. No new heuristics or accuracy claims are authorized.
