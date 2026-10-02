---
status: accepted
stage: BUILT
next: "Parent integrates the isolated outcome; physical/full-login certification remains separate"
depends-on: [../plans/gnome-hud-plan.md]
---

# Serial GNOME HUD owner board

**Status:** 2026-10-01 at `f8c9f02`. Production presentation is independently
proved in the durable backend handoff. Storybook is implemented and its real
Sway/GNOME report gates pass. Integration acceptance and shipped-document reconciliation are measured in
[QA](../qa/gnome-hud-qa.md); independent review accepted parity and the final owner
repeated all applicable landing gates.
The [plan](../plans/gnome-hud-plan.md#file-map) is the exclusive file reservation;
the [design](../design/gnome-hud.md) is the behavioral contract.

## Lanes and progress

| Order | Owner | State | Boundary / exit |
| :--- | :--- | :--- | :--- |
| 0 | Planning | Complete | Four concise docs only; no source changes or commit |
| 1 | Production | Verified backend handoff | Overlay/factory/doctor/dependencies and dedicated GNOME proof files; independent real backend acceptance |
| 2 | Storybook | Component complete, uncommitted | Shared desktop scenes/report/driver/oracle, Sway/GNOME capture and recipes; cannot edit backend or proof files |
| 3 | Integration | Verified, uncommitted | Shared visual-oracle wiring, dedicated coexistence fixture and docs/QA; final owner commits |

Only one owner writes at a time, in the same locked detached worktree. No child
agents, branch changes, other worktrees or concurrent writers. Ownership passes
only when its predecessor records gate results and stops editing. A return to an
earlier owner pauses all later work. No intermediate commits.

```text
Worktree: /workspace/.yolo/durable/worktrees/gnome-hud
Scratch, prototypes, logs, private buses: /workspace/.yolo/durable/ghud
Production handoff: /workspace/.yolo/durable/ghud/production-handoff.md
Storybook handoff: /workspace/.yolo/durable/ghud/storybook-handoff.md
Integration handoff: /workspace/.yolo/durable/ghud/integration-handoff.md
```

Set TMPDIR to a short subdirectory of this root **inside** Nix commands (the
existing shell hook otherwise resets it). Keep runtime paths short and logs
retained. `MAVOR_GNOME_ARTIFACTS` also points under this root. Generated reports
stay in their existing ignored worktree destinations, never force-added.

## Owner gates and handoffs

### Production: presentation independently proved

The following checks are measured in the backend handoff and final QA. Negative
origins have unit coverage only; physical hotplug is not certified.

- [x] Write failing regression tests before behavior changes; record the failure.
- [x] Implement auto-selection preserving Sway and existing output config;
  no alternate painter or Noop success in proof tests.
- [x] Freeze version-1 child protocol and FrameObserver from the
  [design](../design/gnome-hud.md#frame-acknowledgment-contract).
- [x] Supply import-safe session/capture support in the dedicated proof harness;
  storybook can import it and invoke the child but cannot modify it.
- [x] Capture real recording/preview/transcribing/error/hidden transitions;
  verify state pixels, preview clearing, zero Shell focus transitions and zero
  unexpected GTK active changes, unchanged editor text.
- [x] Demonstrate actual pointer clicks delivered to a native fixture underneath
  visible pill and preview and after hiding; read back empty X input region too.
- [x] Test first map, repeated show/hide, rapid updates, workspace/fullscreen and
  overview entry/exit; don't count intentional fixture focus changes as HUD theft.
- [x] Test stall bounds, partial startup cleanup, compositor loss/retry and close;
  inspect worker/adopted PIDs after reaping.
- [x] Test monitor removal/addition, negative origins, 2× and fractional/mixed-scale
  actual sessions. Distinguish measured support, honest rejection and infrastructure
  gaps; source/unit arithmetic alone is not runtime proof.

Required gates, directly usable before Storybook adds recipes:

```bash
go test ./internal/overlay ./internal/config ./cmd/mavor -count=1
python3 -m unittest discover -s test/gnome -p 'test_overlay_harness.py'
env -u LD_LIBRARY_PATH NIX_BUILD_SHELL="$(readlink -f "$(command -v bash)")" \
  nix-shell test/gnome/environment.nix --run \
  'TMPDIR=/workspace/.yolo/durable/ghud/t MAVOR_GNOME_ARTIFACTS=/workspace/.yolo/durable/ghud/runs go test -tags=gnome ./test/gnome -run "^(TestGNOMEOverlay|TestGNOMEOverlayMissingPrerequisite|TestHarnessCleanup)$" -count=1 -v -timeout=10m'
```

Create the short TMPDIR before invocation. Permanent `TestGNOMEOverlay` subtests
cover the requirements above; missing infrastructure in this explicit gate fails,
not skips. Handoff includes changed-file list, exact commands/exits, assertion
results, session/version/scale, capture paths, child invocation/environment,
protocol/receipt examples, cleanup proof and any remaining blockers. Do not pass
the lane with a backend/material acceptance blocker unresolved; ask parent instead.

### Storybook: matched reports from genuine frames

- [x] Add failing shared catalog/template/oracle negatives first.
- [x] Use `test/desktop/` shared Go support for both reports; no Python HTML fork.
  Python stages/captures GNOME only; Go validates artifacts and renders shared HTML.
- [x] Consume the frozen production child without changing its code or window.
  Shared driver adapts Overlay directly on Sway and JSON on GNOME.
- [x] Preserve nine canonical IDs plus three matching preview IDs; test subsequent
  clearing transitions outside the core report catalog too.
- [x] Retain Sway UI controls/layout; GNOME has honest native chrome/capture labels.
- [x] Split old seven clipboard scenes into `gnome-clipboard-qa.html` and
  `gnome-clipboard-screenshots/` with their own metadata, still ignored.
- [x] Add `test-gnome-overlay`/`test-gnome-overlay-nix` recipes for the production
  gate above, and `storybook-gnome-clipboard`/`storybook-gnome-clipboard-nix` for
  old clipboard QA. Retain existing HUD report recipe names/Nix pin.
- [x] Generate both HUD reports at 1920×1080; actual PNG full/crop artifacts,
  manifest sequence/receipt/pixel evidence, unchanged wallpaper and editor text.
- [x] Confirm missing backend/prerequisite, stale receipt, capture failure, wrong
  dimensions/state/preview and region reuse fail explicitly.

Required gates:

```bash
go test ./test/desktop -count=1
python3 -m unittest discover -s test/gnome -p 'test_storybook.py'
just storybook-nix
just storybook-gnome-nix
just storybook-gnome-clipboard-nix
just test-gnome-overlay-nix
```

Set short runtime/artifact roots as above. Handoff includes exact exits/logs,
report/manifest paths, scene parity checks, negative-fixture failures, image/link
checks and full/crop inspection notes. Outputs stay ignored. No commit.

Storybook evidence: `/workspace/.yolo/durable/ghud/storyboard-completed-handoff.md`.
Option A is resolved: receipts remain submission-only; the shared image oracle
locates screenshot canvas bounds from actual pixels independently of Screen.
Both drivers consume the catalog's 50-frame steady-RMS schedule. Constant levels
avoid capture-time scrolling ambiguity while exercising the real decibel mapping;
no production animation freeze or screenshot composite is used. Extra final Hidden
captures verify restoration after preview clearing. All twelve core scenes have
real full-frame and 180px crop output, with one shared Sway-derived template.

### Integration: verify, reconcile, then commit once

- [x] Independently inspect both reports in light/dark, crop/full and lightbox;
  matched scenes, genuine GNOME chrome, real preview and no repeated placeholder HUD.
- [x] Run `just format`, `just check`, `just done`; record literal gate result.
- [x] Run full Sway integration suite in the pinned environment, not only report
  tests; run production HUD and supported X11 clipboard acceptance uncached.
- [x] Run supervisor/Python/shared regressions and GNOME tagged compile checks;
  project tagged gate currently may not include `gnome`, so explicitly compile it.
- [x] Reconcile stale HUD/only-layer-shell/no-preview claims in the plan’s reserved
  shipped docs and roadmap. Preserve separate injection and wl-copy limitations.
- [x] Write `docs/qa/gnome-hud-qa.md` with actual commands/results and screenshot
  inspection evidence; update stages only when evidence supports them.
- [x] Audit ancestor `649aeb6`, unchanged wallpaper hash, process cleanup and
  ignored reports; no host/main-worktree modifications.
- [x] Include the complete feature in one outcome after all applicable gates pass;
  hooks are binding.
  End with clean status. No publish/tag/deploy or host restart.

Additional integration commands:

```bash
go test -tags=gnome ./test/gnome -run '^$'
python3 -m unittest discover -s test/gnome -p 'test_*.py'
env -u LD_LIBRARY_PATH NIX_BUILD_SHELL="$(readlink -f "$(command -v bash)")" \
  nix-shell test/gnome/environment.nix --run \
  'TMPDIR=/workspace/.yolo/durable/ghud/t just test-int -count=1'
env -u LD_LIBRARY_PATH NIX_BUILD_SHELL="$(readlink -f "$(command -v bash)")" \
  nix-shell test/gnome/environment.nix --run 'just test-gnome-x11 -v'
```

Native wl-copy focus diagnostics are known failures and not a supported acceptance
gate. Ordinary deterministic gate failures return to the owning lane for automatic
repair; material decisions/infrastructure blockers stop with a durable handoff.
Planning does not run or claim these implementation gates.

## Planning verification

Only these four planning documents changed. `uvx vantage-check` 0.7.1 reports
“4 files checked, nothing to fix”; its help lacks planning/index capabilities, so
those checks were not run. Stage/dependency and exclusive-owner review was manual.
`git diff --check` passed; HEAD remains `f8c9f02`, and ancestor check confirms
`649aeb6`. No source tests, new screenshots or feature acceptance ran in planning.
Wallpaper baseline SHA-256 (unchanged tracked file):

```text
9419801e0c39a0db869c21a7ac4dec828ede0c4a18699d7f8530092a78074610
```

No commit. The final integration owner, not Planning, owes the clean committed tree.

## Integration measurement

Parent approved Integration edits to backend visual acceptance and a dedicated
same-session coexistence fixture; no production API or output behavior was changed.
Four permanent swap/duplicate assertions failed against the prior weak checks,
then passed with the existing shared screenshot validator. Fresh real backend,
geometry and combined HUD/X11 clipboard captures pass; clipboard owner replacement,
Close and supervisor reaping were observed. Both report recipes and Chromium
parity controls pass. Shipped README, guide, system/porting notes and agent facts
now describe GNOME presentation separately from output.

See [QA](../qa/gnome-hud-qa.md) for exact commands, one audio-server skip, retained
Sway paste-restoration timing failures and passing complete rerun, build/runtime
prerequisites, physical-monitor and lifecycle gaps. The final owner reconciled the fixed-settling capture contract, added a
fail-closed report regression, repeated the full landing gates and retained the
accepted design/plan as the implementation contract. Parent checkout integration
is separate from this isolated feature outcome.
