---
title: "Incremental final transcription QA"
status: accepted
date: 2026-10-02
summary: "Observed regressions, real paced evidence, passing gates and explicit infrastructure/test limits before final review."
---

# Incremental final transcription QA

**Issues first:** The first unpinned Sway run failed because GTK development
metadata was unavailable. The first pinned run then hit the existing paste
PRIMARY restoration timing failure; the unchanged test passed three isolated
repeats and the complete pinned rerun. The audio/clipboard canned test skipped
because no PulseAudio server was reachable. An additional GNOME storybook run
failed on a too-long private D-Bus socket path; a shorter durable artifact prefix
fixed that infrastructure invocation and the existing storybook passed. The first
Markdown check found its generated report missing; after generation all changed
documents passed. The final Sway run explicitly used an
unreachable Pulse endpoint to avoid any host sound-server mutation. No new GNOME
HUD exists in this tree; its acceptance is not claimed.

This records verification of the isolated candidate based on `779917b`.
The durable landing handoff records the actual commit and clean-tree check. The [prototype report](../reports/incremental-final-prototype.md)
owns measured results; the [tasks](../tasks/incremental-final-transcription-tasks.md)
own the completed review/landing checklist.

## Behavior and permanent tests

Runtime regression-first logs observed full after-stop replay winning over the
main finalized stream while preview was off or a distinct companion was used,
and the legacy feed/stop race, before their repairs. Permanent session/daemon
tests cover single audio consumption, unread suffix reconciliation, empty/short/
quiet audio, exact prefix coverage, nonoverlapping repeat retention, queue bounds,
failure replay on the retained full WAV, cancellation, new-cycle isolation,
held-worker draining, native abort and temporary resource cleanup. Default
silence filtering stays false; legacy enabled rejection and GPU/CPU recovery
remain tested. Companion text never becomes final output.

Integration observed the listing configuration seam return `after-stop` for
`advanced.final_mode = "segments"` before repair. Its permanent loaded-TOML test
now verifies plain/verbose/JSON selection without renderer overrides. The
permanent paced producer tests verify sample order/short tail, absolute recording
deadlines, admission overhead, lateness and error-preserving continued coverage.
These clock tests are orchestration proof, not speed proof.

## Real-model evidence

Eight paced rows used production final sessions and factory validation, both
mounted requested models and both reference fixtures. All live rows covered
all samples without replay; only final results were recorded. Nemotron matched
its own after-stop transcript; Parakeet segment quality drift is explicitly
reported, including the technical phrase loss and invented fragment. There was
no model download, microphone, output or clipboard operation in this runner.

Reproduce one model per fresh test process; the output must be an explicit,
writable evidence path. The test appends JSONL rather than overwriting old runs.

```bash
libdir=$(dirname "$(scripts/sherpa-libs.sh | head -1)")
export CGO_ENABLED=1
export LD_LIBRARY_PATH="$libdir:/nix/store/0vqb1mcas5j8dv6bhbrshinlgsg6bvgi-gcc-15.3.0-lib/lib:${LD_LIBRARY_PATH:-}"
MAVOR_PACED_MODELS=/ctx/mavor-models \
MAVOR_PACED_MODEL=nemotron-streaming-en-560ms \
MAVOR_PACED_OUTPUT=/workspace/.yolo/durable/incremental-final/nemotron-paced.jsonl \
go test ./cmd/mavor-bench -run '^TestPacedFinalModels$' -count=1 -v -timeout=10m
# Repeat with parakeet-tdt-0.6b-v2 and a separate output path.
```

The runner times release-to-final, not daemon cleanup/history/dispatch. Warm model
load and session startup are independent fields. Process memory peaks, four ONNX
threads, CPU identity and load snapshots are retained. Shared host work makes
these contended observations; no quiet-host or user-device guarantee is made.
Real noisy/quiet/short/long-tail recognition remains unmeasured, even where
sample-preserving lifecycle behavior is unit-tested.

## Gates and artifacts

All logs reside under the durable incremental-final evidence directory:

| Gate | Evidence / result |
| :--- | :--- |
| Listing seam red then green | `integration-red.log`; focused CLI/bench tests pass. |
| Full unit/format/vet/staticcheck/tagged compile | `integration-check.log`; `just check` passed, including integration/e2e/GNOME typechecks. |
| Full race | `integration-race.log`; `go test -race ./...` passed. |
| Pinned Sway suite | `integration-sway-final.log`; passed with the named audio test skipped. `integration-paste-repeat.log` records three passing retries. |
| Supported GNOME regressions | `integration-gnome.log`; production X11 clipboard acceptance, supervisor cleanup, storybook Python regressions and missing-prerequisite cleanup passed. The existing GNOME storybook also passed with a shorter artifact prefix in `integration-gnome-storybook-short.log`. No GNOME HUD claim. |
| Distribution build | `distribution-origin.log`; executable and both shared libraries built in durable storage. With only the C++ runtime on the library search path, both sherpa libraries resolve beside the executable. `mavor version` ran successfully. |
| Built plain/verbose/JSON | `cli-*-plain.log`, `cli-*-verbose.log`, `cli-*-json.log`; both actual opt-in configs render selected modes and matching catalog eligibility. |
| End-session and documents | `integration-done.log`, `integration-docs.log`; results recorded before evidence handoff. Checker 0.7.1 cannot verify planning/index rules. |

Pinned graphical checks use the existing test-only closure, not jail or host
configuration changes:

```bash
nix-shell test/gnome/environment.nix --run \
  'PULSE_SERVER=unix:/nonexistent/mavor-no-host-audio go test -tags=integration ./test/integration/... -count=1 -timeout=5m -v'
nix-shell test/gnome/environment.nix --run \
  'MAVOR_GNOME_ARTIFACTS=/workspace/.yolo/durable/incremental-final/gnome go test -tags=gnome ./test/gnome/... -run "^(TestGNOMEX11Clipboard|TestHarnessCleanup|TestStorybookRegressions|TestStorybookMissingPrerequisite)$" -count=1 -timeout=3m -v'
```

The ordinary `just build` dependency can download its default Whisper model,
so the equivalent Go build/rpath plus shared-library copy was used instead.
The existing e2e suite was typechecked, not executed with unrelated Whisper
models. Requested real-model recognition was actually executed by the paced
harness above. No deployment or service restart occurred.

## Final repair verification

Independent runtime and metadata/evidence reviewers found no blocking final-audio
or measurement defect. Final repair observed failing regressions before fixing:

- Companions inherited the main final mode, rejecting a valid streaming companion
  when main used segments. Companion configuration now resets final mode independently.
- Explicit phrase preview safely shared main live results but reported separate
  phrase decoding. Plans now report main stream partials or segment aggregates,
  consistently with the design; there is no competing main reader/decoder.
- Companion start/feed failures and full-queue drops were silent. Bounded warnings
  now explain unavailable preview without backpressure or main replay.

Permanent regression output is in `landing-red.log`; focused speech/daemon race
suites pass in `landing-focused.log`. Fresh real-model pairs passed for both
fixtures and candidates; [post-review observations](../reports/incremental-final-prototype.md#post-review-verification-pair)
confirm complete live coverage/no replay and unchanged quality conclusions.

Final post-repair gates actually rerun:

| Gate | Durable log / observed result |
| :--- | :--- |
| Format, full unit/lint/tagged compilation | `landing-format.log`, `landing-check.log`: pass. |
| Full race | `landing-race.log`: pass. |
| Complete pinned Sway | `landing-sway.log`: pass; only canned audio test skips on prohibited/unavailable PulseAudio. |
| Supported GNOME clipboard and existing storybook | `landing-gnome.log`: X11 production clipboard, supervisor cleanup, actual storybook and Python regressions pass. No separate HUD acceptance. |
| Distribution/rpath | `landing-distribution.log`: both sherpa libraries resolve beside the built binary with only Nix C++ runtime on the search path; version runs. |
| Built CLI | `landing-cli-*-{plain,verbose,json}.log`: both actual configured candidates inspected; selected mode and JSON capability assertions pass. |
| Documents/end-session | `landing-docs.log`, `landing-done.log`: landing handoff records final results. Checker 0.7.1 lacks planning/index checks. |

The final verification used the same cgo environment and pinned private graphical
commands above. Real-model comparisons excluded preview and dispatch; permanent
preview regressions, not those timings, verify the preview repairs. The existing
unrelated Whisper e2e suite was typechecked rather than downloaded/executed.
