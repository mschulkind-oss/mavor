---
status: accepted
stage: BUILT
next: "Probe a real GNOME session using the QA acceptance checklist"
---

# Clipboard is an explicit choice, never a fallback

**Status:** 2026-10-01, delivery changes based on `a2ba579`. MEASURED: unit
and race tests and real headless Sway clipboard/paste/typing tests passed.
GNOME session behavior is UNMEASURED; see [QA](../qa/gnome-clipboard-qa.md).
This plan remains a graduation candidate after parent integration and desktop
acceptance; the existing output references describe the implemented mode.

> **In short:** manual paste works without synthetic keyboard permissions, but
> background copying still needs verification on the user's compositor.

**Needs your ruling:** None; behavior follows the supplied workflow plan.
**Reads with:** [research](../research/gnome-clipboard-research.md),
[tasks](../tasks/gnome-clipboard-tasks.md), [QA](../qa/gnome-clipboard-qa.md).

## Behavior

Select `[output] driver = "clipboard"` explicitly. Keep paste as default and
retain typing behavior. Reject unknown nonempty driver names. Clean whitespace,
skip empty transcripts, copy plain UTF-8 text on stdin to CLIPBOARD only.
Copy regardless of the additional-copy flag; ignore paste/typing knobs.
Never connect a virtual keyboard, inject a chord, read selections, restore them,
or touch PRIMARY. Exactly one helper is launched per nonempty transcript.

Apply a three-second launch context deadline, respecting earlier cancellation.
This is not an absolute all-process wait bound. Ordinary backgrounding
wl-copy retains successful ownership beyond that deadline, without paste-once.
Errors are wrapped and logged by the existing daemon; history precedes output.
Idle means completion, not successful copying. Helper checks must not overwrite
user data or pretend to prove clipboard readiness.

Overlay failure stays independent: without layer-shell there is no visible
preview or waveform, though internal preview may still run. Use status/logs
and GNOME custom shortcuts running the absolute binary path with `toggle`.

## Boundaries and risks

No portals, auto-selection, GNOME HUD, notifications, model/audio/history/IPC
changes, or silence-filter setting. Notifications are deferred: status and logs
are less convenient but add no dependency or transcript exposure.

A successful launch is not a readiness guarantee. Transparent-surface fallback
may disrupt focus or hang; cancellation cannot guarantee descendant cleanup.
Sway validation does not certify Mutter. Live GNOME acceptance remains required.

## Implementation map and precedence

Behavior above wins; the tree wins on facts; implementation advice comes last.
Reuse `CleanText` and `DefaultRunner` in a dedicated clipboard dispatcher rather
than the paste ownership machinery. Wire selection ahead of native construction.
Make setup/doctor require wl-copy only for clipboard output; preserve other
drivers' requirements. Explain ignored knobs in the scaffold and user docs.
Keep shared config/main/documentation edits isolated for parent integration.
