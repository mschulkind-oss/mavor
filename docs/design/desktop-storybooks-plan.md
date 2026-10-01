---
status: accepted
stage: BUILT
next: "Parent review and coherent commit after verification"
---

# Keep storybook staging outside ordinary harness startup

**Status:** 2026-10-01, detached `ea440c8` plus uncommitted changes.
MEASURED: both desktop reports generated from compositor frames.
**Needs your ruling:** None. The user supplied the scoped design.

## Constraints and reuse

The user scope wins on behavior; the tree wins on fact. This handoff records
implementation constraints, not a replacement design.

- Reuse `StartWithBar` and `Harness.Grim`; never change the ordinary black backdrop.
- Reuse the [GTK consumer](../../test/gnome/client/consumer.c) with opt-in presentation.
  Preserve its default clipboard tests and native Paste action.
- Reuse the X11 worker's production dispatcher child, focus checks, and external
  supervisor. Storybook mode returns before destructive acceptance scenarios.
- Keep reports ignored. Regeneration replaces current filenames; run sequentially.
- Fail on missing prerequisites, mapping, geometry, screenshot errors, or absent
  colored wallpaper/native-window pixels. Never use an offline fallback.

## File map

| Files | Responsibility |
| :--- | :--- |
| [wallpaper](../../test/desktop/wallpaper.png), [consumer](../../test/gnome/client/consumer.c) | New shared original background; opt-in readable editor |
| [Sway staging](../../test/integration/desktop_staging_test.go), [Sway report](../../test/integration/ui_storybook_test.go) | New tracked staging helpers; preserve nine states and crops |
| [GNOME worker](../../test/gnome/x11_harness.py), [report module](../../test/gnome/storybook.py) | Real Shell captures and exact copy/Paste assertions; new escaped HTML |
| [GNOME entry point](../../test/gnome/ui_storybook_test.go), [Python regressions](../../test/gnome/test_storybook.py) | New tagged entry point, report/error and prerequisite tests |
| [Nix environment](../../test/gnome/environment.nix), [recipes](../../Justfile), [README](../../README.md#desktop-storybooks) | Pinned staging dependencies, regeneration, usage and limits |

## Verification order

1. Report/error regressions (first run failed because the report module was absent).
2. Generate GNOME and Sway; inspect full frames and unchanged overlay crops.
3. Existing X11 acceptance and supervisor tests; ordinary Sway overlay tests.
4. Browser rendering, Markdown checks, `just check`, and `just done`.

No host desktop, daemon deployment, keyboard injection, real inference,
GNOME HUD, Chromium desktop staging, or jail configuration changes.
[Tasks](../tasks/desktop-storybooks-tasks.md) and [QA](../qa/desktop-storybooks-qa.md)
carry execution evidence.
