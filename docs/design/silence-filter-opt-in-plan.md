---
status: accepted
stage: BUILT
next: "Parent workflow integrates and graduates the verified plan"
---

# Plan for the optional silence filter

**Status:** 2026-10-01. Written against `a2ba579`; implementation and local gates
complete; final review found no blocking issues. This plan accompanies the
single landing commit. UNMEASURED: no real-model silence behavior was tested.

[The design](silence-filter-opt-in.md) wins on behavior; the tree wins on facts;
this plan is implementation advice.

## Map

| Paths | Change |
| :--- | :--- |
| `internal/config/config.go`, `config_test.go` | Boolean default, schema recognition, missing/omitted/explicit values, show-format round trips |
| `cmd/mavor/config_cmd.go`, `config_cmd_test.go` | Interpolate and document the default in the scaffold |
| `cmd/mavor/main.go` | Pass the setting to the daemon and log its effective value |
| `internal/daemon/daemon.go`, `silence_preview_test.go` | Guard only final rejection; preserve enabled tests and add disabled routing coverage |
| `docs/user-guide.md`, `docs/reference/how-mavor-works.md`, `docs/design/configuration-surface.md` | Qualify rejection behavior and update examples |
| Five new silence-filter-opt-in stage documents | Research, design, plan, tasks, QA evidence |

## Reuse and constraints

- Reuse `quietRecording`, `newTestDaemon`, `timedTranscriber`, and
  `heldPreviewTail` for deterministic routing and ordering assertions.
- Do not enable filtering globally in `newTestDaemon`; only preservation tests
  opt in. `SilenceThreshold` still controls preview phrase pauses.
- A malformed WAV must be at least header-sized to provoke a detection error:
  smaller files intentionally return no speech without an error.

## Sequence and verification

1. Observe default-off routing and scaffold failures before implementation.
2. Add minimal plumbing and the single guard; run targeted package tests.
3. Update documentation and stage evidence; run `just check` and `just done`.
4. Final phase: fix review findings, rerun full gates and daemon race tests,
   validate all edited Markdown, and land one coherent commit.

See [the tasks](../planning/silence-filter-opt-in-tasks.md) and
[QA evidence](../reports/silence-filter-opt-in-qa.md).
