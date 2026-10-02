---
status: accepted
stage: BUILT
next: "Parent integrates detached commit; retain broader quality and dispatch gaps"
depends-on: [../plans/incremental-final-transcription-plan.md]
---

# Serial incremental final prototype tasks

2026-10-01, base `779917b`. The catalog and runtime components are implemented; paced real-model evidence is recorded in the [prototype report](../reports/incremental-final-prototype.md). Behavior is in the
[design](../design/incremental-final-transcription.md); concrete API signatures
and exclusive ownership are in the durable workflow contract. No child agents
or commits before final repair.

- [x] Catalog: implement final capability/mode helper/types; regression tests
  for all plain/verbose/JSON rows, eligibility, default and selected rendering.
  Initial guide describes opt-in target, not measured recommendations.
  Handoff records real symbols and the pending selected-config CLI seam.
- [x] Runtime: first observe red regressions for concrete lifecycle bugs; add
  permanent tests for independent final ownership, one reader, final tail,
  queue/segment bounds, repeat preservation, failure replay, cancel/new-cycle
  isolation and resource cleanup. Implement config/validated startup, native
  final session, offline-v2 segments, preview sharing, and one final dispatch. Observed the legacy full-replay and feed/stop race
  regressions fail before repair; focused tests, race suites and `just check`
  pass. No real-model latency or quality conclusion is implied.
- [x] Integration/evidence: complete CLI configuredFinalMode helper, validate
  all renderings with the actual new config key, add permanent paced runner
  exercising the same production session and an independent producer.
- [x] Evidence: load mounted Nemotron 560ms and Parakeet v2 without downloading;
  warm load separate from recognition. Run both real fixtures in requested mode
  and each warm after-stop baseline; retain transcripts, reference error rates,
  coverage, backlog/replay, release/finalize times and load context. Dispatch is
  intentionally excluded from the no-output model measurement.
  Missing or failed requested runs remain explicit incomplete evidence.
- [x] Reviews: parallel read-only audio/lifecycle and metadata/measurement audits;
  no source writes. Check loss, last word, repetitions, stop/abort races, preview
  nonpromotion and claims of measured speed/quality.
- [x] Final repair: fix in-scope findings, reconcile guide/reference/README and
  planning artifacts with actual behavior and evidence; link existing roadmap.
  Keep generated reports and the GNOME tree untouched.
- [x] Landing: `just format`, `just check`, applicable race/integration/e2e and
  document checks, then one coherent conventional commit; `just done` and clean
  detached tree handoff. No branch/push/PR/release or deployment.

All logs, scratch and model evidence belong in the durable incremental-final
workflow directory. An ordinary compiler/linter/test failure is a repair queue,
not a reason to pause for a human decision.
