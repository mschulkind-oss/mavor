---
status: accepted
stage: BUILT
next: "Parent integration; separately authorized physical GPU and host audio acceptance"
depends-on: [../tasks/visible-model-initialization-tasks.md]
---

# Visible initialization acceptance evidence

**Measured:** 2026-10-02 in the isolated feature worktree. Host daemon,
configuration and service were not installed or restarted.
[Design](../design/visible-model-initialization.md) owns the contract.

## Repairs and permanent evidence

Independent review found that daemon diagnostics preceded visual transitions,
which clear subtitles on both production backends. Permanent daemon regressions
now mimic clear-on-transition and X11 subtitle gating: startup diagnostics and
backup warnings failed before repair, then passed. Runtime now transitions first,
sets diagnostics second, and does not erase startup errors during shutdown.
Developer/public guidance now documents the narrow finalized-companion exception.

Desktop acceptance exposed two test defects, repaired rather than dismissed by a
rerun: existing daemon tests assumed socket binding meant ready; they now wait for
idle before toggling. Isolated GNOME children strip library environment variables;
the pinned test closure now links a transitive C++ runtime search path into test
executables. Staging-owned harness/workflow files were not edited.

## Actual observations

| Area | Evidence / result |
| :--- | :--- |
| Early startup | Real daemon callback lifecycle on both compositors; quiet startup/status/stop, then start/toggle reveal explanatory initializing before Factory, no queued recording/output |
| Ready | Mounted CPU streaming zipformer Factory and actual readiness inference, stream reset, initializing → hidden idle |
| CLI readiness | Mounted CPU Whisper base.en and zipformer, real inference and repeated decode after ownership transfer; no health-only substitution |
| Startup error | Controlled missing-model resolution error, exact diagnostic in IPC/HUD, original error returned, socket removed and owned loads joined |
| Request backup | Controlled supervised HTTP 500, real CPU zipformer companion, same recording, finalized text, one observed dispatcher call and one history row, model/source/warning retained |
| Warning presentation | Real daemon ERROR → BACKUP TRANSCRIPT → idle-preserved warning on Sway and GNOME, strict production-pixel oracle; no desktop typing by the fixture |
| Refusal/regression | Missing/incomplete/mismatched/odd/overflow/stale/canceled/blank backup, startup/CPU/remote/invalid exclusions, second main chunk failure, all final modes and CPU opt-in tests |
| Race | Uncached full `go test -race ./...`, including mounted backup and repeated stream reuse |
| Desktop safety | Full integration suite; GNOME geometry, focus/editor sentinel, clipboard/HUD coexistence, X11 clipboard persistence and supervisor cleanup |
| Motion | Shared 23-scene reports, quiet → speech → pause → recovery → decay; real captured five-frame GIFs, inspected speech/pause/recovery crops on both compositors |

The request fixture SHA-256 is
`feb3fc5d5a9b8579ec7cbba28ae7651cb07489927ec7fe814d318d944fe64f73`
(640044-byte checked-in local WAV). It is a controlled fixture, not a live microphone
claim. Actual same-cycle companion text begins “'S IS IN THE PIT” and ends
“THEN JARMY RUNS UP THE PATH”; the imperfect recognition is retained honestly.
History and output match the same cleaned finalized result. Main HTTP failure is
controlled, **not** evidence of hardware OOM, Vulkan inference, or shader warm-up.

## Reproduction and retained artifacts

Commands use only existing mounted models; no downloads or host changes:

```bash
MAVOR_READINESS_MODEL_DIR=/ctx/mavor-models MAVOR_BACKUP_SMOKE_MODEL_DIR=/ctx/mavor-models \
  go test -race ./... -count=1 -timeout=10m
MAVOR_READINESS_MODEL_DIR=/ctx/mavor-models go test -tags=e2e ./cmd/mavor ./internal/speech \
  -run '^(TestProductionInitializationTransfersVerifiedMountedModels|TestReadinessMountedModels)$' -count=1 -v
MAVOR_READINESS_MODEL_DIR=/ctx/mavor-models nix-shell test/gnome/environment.nix --run \
  'go test -tags=integration ./test/integration -count=1 -v -timeout=12m'
MAVOR_READINESS_MODEL_DIR=/ctx/mavor-models nix-shell test/gnome/environment.nix --run \
  'go test -tags=gnome ./test/gnome -run "^(TestGNOMEDaemonInitializationLifecycle|TestGNOMEStorybook|TestGNOMEOverlay|TestGNOMEOverlayGeometry|TestGNOMEHUDClipboardCoexistence|TestGNOMEX11Clipboard|TestHarnessCleanup|TestStorybookRegressions)$" -count=1 -v -timeout=12m'
just format
just check
just done
```

Reports are generated, ignored artifacts: [Sway HTML](../../test/reports/ui-storybook.html),
[GNOME HTML](../../test/reports/gnome-storybook.html), corresponding JSON, lossless
full/crop PNGs and five-frame GIFs. They are compositor captures of controlled
production-painter scenes, **not** real microphone/model lifecycle evidence.
The separate lifecycle captures above drive the actual daemon and mounted decoder.
Chromium headless rendered both HTML reports; inspected browser screenshots show
report cards/captures rather than broken images. Wallpaper SHA-256 remains
`9419801e0c39a0db869c21a7ac4dec828ede0c4a18699d7f8530092a78074610`.
Production logarithmic meter scaling and strict pixel tolerance are unchanged.

Durable scratch and the landing handoff are retained at:

```text
/workspace/.yolo/durable/visible-model-initialization/
  landing-handoff.md
  landing-red.log, landing-green.log, landing-race-final.log
  landing-readiness.log, landing-real-backup.log
  landing-sway-final.log, landing-gnome-final.log
  landing-sway-lifecycle-backup.log, landing-gnome-lifecycle-backup.log
  landing-sway-lifecycle-race.log, landing-gnome-lifecycle-race.log
  landing-check.log, landing-done.log, landing-media.log, landing-fixture-hash.log
  browser-sway.png, browser-gnome.png
```

## Demand-only follow-up

Initialization presentation now follows actual recording requests, not daemon
launch. Controller regressions observed automatic startup display failing before
repair; start/toggle, repeated requests, stop retention, readiness, genuine quiet
failure, cancellation cleanup, and request/completion ordering pass afterward.
The request shows the visual before setting its explanatory subtitle; the same
control lock serializes readiness and failure. No extra listener, queued intent,
model policy, or painter change was introduced.

Real Sway and Shell/Mutter 50.4 lifecycle captures now include quiet startup,
requested INITIALIZING with the recording-unavailable explanation, then hidden
ready or genuine ERROR. Mounted CPU model readiness and same-cycle finalized
backup still run. Full Sway integration and GNOME lifecycle, geometry, focus,
clipboard coexistence/restoration and normal 23-scene storybook gates passed.
The storybooks remain explicit requested-state fixtures, not automatic launch
behavior. Strict screenshot comparison and motion/scaling/wallpaper are unchanged.
A cancellation test initially checked a subtitle after shutdown cleared it; that
assertion now runs before cancellation, while close/join checks run afterward.

Follow-up logs and real captures are retained separately:

```text
/workspace/.yolo/durable/demand-initializing-hud/
  contract.md, repair-handoff.md
  unit-red.log, unit-green.log, race-final.log
  sway-lifecycle.log, sway-full.log, lifecycle-sway-*/
  gnome-lifecycle.log, gnome-full.log, gnome/hud-*/
```

## Limits

- `TestCannedWAVReachesClipboard` skipped: no reachable host PulseAudio/PipeWire
  server. Mounted fixture/model tests do not certify private microphone capture.
- Shell/Mutter 50.4 isolated sessions and headless Sway are measured, not a full
  desktop login/lifecycle certification or exact Ubuntu/GitHub CI proof.
- Readiness tests decode a deterministic two-second generated voiced fixture
  (WAV SHA-256 `cd3a452247859a741ccd3b03ddcd3fe32f32838ca41d140b1b72989acc8db35a`),
  discard text, reset streams and remove temporary files. They establish usable
  inference, not accuracy, peak GPU reservation or immunity to later allocations.
- Native C model construction/decode cannot be preempted inside a call; cancellation
  invalidates readiness/results, then joins safely before releasing native memory.
- Browser rendering is local headless Chromium; no interactive Vantage session or
  physical GPU acceptance was performed. Host deployment needs separate approval.
