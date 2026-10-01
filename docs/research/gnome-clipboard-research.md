---
status: accepted
---

# GNOME clipboard output: implementation and live findings

## Verdict

**Adopt explicit X11 clipboard output for the measured GNOME case.** Verified
2026-10-01 against uncommitted changes on `10139e1`: the real production
foreground-owner dispatcher passes native GTK reads, continuous focus checks,
mature overview/no-focus copying, PRIMARY preservation and editable Paste action
on isolated Shell/Mutter 50.4 with xclip 0.13. This is not complete desktop support.

The original Wayland diagnostics remain valid: wl-copy transfers text but
briefly steals focus. Both focused and overview diagnostics were rerun and still
fail their original focus assertions. Deadline/forced-death cleanup pass.
Earlier setup/JSON-decoding failures were not clipboard failures. See
[production evidence](../qa/gnome-clipboard-qa.md#production-x11-verification).

## Existing output architecture

Source reviewed 2026-10-01:

- [Output selection](../../cmd/mavor/main.go) selects clipboard before the native
  constructor, which requires layer-shell through its Wayland connection.
- [Paste output](../../internal/output/paste.go) owns CLIPBOARD and PRIMARY only
  briefly, injects a chord, and restores selections. Reuse is rejected for
  delayed manual paste; clipboard has a separate dispatcher.
- [Wayland clipboard output](../../internal/output/clipboard.go) launches ordinary
  backgrounding wl-copy with plain UTF-8 text, without paste-once.
- [X11 clipboard output](../../internal/output/x11_clipboard.go) supervises a
  foreground xclip owner. The launch deadline does not expire successful ownership;
  replacement/shutdown/connection loss reap it. No keys are injected.
- [DefaultRunner](../../internal/output/output.go) avoids inherited-pipe waits
  for daemonized wl-copy. Preserve this behavior; do not capture child
  stderr/stdout through pipes. A launch deadline is not clipboard readiness.

## Fast-moving — verify before building

Measured versions: GNOME Shell **50.4**, Mutter **50.4**, wl-clipboard **2.3.0**,
GTK **4.22.4**, Mesa **26.2.3**, wayland-utils **1.3.0**. Nix registry resolved
nixpkgs revision `b6c8664de9b6cc07fe5666a29f91884ba81197c4`.

Installed Shell and Mutter help both expose `--headless`, `--wayland`,
`--no-x11`, `--wayland-display`, and `--virtual-monitor`. Source inspection of
Mutter 50.4 also verified the hidden `--unsafe-mode` switch, used only to inspect
and control focus through Shell's private-session `org.gnome.Shell.Eval` method.
Do not enable it on a user's desktop.

The measured registry exposes `wl_data_device_manager` version 3,
`zwp_primary_selection_device_manager_v1`, and `xdg_wm_base`. It does **not**
expose either `zwlr_data_control_manager_v1` or `ext_data_control_manager_v1`,
layer-shell, or virtual-keyboard. Registry claims apply to this launch/version,
not all GNOME versions or privileged connections.

## Reproducible isolated startup

Transient packages were acquired with Nix, without editing jail configuration:

```bash
base=/workspace/.yolo/durable/gnome-headless
nix build nixpkgs#gnome-shell nixpkgs#mutter --out-link "$base/gnome-packages"
nix build nixpkgs#mesa --out-link "$base/mesa"
nix build nixpkgs#gtk4.dev nixpkgs#wayland-utils --out-link "$base/tools"
```

The successful experiment creates private HOME, config, cache, data, and a
mode-0700 runtime directory. It removes inherited DISPLAY, WAYLAND_DISPLAY,
DBUS_SESSION_BUS_ADDRESS, SWAYSOCK, GI_TYPELIB_PATH, GIO_EXTRA_MODULES, and
LD_LIBRARY_PATH. Both session and system bus addresses point to a **real private
D-Bus daemon** using the following minimal configuration; no system-service
implementations are faked, and no host bus is contacted:

```xml
<busconfig>
  <type>session</type>
  <listen>unix:dir=PRIVATE_RUNTIME_DIRECTORY</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*"/>
    <allow receive_sender="*"/>
    <allow own="*"/>
  </policy>
</busconfig>
```

Launch environment and command, in addition to the private XDG paths and buses:

```bash
export XDG_SESSION_TYPE=wayland XDG_CURRENT_DESKTOP=GNOME
export GSETTINGS_BACKEND=memory LIBGL_ALWAYS_SOFTWARE=1
export GBM_BACKENDS_PATH="$base/mesa/lib/gbm"
export LIBGL_DRIVERS_PATH="$base/mesa/lib/dri"
export __EGL_VENDOR_LIBRARY_FILENAMES="$base/mesa/share/glvnd/egl_vendor.d/50_mesa.json"
"$base/gnome-packages/bin/gnome-shell" \
  --headless --wayland --no-x11 --virtual-monitor=1280x720 \
  --wayland-display=gnome-test --unsafe-mode
```

Nix's Shell wrapper supplies its own GSettings schemas and introspection paths;
it was not necessary to hand-assemble them. The successful log reports an
amdgpu render device at `/dev/dri/renderD128`, a GBM renderer, and virtual output
`Meta-0` at 1280×720/60 Hz. Although software rendering was requested, the
actual GL renderer name was not measured; this is **not proof of GPU-free CI**.
The backend uses no mode setting and does not take over the host display.

A fresh headless seat has **no keyboard capability**. Create a real internal
Mutter virtual input device using Eval, retaining it globally for the test:

```javascript
globalThis.testKeyboard = global.stage.context.get_backend()
    .get_default_seat().create_virtual_device(1);
```

The numeric value 1 is `CLUTTER_KEYBOARD_DEVICE` in the inspected 50.4 enum.
The next registry round trip reports `seat0` with keyboard capability. No
Wayland virtual-keyboard extension, fake keyboard executable, or physical
input device is used. The shell's own on-screen keyboard uses the same
virtual-device API.

### Startup dead ends and their repairs

| Observed failure | Verified repair |
| :--- | :--- |
| Shell/Mutter `--help` aborts with stack-smashing detection | Remove jail's inherited LD_LIBRARY_PATH before running this Nix closure. |
| Default dbus-run-session reports configuration needs a listen element | Supply a valid bus config explicitly; final probe uses dbus-daemon with a private Unix socket. |
| Missing `/run/opengl-driver/lib/gbm/dri_gbm.so`, then no Clutter drivers | Set the three Mesa driver/vendor paths above. |
| Shell UI throws in TimeLimitsManager because system bus is absent | Set DBUS_SYSTEM_BUS_ADDRESS to the isolated bus as well. Missing service names then produce warnings, not fatal connection failure. |
| wl-copy exits 1: `This seat has no keyboard` | Create the retained Mutter virtual keyboard before clipboard tests. |

Shell still reports absent GDM, PolicyKit, calendar, accessibility, settings,
GeoClue, and other desktop services. It nevertheless logs `GNOME Shell started`,
answers Eval, serves the registry, focuses clients, and transfers text. These
warnings must remain visible, rather than being described as a full login test.

## Clipboard observations

The experiment launches the **actual dispatcher command**, feeding stdin and
redirecting stdout/stderr to files, not pipes:

```bash
wl-copy --type 'text/plain;charset=utf-8'
```

The consumer is a compiled GTK4 program forced to `GDK_BACKEND=wayland`. It
opens a toplevel, logs `notify::is-active`, and calls
`gdk_clipboard_read_text_async` every two seconds. These are actual native
selection transfers, not clipboard reads through Shell's privileged Eval and
not injected Ctrl+V events.

| Case | Measured outcome |
| :--- | :--- |
| Overview visible, no focused window | After three seconds wl-copy parent is still running; overview remains visible. Consumer later has no compatible transfer format. |
| Focus consumer, but leave earlier wl-copy helper unfocused | Still no text; merely focusing the destination does not unblock the pending source. |
| Explicitly focus pending `wl-clipboard` window | Keyboard enter arrives about ten seconds after launch, then set_selection; refocusing consumer yields `first no focus α` repeatedly. This is a diagnostic control, not background-mode success. |
| Launch with consumer already focused, overview hidden | Parent exits 0 within three-second observation window; consumer logs active 1 → 0 → 1 and reads `second focused consumer β`. |
| Delayed and repeated reads | Second text remains readable for at least twelve seconds after parent exit, across repeated native reads and destination reactivation. |
| Successive copy | Third launch exits 0, causes another focus loss/return, replaces text with `third repeated γ`; repeated reads continue. |

The focused-launch protocol trace contains a transient `xdg_toplevel` titled
`wl-clipboard`, `wl_keyboard.enter`, then `wl_data_device.set_selection`.
Data-source send events continue after launch parent exit. Mutter's inspected
selection handler cancels sources from clients other than the focused client;
its focus check corroborates the measured fallback behavior.

**Do not conflate eventual focus restoration with no focus theft.** Before/after
snapshots show the same destination window, but continuous GTK focus events and
Wayland protocol traces expose the intervening helper focus.

## Evidence and teardown

Durable experimental artifacts (outside the Git tree):

```text
/workspace/.yolo/durable/gnome-headless/
  nix-build.log, tools-build.log, mesa-build.log, nixpkgs-metadata.json
  startup-probe.py, probe.py, focus-probe.py, consumer.c, consumer
  run5-results.log, run6-results.log
  run6/shell.log, bus.log, consumer.log
  run6/no-focus.log, focused-consumer.log, repeated-copy.log
  mutter-50.4/, gnome-shell-50.4/   # inspected upstream release source
```

Final probe terminates copy process groups (including forked holders), consumer,
Shell process group, and private bus, and waits for tracked processes. Shell
logs shutdown and virtual-monitor removal. A post-probe process listing found
no probe Shell or consumer; an unrelated existing PRIMARY wl-copy was left
untouched. The experimental scripts use fixed waits and are evidence, **not a
production-quality reusable harness**; the handoff requires readiness polling
and unconditional cleanup on every failure path.

## Sources

- [Mutter 50.4 release source](https://download.gnome.org/sources/mutter/50/mutter-50.4.tar.xz)
  — inspected 2026-10-01: headless options in core/meta-context-main.c, focused
  client selection check in wayland/meta-wayland-data-device.c, input-device enum.
- [GNOME Shell 50.4 release source](https://download.gnome.org/sources/gnome-shell/50/gnome-shell-50.4.tar.xz)
  — inspected 2026-10-01: virtual keyboard creation in ui/keyboard.js and required
  system-bus access in misc/timeLimitsManager.js.
- [wl-clipboard manual](https://man.archlinux.org/man/wl-clipboard.1.en)
  — defines its focus-dependent fallback when the compositor lacks data-control;
  the live traces above independently demonstrate it.
- [Wayland data-device protocol](https://wayland.freedesktop.org/docs/html/apa.html#protocol-spec-wl_data_device)
  — defines selection ownership and offers, distinct from successful process launch.
- [GNOME design handoff](../design/gnome-clipboard-design-plan.md#real-gnome-harness-handoff)
  — acceptance boundary for turning this probe into permanent integration tests.

## Permanent real-session regression suite

Verified 2026-10-01: the [GNOME suite](../../test/gnome/clipboard_test.go)
now invokes the actual `NewClipboard` dispatcher with its default runner.
`just test-gnome -v` explicitly opts in; missing tools, failed startup, and
failed acceptance assertions are errors, never skips. `just check` type-checks
this suite but does not claim live GNOME coverage.

The [native GTK client](../../test/gnome/client/consumer.c) performs native selection
reads requested over its stdin control channel. It does not exercise an editable
widget, a Paste action, or Ctrl+V. No key
injection is used. A second GTK client owns a PRIMARY sentinel so the consumer
reads PRIMARY over Wayland rather than from its own local GTK clipboard cache.
The private Shell observes focus continuously; its unsafe Eval creates a seat
keyboard and sets test destination focus, never clipboard contents, helper
focus, injected keys, or portal consent.

Measured with Shell/Mutter **50.4**, GTK **4.22.4**, wl-clipboard **2.3.0**:

- Focused destination: two successive UTF-8 transcripts, delayed reads, repeated
  reads, destination reactivation, and unchanged PRIMARY all complete. Acceptance then **fails** on the
  destination's intervening focus loss.
- Initial overview/no focused window: in these fresh sessions copying completed
  and the initial transcript was later consumed natively, unlike the earlier
  long-running probe. Continuous Shell events expose transient `wl-clipboard`
  focus even though the final overview/focus snapshot is unchanged. This case
  also **fails**, not a background-copy certification.
- Frozen private compositor: real wl-copy is canceled by the production
  three-second launch deadline. The test resumes Shell before teardown and
  **passes** the deadline assertion.
- Failure and success teardown: the private worker group is terminated; tracked
  children and adopted forked selection holders are reaped. An external supervisor
  now survives worker SIGKILL and completes cleanup; a real GNOME forced-death
  run passes after native transfer. Direct supervisor SIGKILL is not covered. Logs and native
  Wayland protocol traces are retained in each printed diagnostics directory.

The retained failures precede any production repair. No production strategy
change was made: Mutter rejects selection ownership from an unfocused client
and this session advertises no data-control protocol. Longer timeouts, focusing
its helper, or injecting keys do not repair focus-safe copy-only semantics.
These assertions remain diagnostics for the Wayland backend. The separate
X11 backend passes the bounded acceptance below; do not convert the Wayland
assertions into expected successes or generalize this to all GNOME sessions.

### Run without a jail restart

Use the transient closure paths acquired above; normal `nix shell` does not set
GTK's compiler dependency paths, so use `nix develop` for the consumer build:

```bash
base=/workspace/.yolo/durable/gnome-headless
export MAVOR_GNOME_SHELL="$base/gnome-packages/bin/gnome-shell"
export GBM_BACKENDS_PATH="$base/mesa/lib/gbm"
export LIBGL_DRIVERS_PATH="$base/mesa/lib/dri"
export __EGL_VENDOR_LIBRARY_FILENAMES="$base/mesa/share/glvnd/egl_vendor.d/50_mesa.json"
env -u LD_LIBRARY_PATH nix develop --impure --expr '
  let p = import (builtins.getFlake "nixpkgs").outPath {};
  in p.mkShell { packages = [ p.gtk4 p.pkg-config p.gcc p.glib ]; }
' --command just test-gnome -v
```

Prerequisites: Python 3, Linux process-group/subreaper support, dbus-daemon,
GNOME Shell, gdbus, GTK4 development headers, pkg-config, cc, actual wl-copy,
and working Mesa driver paths. Optional overrides are `MAVOR_GNOME_SHELL` and
`MAVOR_GNOME_ARTIFACTS` (the latter chooses retained diagnostics). In this jail
the default artifacts live under the durable GNOME experiment directory;
elsewhere they live in the user's cache. No baked dependencies were added while
acceptance remains red; no human restart was necessary for these executions.

Ordinary output unit tests and the existing real-Sway clipboard persistence
test pass. `just check` passes independently of the live GNOME failures.
GPU-free CI is still unproven. Full login/logout, screen locking, daemon/audio
and model lifecycle, physical shortcut activation, and physical Ctrl+V remain
outside this dispatcher-level suite. Native selection reads do not
claim application Paste acceptance or complete-desktop lifecycle checks.

Finalize evidence and cleanup limits are recorded in the
[independent report](../reports/gnome-headless-qa.md#finalize-repair-and-rerun).
The original Wayland run still fails focused and overview acceptance; deadline
and forced worker-death cleanup pass. X11 acceptance is a separate result below.


## Focus-safe XWayland production result

**Measured 2026-10-01:** the recovered foreground-xclip proof passes, followed by
production dispatcher acceptance. XWayland is the X server inside Mutter; its
selection bridge offers X11 CLIPBOARD contents to native Wayland clients.
CLIPBOARD and PRIMARY are separate selections; see the
[X selection conventions](https://www.freedesktop.org/wiki/Specifications/ClipboardsWiki/).
No native helper surface needs focus, unlike the wl-copy fallback.

**Re-analyzed from source:** xclip 0.13 calls XSetSelectionOwner before its
foreground request-loop message. That message precedes the event-loop flush and
is not server-confirmed readiness. Ordinary silent-mode xclip forks at this
point, which would hide owner lifecycle from mavor. Adopt quiet foreground mode
and retain native consumer reads as proof of transfer. Source:
[xclip 0.13](https://github.com/astrand/xclip/blob/0.13/xclip.c).

Retained Mutter 50.4 source confirms that XFixes selection-owner notifications
install the X11 source through the selection bridge, and that its clipboard
manager can save/restore text on owner loss. Measured GTK offers returned exact
text six seconds after production Close; this is bounded observation, not
indefinite persistence or a promise for clipboard managers disabled.

The recovered Eval decoder parses the D-Bus tuple and JSON exactly once for
direct JavaScript values. Shell already serializes the result; adding
JSON.stringify double-encodes it. Mature overview may retain focus_window despite
its stage-input grab. The test clears destination focus explicitly after the
animation, never focuses the owner, and verifies no subsequent focus events.

Production runs cover delayed/repeated/replacement native consumption, unchanged
external PRIMARY, ten-second delayed consumption, editable GTK Paste action,
Close and post-Close retention. Startup deadline testing must freeze the private
**XWayland server**: freezing Shell alone does not stop an existing X11 server,
and xclip can still enter its request loop. Abrupt production-process death kills
the owner through Linux parent-death signaling; the test subreaper reaps it.
Compositor loss also ends ownership and the production wait goroutine reaps it.

Use the [hash-pinned Nix environment](../../test/gnome/environment.nix), containing
the measured nixpkgs revision rather than today's registry. Reproduction:

```bash
env -u LD_LIBRARY_PATH nix develop --impure \
  --file test/gnome/environment.nix --command just test-gnome-x11 -v
# Original focus diagnostics remain intentionally failing on measured GNOME:
env -u LD_LIBRARY_PATH nix develop --impure \
  --file test/gnome/environment.nix --command go test -tags=gnome \
  ./test/gnome -run '^TestGNOMEClipboard$' -count=1 -v -timeout=130s
```

[QA](../qa/gnome-clipboard-qa.md#production-x11-verification) identifies the
retained logs and known limits. No jail edit, restart, extension, production
key injection, host desktop, deployment, or silence-filter change was used.
