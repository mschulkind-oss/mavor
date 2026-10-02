---
status: accepted
stage: BUILT
next: "Parent integrates the isolated feature outcome; expand runtime certification separately"
depends-on: [../design/gnome-hud.md]
---

# Build the backend first, then share the storybook

**Status:** 2026-10-02. Implemented and measured against `f8c9f02`-based
source; [QA](../qa/gnome-hud-qa.md) records final acceptance gates and limits.
Independent review accepted actual parity; the final owner repeated landing checks.
**Design:** [one shared painter](../design/gnome-hud.md).
Precedence: design wins on behavior; tree wins on fact; plan advice is first to
be wrong. Never twist code to fit a stale file map.

**Constraint:** the [owner board](../tasks/gnome-hud-tasks.md) is exclusive and
serial. Its paths reserve new files too; no writer crosses a boundary without a
recorded handoff. No component commits; final integrator owns the one commit.

## File map

The following paths record the completed serial implementation.

| Owner | Files | Change |
| :--- | :--- | :--- |
| Production | `internal/overlay/overlay_x11.go`, `x11_transport.go`, `x11_geometry.go` (new) | Production presenter, bounded authorized socket, monitor mapping |
| Production | `internal/overlay/frame.go`, `frame_test.go` (new) | Optional observer and receipt contract |
| Production | Existing `internal/overlay/factory.go`, `overlay.go`, `overlay_wl.go`; new `factory_test.go`, `overlay_x11_test.go`, `x11_transport_test.go`, `x11_geometry_test.go` | Automatic selection, shared frame receipts, tests; painter stays sole renderer |
| Production | `cmd/mavor/main.go`, `doctor.go`, `doctor_test.go`, `service_cmd.go`, `service_cmd_test.go`; `internal/config/` only if comments/tests require correction | Initialization, honest HUD probe and session environment guidance; no new settings |
| Production | `go.mod`, `go.sum` | Pin reviewed pure-Go XGB/D-Bus libraries |
| Production | `test/gnome/overlay_test.go`, `overlay_harness.py`, `test_overlay_harness.py`, `overlay_consumer.c` (new) | Dedicated independent production proof/JSON child, native pointer/focus fixture |
| Production | Existing `test/gnome/environment.nix` only if backend prerequisites require it | Preserve pinned Shell/Mutter; no storybook-only changes here |
| Storybook | `test/desktop/capture.go`, `oracle.go`, `report.go`, `oracle_test.go`, `report_test.go` (new) | Untagged shared Go catalog, frame driver, oracle, artifacts, Sway-derived template |
| Storybook | Existing `test/integration/ui_storybook_test.go`, `desktop_staging_test.go` | Consume shared support; preserve ordinary integration harness behavior |
| Storybook | Existing `test/gnome/ui_storybook_test.go`, `test_storybook.py`, `x11_harness.py`; new `hud_storybook_harness.py`, `clipboard_qa.py`, `test_clipboard_qa.py` | HUD captures via production child; relocate clipboard QA; editor staging |
| Storybook | `Justfile` | HUD acceptance and separate clipboard-report recipes; retain Nix wrappers |
| Integration | Existing `README.md`, `docs/user-guide.md`, `docs/reference/how-mavor-works.md`, `docs/design/porting-to-gnome.md`, `docs/roadmap.md`; new `docs/qa/gnome-hud-qa.md`; these four GNOME HUD docs | Reconcile actual behavior, record final checks/stages, one commit |

Production owns all `internal/overlay/` and necessary existing related tests,
not just listed additions. Production owns necessary cmd/config tests within its
listed domains. Storybook owns all new shared `test/desktop/` code, but never
changes its existing wallpaper. No changes to `supervise.py`, `test_supervise.py`,
`clipboard_test.go` or native `harness.py` are planned; reuse them unchanged.
Integration is verification/docs plus the parent-approved backend-oracle wiring
and dedicated HUD/clipboard coexistence fixture. Route substantive production repairs back to the
owning lane serially. Unlisted source changes require an explicit board amendment.

## Reuse and traps

- **Advice:** reuse `Scene`, `FixedSurfaceSize`, `RenderInto`, `SceneBounds` from
  [paint.go](../../internal/overlay/paint.go); these preserve visual parity.
- **Advice:** mirror the owner loop/sample aggregation and cadence in
  [overlay_wl.go](../../internal/overlay/overlay_wl.go), rather than the synchronous
  scratch probe, because the probe does not meet transport/lifecycle requirements.
- **Constraint:** freeze the design’s JSON protocol and optional observer in the
  production handoff before storybook starts. Driver owns scene selection; child
  consumes generic apply requests and returns owned real frame metadata.
- **Advice:** move the Sway HTML rather than rewrite it: existing controls are the
  requested product. `test/desktop/` is the agreed shared package (not a second
  competing `test/storybook/` package).
- **Constraint:** rewrite old GNOME tests asserting “No mavor HUD” or counting seven
  core scenes to the new catalog. Preserve the seven output scenes separately.
- **Constraint:** static animation does not freeze waveform history. Use frame
  receipts, one steady-RMS sample per acknowledged scheduled frame, record actual
  levels/phase, and fail closed if the independently captured frame disagrees.
- **Constraint:** producer acknowledgment is not screenshot evidence. Take actual
  capture after the documented steady-state settling wait and fail closed on any
  state-oracle mismatch; never silently accept a baseline.
- **Advice:** reuse supervisor invocation/cancellation from
  [GNOME entry point](../../test/gnome/ui_storybook_test.go) to preserve process
  group ownership, adoption and reaping. Backend proof harness exposes reusable
  session setup/capture functions without starting a session on import.

## Build order and ships with

1. Production: failing tests first for selection, auth/capabilities, deadlines,
   request chunking, geometry and preview clearing; implement real backend and
   observer. Run targeted Go tests and Python negative/lifecycle tests.
2. Production: independent real Shell proof for visual/input/loss/monitor behavior;
   freeze child protocol, gate output and artifacts in the durable handoff.
3. Storybook: failing shared catalog/template/oracle tests first; migrate both
   suites and clipboard QA, then generate both actual compositor reports.
4. Integration: inspect reports and run full applicable gates once after all
   repairs, reconcile shipped docs and write QA evidence before the single commit.

Exact per-owner commands and evidence requirements are in the
[board](../tasks/gnome-hud-tasks.md#owner-gates-and-handoffs).
Unit coverage must include local/remote DISPLAY, missing/wrong auth, alpha/SHAPE
failure, stall/close bounds, negative origins, transforms, no outputs, ambiguous
workspace, primary removal, scale mapping, latest-state/sample bounds and retries.
Shared tests cover exact IDs/order, previews, template controls/escaping,
compositor-specific labels, relative artifact paths/traversal, dimensions, absent
HUD, wrong state, missing/fitted preview, stale receipts and duplicate HUD regions.

## Constraints and blockers

Work only in the locked detached durable worktree; preserve ancestor `649aeb6`.
Run/log everything under the short durable root specified by the board. Reports
remain ignored. No host installs/services, branches, deployment, publishing,
tagging, alternate painter, screenshot composites or simulated production typing.

Ordinary compiler/test/lint failures are repair work, not checkpoints. A real
infrastructure blocker or need for extension/special setup requires durable
handoff and parent decision; never turn missing prerequisites into passed tests.
Cheap implementation choices (containers, decomposition, error wording) belong
to the owner; changes to design behavior require reopening the design explicitly.
