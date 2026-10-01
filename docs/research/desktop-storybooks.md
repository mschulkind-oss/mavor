---
status: accepted
stage: CURRENT
---

# Real desktop capture findings

Measured 2026-10-01 against detached `ea440c8` plus the uncommitted storybook
changes. [GNOME clipboard research](gnome-clipboard-research.md) owns the
selection-transfer and private-session background; this page adds capture findings.

## Verified choices

- Adopt one original [wallpaper](../../test/desktop/wallpaper.png) and the
  existing native GTK editor, rather than a browser with another focus lifecycle.
- GNOME Shell 50.4 accepts `SHELL_BACKGROUND_IMAGE` before startup. Its real
  `org.gnome.Shell.Screenshot.Screenshot` method produces 1280 × 720 frames
  through the private bus with cursor and flash disabled. No image compositing.
- Sway 1.12 uses tracked `swaybg` and a floating editor below the 180-pixel
  overlay crop. Wait for final geometry: resizing immediately after mapping
  can be overwritten by GTK's initial size negotiation.
- Copy-only GNOME scenes preserve editor contents and emit no focus-change
  events; invoking the editor's own Paste action inserts the exact fixture.

## Traps and limits

External settings commands cannot share a memory settings backend. Use the
Shell wallpaper override instead. Private buses need short socket paths.
Shell startup warning banners must be dismissed through its native dismissal
before capture; do not fabricate notifications or remove pixels afterward.

The pinned shell needs its C++ runtime library for Go's vendored speech
libraries. Resolve the real bash executable for `NIX_BUILD_SHELL`: `/bin/bash`
can prepend `/bin` and choose a Go executable inconsistent with inherited
`GOROOT`. [QA evidence](../qa/desktop-storybooks-qa.md) records reproduction.
