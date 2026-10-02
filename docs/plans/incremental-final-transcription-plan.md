---
status: accepted
stage: BUILT
next: "Graduate lifecycle documentation; retain broader quality verification gaps"
depends-on: [../design/incremental-final-transcription.md]
---

# Plan: incremental final transcription

Written against `779917b`, 2026-10-01. Components and paced runner are implemented
and independently reviewed; [measured feasibility and quality limits](../reports/incremental-final-prototype.md)
are now recorded. The
[design](../design/incremental-final-transcription.md) wins on behavior, the tree
wins on fact, this plan is advice and the first thing to be wrong. Binding API
signatures and exact serial ownership are frozen in the durable workflow contract.

## Map and ownership

| Paths | Owner / change |
| :--- | :--- |
| `internal/models/final.go` (new), catalog comments and tests | Catalog: modes, capability derivation and selection validation. |
| `cmd/mavor/models_cmd.go`, listing tests | Catalog: three truthful views, temporary default-mode plumbing seam. |
| `docs/choosing-a-model.md` | Catalog first; evidence owner reconciles measurements after runtime. |
| `internal/config/config.go`, tests | Runtime: final_mode enum, default/resolve/load validation. |
| `internal/speech/final_session.go` (new), tests | Runtime: bounded work, segmentation, coverage reconciliation, complete replay. |
| `internal/speech/streaming.go`, Sherpa/cgo and chunking wrappers/tests | Runtime: abort/cleanup and actual native eligibility; retain warmup and GPU recovery. |
| `internal/speech/factory.go`, companion selection/tests | Runtime: model-mode/layout and unsafe preview validation. |
| `internal/daemon/daemon.go`, new final tests and preview suites | Runtime: one reader, partial sharing, cycle cancellation and final dispatch. |
| `cmd/mavor/main.go`, config scaffold/show and doctor/tests | Runtime: startup/config wiring and truthful chosen mode. |
| `cmd/mavor-bench/` paced runner and tests (new) | Evidence: production session lifecycle with independent paced producer. |
| `test/integration/` or tagged e2e suite | Evidence: opted-in recording/release pipeline. |
| README, configuration design and architecture reference | Evidence: qualify old “always Transcribe after stop” statements. |

**Constraint:** Catalog goes first and must compile without new config fields.
Its rendering API accepts an explicit optional final mode for tests; its
configuredFinalMode helper initially returns after-stop. Integration, after
runtime handoff, owns the one helper-body edit reading the new validated field.
This seam is now integrated and permanently tested from loaded TOML in all
three listing views. Built CLI output was also inspected against mounted models.

## Reuse before writing

- **Advice:** Reuse `ReadWAVAudio`, `WAVDataOffset`, `WriteWAV` and existing private
  temporary-file cleanup shapes; avoid adding an audio format parser.
- **Constraint:** `ParecRecorder.Stop` clears reader state; reconcile the final
  WAV by the final session's counted prefix after stopping/joining the reader.
- **Advice:** Use `newTestDaemon`, `pcmChunk`, `quietRecording`, held stream tests
  and mock output/history; they already exercise real IPC/state transitions.
- **Constraint:** Preserve `feedTailPadding` and online InputFinished/ready-decode
  sequencing. Preserve wrapper Start/Close and recovery from commit `649aeb6`.
- **Advice:** Use deterministic held channels for race/order tests; sleeps are
  weak evidence when native work can exceed fixture timing.
- **Constraint:** Do not reuse `StitchOverlappingTexts` for nonoverlapping segments.
  Text suffix matching cannot establish that repeated words refer to same audio.
- **Advice:** Keep sample-based counters in the session shared by benchmark and
  production; ticker counts mismeasure large accumulated reads.

## Build order and proving commands

1. Catalog API/listing parity and capability tests, then catalog handoff.
   Prove with `go test ./internal/models ./cmd/mavor` using cgo environment below.
2. Runtime red regressions and new session tests; implement queue/drain/replay;
   then config/factory/daemon wiring. Prove with
   `go test ./internal/config ./internal/speech ./internal/daemon ./cmd/mavor`.
3. Integrate selected-mode CLI seam; add paced runner arithmetic/producer tests
   and run both mounted candidates. Prove with `go test ./cmd/mavor-bench` and
   retained actual transcript/timing evidence in durable storage.
4. Read-only correctness and methodology review; final repair fixes all ordinary
   gate failures, runs race/tagged/local gates, formats and commits once.

```bash
libdir="$(dirname "$(scripts/sherpa-libs.sh | head -1)")"
export CGO_ENABLED=1 LD_LIBRARY_PATH="$libdir${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
just check
# Additional applicable race/integration/e2e runs precede landing.
just done
```

## Ships with

- Tests: exact final ownership, retained original replay, tail/odd-byte coverage,
  zero/quiet/short/repeated speech, bounded overload and unsafe split, lifecycle
  errors, cancellation, next-cycle isolation, wrapper cleanup, config/schema and
  listing parity. Integration must catch a consuming preview stealing main audio.
- Existing default-mode discarded-companion and enabled-energy tests remain.
  Opted-in tests assert finalized main text and no full Transcribe on success;
  do not rewrite default-mode tests into preview promotion.
- Fixture references: [real speech](../../test/fixtures/real_speech.wav.txt) and
  [technical prompt](../../test/fixtures/llm_prompt_68s.wav.txt), including intentional
  repetition. Compare accuracy, not only nonempty output or runtime.
- Model loading is separate from release-to-final, replay and dispatch timing.
  Missing candidates are named; generated historical reports remain untouched.
- Cheap and delegated: worker queue container, internal helper decomposition,
  log wording and private temp filename scheme, within the frozen API/behavior.

## Don't / blockers

No changes to defaults, GPU readiness, host deployment, credentials, GNOME tree
or generated reports. No broad offline eligibility or model-identity guessing.
Mounted candidates and both cgo libraries were found; successful loading and
paced execution are recorded in the prototype report. No unresolved user ruling blocks coding.

The existing roadmap was reviewed for preview and benchmark claims; this phase
does not edit it because ownership is restricted to the four requested artifacts.
The existing roadmap links this work for documentation graduation without
duplicating source state. Documentation checker 0.7.1 lacks planning/index checks; disclose
that limitation rather than weakening document metadata.
