---
status: accepted
stage: BUILT
next: "See measured QA and separately authorized host acceptance"
depends-on: [../design/visible-model-initialization.md]
---

# Visible initialization implementation plan

Written against `dbb44ff`, 2026-10-02. [Design](../design/visible-model-initialization.md)
wins on behavior; tree wins on fact; this plan is advice and is first to be wrong.
Frozen signatures and exclusive lanes are in the durable parent contract handoff.

## Source map

| Owner | Files / change |
| :--- | :--- |
| Painter | [`overlay`](../../internal/overlay/) — additive Initializing/Degraded, size/backends/tests |
| Painter | [`desktop`](../../test/desktop/) — catalog, timed motion, strict validation, dynamic counts |
| Painter | [`ui_storybook_test.go`](../../test/integration/ui_storybook_test.go), [`GNOME suite`](../../test/gnome/) — real captures, lifecycle tests |
| Backup | New daemon backup_cycle implementation/test only — frozen complete-result helper |
| Runtime | [`main.go`](../../cmd/mavor/main.go), [`modelstart.go`](../../cmd/mavor/modelstart.go) — early owned callback, remove duplicate closes |
| Runtime | [`daemon.go`](../../internal/daemon/daemon.go), [`final_cycle.go`](../../internal/daemon/final_cycle.go) — one listener, init transfer, one PCM reader, whole-result dispatch |
| Runtime | [`state`](../../internal/state/), [`ipc`](../../internal/ipc/) — startup state/events, optional warning/source |
| Runtime | [`speech`](../../internal/speech/) — new readiness fixture/probe, classified request errors, supervisor default, companion diagnostics |
| Runtime | [`history`](../../internal/history/) — optional source/model/warning with old-row tests |
| Parent | [QA](../qa/visible-model-initialization-qa.md), affected references/README/AGENTS/roadmap — reconcile and record measured evidence |

## Reuse and traps

- Advice: reuse main FinalSession prefix digest/tail-reconciliation algorithm;
  reason: it already handles the recorder's stop-tail timing. Do not reuse replay.
- Constraint: `ParecRecorder.Stop` clears the live read path; a final ReadChunk
  after Stop does not recover the tail. Read finalized WAV instead.
- Constraint: incremental companion queue can drop; old final text is discarded.
  Helper must replace that companion path, not run beside it.
- Advice: use `speech.Unwrap` for capability checks; reason: chunking wraps server
  and streaming recognizers and already forwards Start/Close.
- Constraint: `beginStart` wait is a single receive, not an idempotent future;
  callback cancellation must join before model ownership transfer or close.
- Constraint: native factory/decode has no forced preemption; retain owner until
  return. Context timeout alone does not prove goroutine/process cleanup.
- Advice: retain `history.Append` seam and optional JSON fields; reason: old rows
  remain valid and exactly-once dispatch need not gain a second persistence API.
- Constraint: receipts prove submission only. Real motion captures must coordinate
  production history shifts without weakening waveform pixel checks.

## Build order and proof

1. Red regressions for missing initializing/degraded catalog and backup coverage.
   Painter and backup owners run their targeted unit suites; do not commit stages.
2. Painter slice: `go test ./internal/overlay ./test/desktop`; tagged desktop
   lifecycle/motion tests require actual compositor sessions, not painter fixtures.
3. Backup helper slice: `go test -race ./internal/daemon -run Backup`; prove cancel,
   queue loss, digest mismatch, final tail, deadlines and rapid recognizer reuse.
4. Runtime red tests then startup/classifier/dispatch wiring:
   `go test ./cmd/mavor ./internal/daemon ./internal/speech ./internal/state ./internal/ipc ./internal/history`.
5. Parent integration: `just check`; actual Sway and GNOME storybooks and lifecycle
   tests; real-model readiness E2E where models exist. Record skipped prerequisites.
6. Parent repairs deterministic failures, reconciles docs, `just done`, one commit.

## Ships with

- Rewrite fixed12 catalog assertions in desktop oracle/report tests to derived
  catalog expectations; retain rejection of absent/unpresented HUDs.
- New daemon regressions use real socket controls while factories/probes block,
  and spies assert zero capture/duck/history/output before ready.
- Speech tests count actual decode requests, verify fixture format/nonzero voiced
  samples, stream reset, sidecar cleanup and classifier exclusions.
- Backup tests use divergent partial/final texts and byte-exact finalized WAV tail;
  output tests assert authoritative empty main never triggers backup.
- Existing preview/discard tests remain for ordinary previews; qualify assertions
  rather than relaxing generation/authority protection globally.
- Parent reconciles [README](../../README.md), [AGENTS](../../AGENTS.md),
  [system reference](../reference/how-mavor-works.md),
  [configuration design](../design/configuration-surface.md),
  [model choice](../choosing-a-model.md),
  [incremental-final design](../design/incremental-final-transcription.md),
  [roadmap](../roadmap.md) and CLI status help. Old unconditional
  “preview never emits” / “always main” claims now need the qualified exception.
- No new public config knobs required. Existing CPU fallback remains explicit,
  false by default; no fresh backup model download/load on failure.

## Don't

No host changes, alternate-tree edits, feature branches, delegated children,
remote CI reruns or stage commits. Staging-owned desktop_staging test and possible
harness readiness/.github files require parent approval. Cheap private choices
(queue representation, diagnostic wording) are implementer's; weakening eligibility,
coverage, ownership, strict pixels or safety requires parent coordination.
