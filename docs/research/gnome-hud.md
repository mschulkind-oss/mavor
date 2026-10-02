---
status: accepted
stage: CURRENT
---

# GNOME can display the existing painter without a Shell extension

Historical planning evidence reviewed 2026-10-01 against detached `f8c9f02`.
Production acceptance now supersedes the prerequisite gaps below; see
[final measured QA](../qa/gnome-hud-qa.md). This page adds only
HUD findings; [desktop capture research](desktop-storybooks.md) and
[clipboard research](gnome-clipboard-research.md) retain their separate scope.

## Planning evidence and its limits

- Verified from the planning baseline: [factory](../../internal/overlay/factory.go#L17)
  selected only Wayland; [daemon initialization](../../cmd/mavor/main.go#L188)
  warns and uses Noop on constructor failure. Production GNOME had no HUD at that baseline.
- Verified from this tree: [shared painter](../../internal/overlay/paint.go#L63)
  accepts visual state, waveform, preview, phase and fixed canvas dimensions.
  No separate GNOME painter is needed.
- Scout-reported experiments, not rerun during planning: two private real
  Shell/Mutter 50.4 sessions displayed recording, preview, transcribing, error,
  hidden and recording again using the shared painter. Both observed zero
  Shell keyboard-focus transitions; the second read back an empty X input region.
- Actual compositor PNGs demonstrate alpha transparency and the unchanged
  soft-focus wallpaper. Empty input rectangles do **not** prove actual pointer
  delivery; final focus alone does **not** prove absence of temporary focus theft.
- Not accepted during planning: production transport, pointer clicks, stacking, workspace
  changes, scale/hotplug, stalled-server deadlines and compositor-loss recovery.

Durable scout contract and evidence (local artifacts, not release dependencies):

```text
/workspace/.yolo/durable/ghud/production-backend-contract.md
/workspace/.yolo/durable/ghud/runs/storybook-3pw6sufy/preview.png
```

## Verdicts

| Candidate | Disposition |
| :--- | :--- |
| Passive XWayland alpha window | Adopt, conditional on independent production acceptance; no extension installation |
| Native ordinary Wayland window | Reject: does not supply passive absolute positioning on GNOME |
| Managed X11 dock/window | Reject: unnecessary window-manager focus and workspace policy |
| Shell extension | Reserve for an explicit deployment decision if XWayland cannot meet the design |
| Production Shell Eval | Reject: unsafe and not a deployable presentation backend |
| Clipboard/editor screenshots as HUD storybook | Reject: demonstrate output acceptance, not HUD states |

The [settled design](../design/gnome-hud.md) owns behavior, and the
[serial owner board](../tasks/gnome-hud-tasks.md) owns execution gates.

## Fast-moving — verify before building

Scout inspected `github.com/jezek/xgb` v1.3.1 (pure Go X protocol bindings).
Its ordinary connection constructor is not deadline-safe enough; use an owned
socket and checked authorization. A pure-Go D-Bus client is needed for bounded
read-only Mutter monitor queries; the production owner pins its reviewed version.
No new C library or helper service is planned. XWayland availability and session
DISPLAY/authorization remain actual prerequisites, not inferred from desktop name.

Mutter source inspection reports per-monitor work areas and a potentially global
X coordinate multiplier. Matching monitor layouts is necessary; primary-monitor
DPI alone is insufficient. Missing current-desktop data occurred in the probe;
workspace zero is safe only when published work areas agree.

## Sources

- [X core protocol](https://www.x.org/releases/current/doc/xproto/x11protocol.html)
  defines override-redirect: a window bypassing ordinary window-manager management,
  not permission to take focus.
- [SHAPE protocol](https://www.x.org/releases/current/doc/xextproto/shape.html)
  defines the input region, independent of transparent pixels.
- [RENDER protocol](https://www.x.org/releases/current/doc/renderproto/renderproto.html)
  defines alpha-capable visual formats; depth 32 alone is insufficient.
- [XGB source](https://github.com/jezek/xgb) supplies protocol bindings, not HUD policy.

The scout inspected local Mutter/library source; these external specification
links provide terminology, not a claim that planning fetched or reverified them.
