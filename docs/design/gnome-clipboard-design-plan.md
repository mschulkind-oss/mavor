---
status: accepted
stage: DECIDED
next: "Parent review, landing, and integration of the measured X11 backend"
---

# Clipboard is an explicit choice, never a fallback

**Status:** 2026-10-01, uncommitted changes against `10139e1`. MEASURED:
production X11 dispatcher passes isolated Shell/Mutter 50.4 acceptance;
Wayland focus diagnostics still fail. See [QA](../qa/gnome-clipboard-qa.md).

> **In short:** manual paste needs a persistent selection owner, not synthetic
> keys. On measured GNOME, XWayland supplies that ownership without helper focus.

**Needs your ruling:** None; the supplied implementation phase authorizes the
explicit backend after real proof. Parent review/integration is still owed.
**Reads with:** [research](../research/gnome-clipboard-research.md),
[tasks](../tasks/gnome-clipboard-tasks.md), [QA](../qa/gnome-clipboard-qa.md).

## Behavior and compatibility

Keep `[output] driver = "paste"` as default. Clipboard mode is explicit;
no compositor detection or automatic fallback enables it. Within clipboard mode,
`clipboard_backend = "wayland"` is the default, preserving ordinary wl-copy.
`clipboard_backend = "x11"` explicitly selects xclip; reject unknown values
and reject X11 with paste or typing rather than silently ignoring it.

Both clipboard backends normalize whitespace, skip empty transcripts, and pass
UTF-8 text on stdin to CLIPBOARD only. CLIPBOARD is the ordinary copy selection;
PRIMARY is the independent selected-text selection, as described by the
[X selection conventions](https://www.freedesktop.org/wiki/Specifications/ClipboardsWiki/).
Neither backend injects keys, focuses helpers, reads/restores selections, or
changes PRIMARY. Copy regardless of `output.clipboard`; ignore paste/typing knobs.
Leave paste, typing, overlay fallback, history ordering, and silence filtering alone.

## X11 ownership lifecycle

XWayland is the X server inside a Wayland compositor. Its selection bridge makes
X11 selection offers available to native Wayland clients. This is not X11
injection, an X11 overlay, or complete GNOME desktop support.

Use foreground xclip with CLIPBOARD, UTF8_STRING and unlimited requests.
Successful ownership has **no lease expiration** and survives cancellation of
the completed Emit context. Allow delayed/repeated manual paste until external
replacement, connection loss, a new successful Emit, or dispatcher Close.

- **Launch:** a three-second deadline includes waiting for serialized Emit
  access. Earlier cancellation wins. A bounded stderr acknowledgment says
  xclip entered its request loop; it does not prove native transfer or server
  acceptance. Consumer reads are the acceptance evidence.
- **Failure/cancellation:** kill and wait for the attempted owner. Return a
  wrapped error; never inject, auto-retry, or fall back to wl-copy. Keep the
  previous tracked owner on failed launch, although a partially completed
  selection replacement cannot be rolled back safely.
- **Replacement:** start the new owner before killing/waiting for the previous
  tracked one. Naturally exited children are reaped by their wait goroutines.
- **Shutdown:** daemon selection wiring registers Close, which serializes with
  Emit, kills/waits for the last owner, and prevents subsequent nonempty Emits.
  Linux parent-death signaling kills the owner on abrupt daemon death as well.
- **Connection loss:** xclip exits and is reaped; no implicit reconnect or
  retention promise. A subsequent Emit reports its own launch result.

Normal shutdown relinquishes ownership. Mutter's clipboard manager retained text
six seconds after Close in the measured session, but mavor does not promise that
another compositor or clipboard-manager configuration will preserve it.

## Requirements and diagnostics

For X11 clipboard output, setup installs xclip rather than wl-copy; doctor checks
xclip, DISPLAY and a readable nonempty authorization file (XAUTHORITY, or the
standard home-directory file). These checks do not prove the server accepts the
cookie. Launch errors remain visible in daemon logs. No helper check writes a
selection. Wayland clipboard mode still requires wl-copy; paste/typing keep their
existing dependencies. History copying remains Wayland-based and is not a tested
X11 recovery path.

A daemon launched without its desktop's DISPLAY/XAUTHORITY cannot use this backend.
Do not copy credentials or discover them through host access. The private tests
obtain only their own Shell's environment through test-only Eval.

## Real GNOME harness handoff

Use the [pinned test environment](../../test/gnome/environment.nix) and
[production X11 harness](../../test/gnome/x11_harness.py), separately from Sway.
Missing prerequisites are failures, never substitutes or skips. Private HOME,
XDG directories, session/system buses and environment removal prevent host access.
Unsafe Shell Eval is enabled only in this private session to establish/observe
focus and keyboard capability, never selections, helper focus, or production keys.

Acceptance requires continuous GTK active-state and Shell focus-event assertions,
not only final focus snapshots. Mature overview testing waits for Shell startup
and animation completion, then explicitly clears **destination** focus to establish
no-focus state. No selection owner is activated. Delayed/repeated/replacement
reads and an editable GTK Paste action must consume native offers. The Paste
action is test input, not production injection. Preserve external PRIMARY reads,
owner persistence/Close, frozen-XWayland launch cancellation, connection loss,
abrupt owner-parent death, and supervisor cleanup checks.

Original [wl-copy diagnostics](../../test/gnome/harness.py) remain accessible and
unmodified: focused and overview acceptance still fail on helper focus. They
must not become expected-success tests or have their assertions weakened.

## Boundaries and trade-offs

No portals, extensions, GNOME HUD, notifications, automatic injection, host
session access, audio/model changes, or production deployment. X11 adds a
selection bridge and authorization dependency; Wayland remains preferable on
compositors with working data-control protocols. The wl-copy fallback is rejected
for focus-safe GNOME delivery based on real continuous-focus failures, not a
startup assumption. A successful launch is not a successful paste claim.

Complete login/logout, lock/unlock, physical shortcuts/Ctrl+V, daemon/audio/model
lifecycle and clipboard-manager configurations remain unverified. Software
rendering was requested but Shell selected amdgpu; GPU-free CI is unproven.
Parent owns final review, repairs, commit and integration. Graduation into a
system reference follows landing, not this uncommitted implementation phase.
