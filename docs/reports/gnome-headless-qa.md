---
status: accepted
date: 2026-10-01
summary: "Independent real Mutter rerun confirms selection transfer and rejects focus-safe background copying."
---

# Independent real GNOME headless verification

## Verdict

**Blocked: focus-safe background copying fails on real GNOME Shell/Mutter
50.4.** The independent recipe run reproduced both retained focus failures;
this is not verified GNOME support. Native Wayland clipboard transfer and the
production launch deadline work in the tested isolated session.

The independent review inspected the dirty worktree at `c9fadca1d5a4144f8514521646c29f6e2021ecc6`:
the [Justfile](../../Justfile), [Go suite](../../test/gnome/clipboard_test.go),
[Python harness](../../test/gnome/harness.py), [GTK consumer](../../test/gnome/client/consumer.c),
and revised [research](../research/gnome-clipboard-research.md),
[design](../design/gnome-clipboard-design-plan.md), and
[tasks](../tasks/gnome-clipboard-tasks.md). That review changed only this report. The finalize pass below repairs test
cleanup and records another real run; production code, jail configuration,
host services, branches, and child-agent delegation remain unchanged.

## Independent execution

Ran the documented transient Nix environment from this worktree, not the main
checkout. Existing package links resolved successfully; no restart was needed.

```bash
base=/workspace/.yolo/durable/gnome-headless
export MAVOR_GNOME_ARTIFACTS="$base/independent-qa"
export MAVOR_GNOME_SHELL="$base/gnome-packages/bin/gnome-shell"
export GBM_BACKENDS_PATH="$base/mesa/lib/gbm"
export LIBGL_DRIVERS_PATH="$base/mesa/lib/dri"
export __EGL_VENDOR_LIBRARY_FILENAMES="$base/mesa/share/glvnd/egl_vendor.d/50_mesa.json"
env -u LD_LIBRARY_PATH nix develop --impure --expr '
  let p = import (builtins.getFlake "nixpkgs").outPath {};
  in p.mkShell { packages = [ p.gtk4 p.pkg-config p.gcc p.glib ]; }
' --command just test-gnome -v
```

| Scenario | Independent result | Evidence |
| :--- | :--- | :--- |
| Focused destination | FAIL, 6.28 seconds | Both transcripts read repeatedly and after reactivation; copy causes active 1 → 0 → 1. |
| Overview/no destination focus | FAIL, 6.01 seconds | Initial copy completes and is later read, but Shell records transient `wl-clipboard` focus. |
| Frozen compositor | PASS, 4.79 seconds | Dispatcher child fails after 3.00 seconds with `signal: killed` and `context deadline exceeded`; Shell resumes for teardown. |

Overall suite: **FAIL**, 17.077 seconds, recipe exit 1. No scenarios skipped.
The earlier diagnostic probe's overview stall is not reproduced in this fresh
session: initial ownership completes through transient helper focus. Both
observations reject focus-safe background delivery; they are not interchangeable
claims about startup timing.

Durable evidence, outside the Git tree:

```text
/workspace/.yolo/durable/gnome-headless/independent-qa/
  recipe.log
  focused-rlp6zwy4/
  overview-zh5a4k5w/
  deadline-ejmezjzb/
  done.log
  unit.log
```

## What actually ran

Shell logs explicitly report `Running GNOME Shell (using mutter 50.4) as a
Wayland display server`. The resolved Shell is the Nix `gnome-shell-50.4`
closure; independent dynamic-library inspection resolves `libmutter-18.so.0`
and its supporting libraries to `mutter-50.4`. The actual copy executable is
Nix `wl-clipboard-2.3.0`, reached through `/bin/wl-copy`. There is no Sway
launch, substitute clipboard executable, or selection implementation in Eval.

The child Go test calls [NewClipboard](../../internal/output/clipboard.go) and
its real default runner. It does not set a fake Runner. Production cleaning,
plain UTF-8 arguments, process launch, and three-second launch timeout execute.
The daemon, audio capture, transcription, config loading, and output-driver
selection are **not** part of this live test.

The compiled GTK client is forced to native Wayland. Consumer traces advertise
`wl_data_device_manager` and show actual `wl_data_offer.receive` requests for
every delayed, repeated, and reactivated read, followed by the expected UTF-8
text. Thus these observed reads are not merely GTK-local cached contents.
A separate GTK process seeds PRIMARY; the destination's primary-selection
receive requests and unchanged sentinel verify transfer from that external
owner, not a local cached PRIMARY value.

**Simulated manual selection consumption is not application Ctrl+V.** Stdin
commands directly call `gdk_clipboard_read_text_async`. The window contains a
label, not an editable widget; no Paste action, Ctrl+V binding, inserted text,
physical shortcut, or keyboard injection is exercised. This establishes the
selection transfer that a Paste action would request, not user-visible paste
acceptance in a real application.

## Focus and rendering assumptions

Eval runs only in the private unsafe-mode Shell. It creates an internal Mutter
virtual keyboard so the otherwise keyboard-less headless seat can copy; it
activates test destinations and observes focus, but never focuses the helper
or supplies clipboard data. The keyboard receives no injected keys. This is
real compositor input capability, but it is not a physical login-session seat.

Both GTK active-state events and Shell focus logs expose transient helper
activation. Final restored focus does not prove uninterrupted focus. The
focused scenario excludes intentional destination reactivation from its copy
interval. The overview scenario also checks the initial interval's Shell log,
not merely the final overview/focus snapshot.

Headless here means no physical output or mode setting, **not no GPU**. Shell
logs select `/dev/dri/renderD128` (amdgpu), create a GBM renderer, and identify
that render device as primary. `LIBGL_ALWAYS_SOFTWARE=1` is requested, but the
actual renderer is not asserted and GTK traces also contain Mesa Vulkan
activity. There was no run with render devices inaccessible. GPU-free CI
remains unproven; missing executables are not an inherent runtime blocker,
since transient Nix packages demonstrably execute the test now.

Readiness polls the Wayland socket and successful Shell Eval; it does not wait
for the logged `GNOME Shell started` message. In the overview run, initial copy
occurred before that message. This is valid evidence of a startup-session
failure, but later steady-session overview behavior needs a separate test
before claiming the startup race and mature desktop behavior are identical.

## Isolation and teardown

Each run uses private HOME, config/cache/data paths, and a mode-0700 runtime.
Both bus addresses point to its actual private dbus-daemon, configured without
host service activation directories. Inherited display/socket/bus addresses
and conflicting library/introspection variables are removed. Missing GDM,
PolicyKit, accessibility, settings, and other desktop services remain visible
in Shell logs: this is not a complete GNOME login environment.

All three runs print successful tracked-child and adopted-holder reaping.
The final harness shares one private worker process group, including forked
wl-copy holders. An external supervisor adopts and waits for descendants if
the worker dies; normal worker cleanup also adopts and waits for holders. Post-run process inspection found no test
Shell, harness, consumer, or copy holder. The pre-existing unrelated bus
PID 31102 and PRIMARY holder PID 31113 remained untouched. Private Shell logs
show shutdown. This supports successful teardown for this execution, not a
guarantee against every interruption.

### Remaining blocking and coverage issues

1. **Production focus safety is unresolved.** Both independent acceptance cases
   fail. Do not convert these assertions to expected success or advertise
   background GNOME copying as verified without a permitted strategy and rerun.
2. **Forced worker death is now covered, not unconditional teardown.** The
   finalize pass adds an external supervisor, a private shared worker group,
   graceful Go cancellation, and a Linux parent-death SIGTERM notification.
   The supervisor kills the group and waits for adopted descendants even if
   the worker receives SIGKILL. Real GNOME and process-only regressions pass.
   Direct SIGKILL of the supervisor itself, escaped process groups, and a
   kernel task that cannot finish termination remain outside this guarantee.
   Parent-death notification is configured but not independently exercised.
3. **Cancellation coverage is narrower than ownership lifecycle coverage.**
   Freezing Shell proves startup cancellation with a pending real helper; it
   does not prove compositor-loss recovery, logout/lock behavior, or cleanup of
   established holders by production code. Test cleanup deliberately kills
   owners and must not be confused with daemon lifecycle management.
4. **Source-helper protocol diagnostics are absent.** Consumer Wayland traces
   and Shell focus logs are retained. Although the harness sets WAYLAND_DEBUG,
   production DefaultRunner discards wl-copy stdout/stderr, so dispatcher logs
   contain Go test results, not helper wire events. Do not claim complete source
   protocol capture. Overview's initial focus evidence is a log-file snapshot;
   an explicit completion barrier would make it more robust than assuming log
   writes are already visible.
5. **GPU-free execution and complete application acceptance remain open.**
   Establish a renderer path without accessible GPU nodes and exercise a real
   editable application's Paste action, plus mature-session overview and full
   desktop lifecycle, before broadening the support claim.

## Gates, configuration, and package persistence

`just done` independently passed: format check, vet, staticcheck, tagged-suite
vet including GNOME, and ordinary tests; output ends `Quality gate passed.
Ready to commit.` This does **not** execute live GNOME. The independent live
recipe is deliberately opt-in, uses `-count=1`, and remains red. The ordinary
parent invocation of `TestDispatcherChild` returns immediately and is reported
PASS; only harness child invocations with the explicit environment execute
production output. Missing prerequisites fail rather than skip. Test filtering
can still select only that inert helper, so its PASS alone is not evidence.

Uncached `go test -count=1 ./internal/output ./internal/config ./cmd/mavor`
passed. Existing tests verify clipboard config round-trip, rejection of unknown
driver names, selection before native keyboard construction, and driver-aware
tool checks. Tool-availability tests use fixture executables and prove only
configuration/diagnostic behavior; they are not the real GNOME evidence above.
The live harness bypasses config validation entirely.

The recipe requires Python, Linux subreaper support, actual wl-copy, dbus-daemon,
gdbus, GNOME Shell, GTK4 development inputs, a C compiler, and working renderer
paths. Plain jail `pkg-config --modversion gtk4` fails outside the Nix development
environment; the documented environment repairs this and the real recipe runs.
No GNOME package provisioning was added to the jail or CI. Existing CI runs
Sway integration only, so its green result would not certify GNOME.

Transient package links and diagnostics survive in durable storage, but these
jail-created Nix output links are not host-honored garbage-collection roots.
Rebuild dangling links after host collection. The documented unpinned nixpkgs
registry expression can change dependency versions; preserve the measured
closure/version evidence or pin a reproducible environment when adopting CI.
No jail configuration was edited, so no jail validation/restart was required.

This report was checked with `uvx vantage-check`. The installed checker prints
the older style-guide vocabulary; no planning-index verification or browser
render inspection is claimed.

## Finalize repair and rerun

The [supervisor](../../test/gnome/supervise.py) owns cleanup outside the worker
it terminates. [Lifecycle regressions](../../test/gnome/test_supervise.py)
exercise worker SIGKILL and supervisor SIGTERM, including a forked child that
ignores SIGTERM; both child PIDs disappear after supervisor exit. These tests
use real process lifecycles, not substituted clipboard commands. Run them
without GNOME using:

```bash
PYTHONDONTWRITEBYTECODE=1 go test -tags=gnome ./test/gnome -run TestHarnessCleanup -count=1 -v
```

The final real recipe uses the environment in [Independent execution](#independent-execution),
with `MAVOR_GNOME_ARTIFACTS` set to the durable `finalize` directory and
`PYTHONDONTWRITEBYTECODE=1`. Shell again reports GNOME Shell/Mutter **50.4**;
wl-clipboard reports **2.3.0**. Shell launches with `--headless --wayland
--no-x11 --virtual-monitor=1280x720 --wayland-display=mavor-gnome-test
--unsafe-mode` on a private bus/runtime. Consumer protocol traces retain native
`wl_data_offer.receive` requests and exact UTF-8 readback.

| Scenario | Final result | Diagnostics directory |
| :--- | :--- | :--- |
| Focused destination | FAIL, 6.48 seconds; native transfer completes, focus is stolen | `focused-vbnkr4u8` |
| Overview | FAIL, 7.02 seconds; initial native read completes, helper briefly focuses | `overview-qrfz72y2` |
| Frozen compositor | PASS, 5.11 seconds; production deadline cancels pending helper | `deadline-pg80gble` |
| Forced worker death | PASS, 4.38 seconds; transfer/PRIMARY read precede SIGKILL, supervisor reaps group | `forced-1tscelpa` |

Overall live suite remains **FAIL**, 23.470 seconds. Neither focus failure was
weakened or marked expected. The forced-death subtest expects worker failure,
not copy-acceptance failure, and requires transfer and supervisor completion
markers. Post-run process inspection found no test Shell, consumer, worker,
supervisor, or clipboard holder; unrelated PRIMARY PID 31113 was untouched.

All final logs live under:

```text
/workspace/.yolo/durable/gnome-headless/finalize/
  recipe-final.log, unit.log, race.log, sway.log, check.log, format.log,
  done.log, markdown.log
```

Final verification commands:

```bash
just format
just check
go test -count=1 ./internal/output ./internal/config ./cmd/mavor
go test -race -count=1 ./internal/output ./internal/config ./internal/daemon
go test -tags=integration -count=1 ./test/integration -run 'TestClipboardDispatchPersistenceRealSway|TestPaste|TestTyping' -v
uvx vantage-check docs/reports/gnome-headless-qa.md docs/research/gnome-clipboard-research.md docs/design/gnome-clipboard-design-plan.md docs/tasks/gnome-clipboard-tasks.md
just done
```

The local non-GNOME gates pass. Live GNOME remains an opt-in failing acceptance
suite, not a CI prerequisite. No production focus workaround or silence-filter
change is included. Ctrl+V, mature-session overview, GPU-free rendering, and
complete desktop lifecycle remain unverified. No jail edit or restart is needed
for the demonstrated transient Nix environment; adopting baked dependencies
would require separate jail validation and a human restart.
