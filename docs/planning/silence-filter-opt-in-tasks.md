---
status: accepted
stage: BUILT
next: "Parent workflow integrates the detached-worktree landing commit"
---

# Optional silence filter implementation tasks

**Status:** 2026-10-01. Completed against `a2ba579` in the detached worktree;
this checklist accompanies the single landing commit. UNMEASURED: real-model
silence behavior was not tested; mock-backed routing and daemon race tests passed.

- [x] Write default-off regression tests and observe baseline routing failures.
- [x] Add config, schema, scaffold, and show-format round-trip coverage.
- [x] Wire the setting into daemon startup and expose it in the startup log.
- [x] Guard final rejection only; explicitly opt in all three preservation tests.
- [x] Cover no-preview rejection, energy-check errors, held tails, and empty or
  annotated final output remaining untyped and unrecorded.
- [x] Update user/reference examples and run targeted tests and `just check`.
- [x] Final review: fix the troubleshooting finding, rerun all required gates,
  and include code, tests, and evidence in one conventional landing commit.

[QA evidence](../reports/silence-filter-opt-in-qa.md) records verification limits.
