---
status: accepted
stage: BUILT
next: "Validate physical-monitor and full-login lifecycle limits before extending compatibility claims"
depends-on: [../research/gnome-hud.md]
---

# One HUD painter, two passive presentation backends

**Status:** Final acceptance measured 2026-10-02 on `f8c9f02`-based source.
Production presentation, shared storybooks, visual negatives and combined
clipboard/HUD evidence are recorded in [QA](../qa/gnome-hud-qa.md). This accepted
contract is retained with the coherent feature outcome; no release is claimed.

> **In short.** GNOME automatically displays the same dictation HUD as Sway,
> without taking input focus or requiring a Shell extension.

**Needs your ruling:** None for this scope. An extension or special user setup
requires the parent’s approval, not an implementation workaround.
**Reads with:** [research](../research/gnome-hud.md),
[implementation plan](../plans/gnome-hud-plan.md),
[serial owners](../tasks/gnome-hud-tasks.md).

## Production selection and visual behavior

The implementation uses a passive XWayland window, not another Shell artifact. The cost is an XWayland prerequisite and explicit monitor
coordinate handling; visual policy stays in the existing painter.

- **Automatic selection:** case-insensitive colon-separated GNOME desktop tokens
  choose XWayland; other desktops retain the existing Wayland selection. Missing
  desktop identity retains Wayland. No new config selector and no output-driver
  auto-selection. Session DISPLAY and authorization are prerequisites.
- **Same behavior:** use the existing Overlay methods and shared Go painter for
  pill, waveform, preview tail fitting, Transcribing, Error and Hidden. Reuse
  existing margin/preview defaults, cadence, level mapping and animation policy.
  Clear preview on leaving Recording; text received outside Recording does not
  become output or stale pixels on the next recording.
- **Hidden:** no visible pixels and no input region; a transparent mapped window
  is allowed. A fixed logical canvas reserves preview height, so words do not
  resize/recenter the window. Complete frames clear prior larger content.
- **Nonblocking producers:** latest visual/text wins; audio samples are bounded
  to the existing 64-sample limit and aggregated per rendered frame. One loop
  owns window resources. No unbounded event/image/update queues. During loss,
  setters retain desired state for retry; after Close they return a closed error.
  Invalid visuals are rejected. The child rejects levels outside finite [0,1];
  normal production audio continues through the existing decibel display mapping.
- **Passive input:** set non-focusable hints and an empty SHAPE input region
  before first map. Never request focus, activation, keyboard input or grabs.
  Alpha transparency alone is not pointer pass-through.
- **Stacking:** notification-type override-redirect alpha window; raise on first
  map/geometry rebuild, not every frame. Normal desktop and fullscreen app
  coverage must be tested. Shell overview, lock/login and secure prompts remain
  authoritative; do not promise a HUD above them or alter their focus behavior.

Here, XWayland is GNOME’s X11 compatibility server, not a typing backend.
Alpha format and override-redirect terminology are defined by the
[protocol sources](../research/gnome-hud.md#sources).

## Geometry, transport and failure

- Use the **primary monitor**, not pointer/focus following. Center within its
  current usable rectangle; y is usable-top plus configured margin in logical
  pixels. Never hard-code panel height. If there is no primary flag, choose the
  first logical monitor in stable position order; no monitor means hide.
- Match read-only Mutter DisplayConfig logical layout to X monitor/work-area
  coordinates, including negative origins, transforms, integer/fractional and
  mixed scales. Paint logical pixels and scale the premultiplied image for X.
  Ambiguous workspace or scale mapping means hide with a diagnostic, not guess.
- Coalesce workspace, work-area, monitor and scale changes; rebuild dimensions,
  position and backing storage atomically, then repaint the latest desired state.
  A primary monitor disappearing cannot leave a stale HUD at its old coordinates.
- Require a real direct alpha visual, SHAPE input support and local Unix DISPLAY.
  Reject malformed/remote DISPLAY and invalid authorization; never downgrade to
  opaque or input-catching windows. Use explicit XAUTHORITY, or the current
  user’s ~/.Xauthority only when unset; never bypass a failed authorization.
  No cookie scraping or other-session discovery.
- Constructor budget: **5 seconds total**. Each active synchronous transport
  operation has a **5-second maximum**; idle reads are not operation timeouts.
  Upload complete frames via reusable backing storage and one window copy,
  respecting X request limits rather than showing partially uploaded rows.
- On transport/compositor loss, close the old resources, preserve desired state,
  retry every **2 seconds**, and re-read only current session credentials.
  Geometry-only ambiguity triggers reread on change and the same retry cadence.
  Do not restart services or claim recovery into a different desktop session.
- Close is idempotent, force-unblocks transport, releases window/pixmap/GC and
  workers within **5 seconds**, and cancels retries. Partial startup also cleans up.
- Constructor failure keeps the daemon’s existing warning + Noop fallback;
  transcription/copy can continue. Runtime loss logs unavailable/recovery without
  false success. Explicit HUD tests fail on unavailable backend, never accept Noop.
- Doctor separately reports selected HUD backend and bounded actual capability
  probe result, including geometry/authorization reasons, independently of output.
  Capability success is not proof of displayed pixels or clipboard delivery.

## Frame acknowledgment contract

**Frame receipt** *(coined here)* means metadata from a frame processed by the
production draw/submission path, not compositor presentation or a screenshot.

Keep Overlay unchanged. Add an optional `FrameObserver` with
`SyncFrame(ctx context.Context) (FrameReceipt, error)` on both real backends.
It captures the current mutation counter (the number of accepted state changes)
and waits for a scheduled frame containing at least that revision; it must not make audio setters synchronous or reset cadence.
The receipt contains backend, revision, frame counter, actual painter Scene
(including waveform and phase), canvas dimensions, available backend coordinates
and scale, and status `submitted`. GNOME's Screen rectangle contains X protocol
coordinates, not screenshot coordinates. Wayland's Screen rectangle is empty:
layer-shell does not expose absolute placement. Capture drivers independently
measure HUD bounds in real compositor screenshots and record those coordinates
separately from production receipts. No compositor-specific position query is
added to production merely for report metadata. Context expiration, closed/lost backend and stale revisions
return errors. No receipt is labeled `presented` without actual compositor proof.
Scene slices are owned snapshots, not mutable renderer storage.

The backend-owned child uses bounded line-delimited JSON on stdin/stdout; stderr
is diagnostics. Maximum command size is 64 KiB. Commands carry a strictly
increasing `sequence`; duplicate/out-of-order or unknown commands fail explicitly.

| Command | Request | Successful response |
| :--- | :--- | :--- |
| `ready` | protocol version 1 | real selected backend and version; reject Noop |
| `apply` | sequence, visual, level or null, text or null | same sequence and status `queued`; calls only Overlay methods |
| `await_frame` | sequence, timeout_ms (1–5000) | same sequence, receipt covering all prior apply mutations |
| `close` | sequence | acknowledgment after Close completes; EOF also closes |

Visual values are `hidden`, `recording`, `transcribing`, `error`. An apply calls
Show only when changing visual, then supplied level/text; null means unchanged,
empty text clears preview. Only one request is outstanding per child. No scene
catalog, dispatcher, alternate painter or test window belongs in this child.

## Storybook parity and honest capture

One untagged shared Go scene/driver/validation/report package serves both suites.
Reuse the Sway template’s theme toggle, badges, filters, four viewing modes,
lightbox, crops, full frames and metrics; do not redesign either report.

Canonical order: `hidden`, `recording-00`, `recording-15`, `recording-35`,
`recording-55`, `recording-75`, `recording-100`, `transcribing`, `error`, then
`preview-short`, `preview-long`, `preview-cleared`. Use one steady-RMS schedule
and frame-aware driver: fifty acknowledged scheduled frames fill the waveform
window at each recording level. Setter calls alone are not waveform evidence.
Record receipts and validate actual waveform rather than assuming timing.
Correct Hidden and decibel-mapping descriptions in both reports.

- Both HUD desktops: 1920×1080, unchanged soft-focus wallpaper, native GTK editor
  targeting 900×500 at x=510, y≈300; report observed decoration differences.
  Derive 180-pixel top crops from real full frames only.
- Sway captures use Grim; GNOME captures use Shell Screenshot with cursor/flash
  disabled. Report only real compositor PNGs, never painter composites or annotations.
- Validate visible HUD in measured bounds against receipt-driven painter reference:
  label/dot/waveform, amber Transcribing, crimson Error, preview text/tail/ellipsis.
  Blend expected alpha against same-session Hidden baseline only for comparison;
  never export that reference as a screenshot. Exclude clock/caret from comparison.
- Capture once after a 100ms settling wait; fifty steady-RMS frames prevent
  waveform scrolling from changing the expected envelope. GNOME refreshes its
  submission receipt immediately before capture. This is not a presentation
  acknowledgment or a screenshot retry loop: any image mismatch fails closed.
  Negative fixtures must reject absent/wrong/stale HUD and preview.
  Compare decoded HUD-region pixels, not whole-frame hashes; legitimate repeated
  states are allowed, accidental reuse of distinct expected states is not.
- Verify preview → Transcribing/Error/Hidden and visible → Hidden → visible.
  Observe all Shell focus-window and GTK active events, and unchanged editor text.
  Deliberate pointer tests separately require actual click delivery underneath
  visible pill/preview and Hidden; focus deliberately changed by a fixture is
  excluded only during its declared setup, never across HUD updates.
- Parameterize compositor/version, output, panel, positioning, capture method and
  sibling link. GNOME labels cannot say Waybar, HEADLESS-1, Grim or layer-shell.
- Keep existing report destinations and ignored artifact directories. Move seven
  clipboard/Paste images to a separately named clipboard QA report at 1280×720;
  they are not HUD scenes and core scenes never invoke Paste or a dispatcher.

## Distribution, acceptance and non-goals

Pure-Go XGB plus a reviewed pure-Go D-Bus dependency join the normal build.
Existing sherpa directory distribution remains unchanged; no libX11, GTK production
helper, extension, cloud API or service is added. XWayland must be present and session
DISPLAY/XAUTHORITY must reach the daemon. Update service environment guidance and
upgrade docs; do not install, import host environment or restart host services here.
Any unavoidable special setup beyond ordinary session prerequisites goes to parent.

The measured compatibility target is pinned GNOME Shell/Mutter **50.4**; other
versions need equivalent evidence before compatibility is claimed.

Production acceptance covers focus/pointer, frame clearing, transport stalls/loss,
workspace/fullscreen/overview, hotplug, 2× and fractional/mixed scale with actual
Shell screenshots. Unsupported/ambiguous geometry is an honest unavailable result,
not a passing support claim. If real scale infrastructure is absent, retain durable
handoff and report the unverified configuration; do not invent captures.

No production typing changes, preview emission, wallpaper replacement, host changes,
concurrent writers, child delegation or component commits. CPU recovery remains.
Native wl-copy focus failures remain diagnostics outside supported acceptance.

The old [GNOME proposal](porting-to-gnome.md) says an extension is the only HUD
route. This measured candidate supersedes that HUD recommendation, not its separate
injection proposal; the integrator reconciles obsolete shipped documentation only
after production acceptance. That reconciliation and final measured completion
are recorded in QA; this design does not claim a release.
