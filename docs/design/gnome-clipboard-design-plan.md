---
status: accepted
stage: BUILT
next: "Establish a permitted focus-safe copy strategy; retained GNOME acceptance fails"
---

# Clipboard is an explicit choice, never a fallback

**Status:** 2026-10-01, delivery changes based on `a2ba579`. MEASURED: unit
and race tests and real headless Sway clipboard/paste/typing tests passed.
MEASURED: isolated real GNOME Shell 50.4 transfers text but briefly steals
focus, and stalls with the overview open; see
[research](../research/gnome-clipboard-research.md). Complete login-session
acceptance remains outstanding; the permanent harness now retains failing
focus acceptance checks.
This plan remains a graduation candidate after parent integration and desktop
acceptance; the existing output references describe the implemented mode.

> **In short:** manual paste works without synthetic keyboard permissions, but
> GNOME copying is focus-dependent, not focus-safe background delivery.

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
Sway validation does not certify Mutter. The real headless GNOME probe proves
transfer only with helper focus; complete desktop acceptance remains required.

## Implementation map and precedence

Behavior above wins; the tree wins on facts; implementation advice comes last.
Reuse `CleanText` and `DefaultRunner` in a dedicated clipboard dispatcher rather
than the paste ownership machinery. Wire selection ahead of native construction.
Make setup/doctor require wl-copy only for clipboard output; preserve other
drivers' requirements. Explain ignored knobs in the scaffold and user docs.
Keep shared config/main/documentation edits isolated for parent integration.


## Real GNOME harness handoff

The [live research](../research/gnome-clipboard-research.md) establishes feasibility
without a jail restart. Keep the GNOME harness separate from the Sway harness:
its isolation pattern is reusable, its compositor flags, focus control,
screenshot tools, and readiness conditions are not.

Use actual GNOME Shell with a private runtime, home, XDG paths, and bus addresses,
Mesa paths from the installed closure, and an internal Mutter virtual keyboard.
Enable unsafe Eval only inside the private test session, to create input capability
and focus the destination. The consumer must request clipboard text over native
Wayland; Eval must never supply clipboard contents or bypass selection ownership.

Acceptance has two distinct results:

- **Transfer:** default-backgrounding wl-copy exits, a native client reads exact
  text after a delay and repeatedly, and a later copy replaces it.
- **Focus safety:** no intervening destination focus loss and no source-helper
  activation required. The 50.4 probe fails this condition. Preserve that finding
  explicitly; do not force focus onto the helper inside a passing delivery test.

Cover overview/no-focused-window separately. Activating the transient helper is
allowed only as a labeled diagnostic control proving why a pending copy unblocks.
An unchanged final focus snapshot cannot certify focus safety. Log focus events
continuously and preserve protocol traces on failure.

No output behavior, timeout workaround, focus-forcing production helper, portal,
GNOME extension, overlay, or silence-filter change is authorized by this handoff.
The evidence narrows the support claim; it does not silently redesign the driver.
GPU-free CI remains unproven: the launch used a passed-through render node even
with software rendering requested. The harness should report missing runtime
prerequisites distinctly and never substitute Sway.

## Retained integration result

The [permanent suite results](../research/gnome-clipboard-research.md#permanent-real-session-regression-suite)
now reproduce focus theft through the real dispatcher, not only its command.
Transfer and launch cancellation were verified, but focus-safe copy-only
acceptance remains red on Shell/Mutter 50.4. Production repair is outstanding;
no timeout workaround, helper activation, extension, or injection was added.

The [finalize report](../reports/gnome-headless-qa.md#finalize-repair-and-rerun)
records the external-supervisor cleanup repair and passing forced worker-death
coverage on real GNOME. Production ownership cleanup and focus safety are not
changed by test cleanup. Ctrl+V and GPU-free CI remain unverified.
