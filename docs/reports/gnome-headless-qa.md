---
status: accepted
date: 2026-10-01
summary: "Independent production X11 acceptance passes twice on isolated GNOME; default wl-copy focus failures remain."
---

# Independent real GNOME headless verification

## Verdict

**No blocking findings for the explicit copy-only X11 backend within the tested
scope.** Independent acceptance passed twice against the actual uncommitted
implementation on detached `10139e1cfb30e301620301c2ffa04e34499818eb`.
This supersedes this report's earlier wl-copy-only blocking verdict; it does
**not** resolve the default Wayland backend's focus failures or establish full
GNOME desktop support.

The unchanged Wayland diagnostics still fail continuous-focus and overview
assertions. Two initial diagnostic cases also failed private-bus setup because
the artifact path exceeded the Unix socket-name limit. Shortening only the
artifact directory reproduced the overview focus failure and passed startup
cancellation. Those setup failures are not clipboard failures or skips.

Review changed only this report. Parent owns repairs, commit, and integration.
No production/test edits, silence-filter changes, child delegation, host desktop
access, deployment, branches, tags, publishing, extension, or jail edits occurred.

## Reviewed implementation

Inspected the actual dirty diff, including the untracked
[X11 dispatcher](../../internal/output/x11_clipboard.go),
[owner regressions](../../internal/output/x11_clipboard_test.go),
[production child tests](../../test/gnome/clipboard_test.go),
[X11 harness](../../test/gnome/x11_harness.py), and
[native GTK consumer](../../test/gnome/client/consumer.c).
Also inspected config/default/validation, CLI selection and shutdown wiring,
setup/doctor/tool requirements, and the related documentation changes.
The [implementation QA](../qa/gnome-clipboard-qa.md) provides complementary evidence.

The dispatcher launches foreground xclip with literal cleaned UTF-8 stdin,
CLIPBOARD-only arguments, and no keys. Successful ownership outlives Emit's
context; launch and serialized waiting have deadlines. Replacement and Close
kill/wait for owners; a wait goroutine also reaps external selection or connection
loss. Linux parent-death signaling terminates the owner after abrupt process
death. Startup acknowledgment recognizes xclip's foreground-loop diagnostic,
not a successful consumer transfer; the live test independently proves transfer.

Config preserves `clipboard_backend = "wayland"` as the default, including
omitted/empty values. `"x11"` requires `driver = "clipboard"`; unknown backends
and injection-driver combinations are rejected. CLI selection returns the X11
Close callback, and daemon shutdown defers it. Setup requires xclip rather than
wl-copy for this backend; doctor checks DISPLAY and a readable, nonempty regular
authorization file without touching selections. These are availability checks,
not proof of valid cookies or a live server. History recovery still uses wl-copy.

## Independent execution

Ran from this worktree using the
[hash-pinned test environment](../../test/gnome/environment.nix), not the host
session, a registry-latest environment, or a mock clipboard:

```bash
env -u LD_LIBRARY_PATH \
  MAVOR_GNOME_ARTIFACTS=/workspace/.yolo/durable/gnome-headless/independent-review \
  PYTHONDONTWRITEBYTECODE=1 \
  nix develop --impure -f test/gnome/environment.nix \
  --command just test-gnome-x11 -v
```

Both runs reported GNOME Shell **50.4**, actual Mutter **50.4** in Shell logs,
xclip **0.13**, and wl-clipboard **2.3.0**. Acceptance durations were **42.51**
and **42.72 seconds**. Supervisor regressions also passed twice. No skips.

| Acceptance | Independent observation in both runs |
| :--- | :--- |
| Production dispatcher | One actual `output.X11Clipboard` instance emits successive transcripts; no substituted command runner. |
| Native Wayland transfer | GTK traces show real `wl_data_offer.receive` calls and exact UTF-8 responses, not only local cached values. |
| Delayed/repeated/replacement | Three transcripts, each read three times after a two-second delay; previous owners exit and disappear. |
| Editable Paste | GtkTextView's own `clipboard.paste` action inserts each exact focused transcript. |
| Uninterrupted destination focus | No native inactive event during each focused copy interval; continuous Shell focus-event arrays remain empty. |
| Mature overview/no focus | Waits for Shell startup and completed animation, explicitly clears destination focus, then copies with overview visible, no focused window, and no focus event. |
| Later consumption | Overview text reads repeatedly after intentional destination reactivation and after another ten seconds. |
| PRIMARY | Separate native owner supplies the unchanged sentinel throughout copying and after Close; destination traces show primary-selection receive requests. |
| Successful owner lifetime | Owner remains alive beyond the three-second launch limit and through the ten-second delayed read. |
| Close and retained data | Close reaps final owner; actual native receives return last text at one and six seconds afterward. |
| Startup deadlines | Frozen private XWayland produces the three-second launch deadline and earlier 50-ms caller cancellation; child completes in 3.05 seconds. |
| Abrupt process death | SIGKILL of production child kills its owner; private subreaper waits for it. |
| Compositor loss | Real private Shell shutdown ends ownership; production wait goroutine reaps it and subsequent Close succeeds. |
| Teardown | Worker and supervisor report reaping; independent inspection finds all 14 recorded PIDs absent for each acceptance run. |

Shell Eval establishes/observes test focus and creates input capability for the
otherwise keyboard-less seat. It never supplies selection data, activates an
ownership helper, or injects keys. Focus assertions count transitions rather
than merely testing final restored focus; unique Shell log barriers ensure
observations include the preceding interval. The native consumer is forced to
Wayland. X11 ownership reaches it through Mutter's XWayland selection bridge
(the compositor's transfer between X11 and Wayland selections).

## Evidence

Durable artifacts, outside Git:

```text
/workspace/.yolo/durable/gnome-headless/independent-review/
  run1.log, run2.log
  x11-_uqnq_k5/, x11-schq0s5b/
  wayland-diagnostics.log, wayland-short-path.log
  done.log, race.log, sway.log
  reviewed-sha256.txt
/workspace/.yolo/durable/gnome-headless/ir/
  overview-dfesdf3g/, deadline-i6vlr57i/
```

Acceptance directories retain Shell/Eval/native protocol logs, production
acknowledgments, deadline/crash/loss logs, executable version, and tracked PIDs.
Authorization contents are not reproduced in the report. Source checksums
identify the reviewed uncommitted production and harness files.

Original Wayland diagnostics independently reproduced:

- Focused: transfer occurs, then continuous native focus assertion fails.
- Overview: after shorter-path rerun, initial copy transiently focuses helper.
- Deadline: shorter-path rerun passes real wl-copy cancellation.
- Forced worker death: native transfer precedes SIGKILL; supervisor reaps group.

These tests remain failing diagnostics, not expected-success assertions.
`just test-gnome-x11` deliberately selects X11 acceptance and supervisor tests;
`just test-gnome` still includes the known-broken Wayland cases.

## Independent gates

- `just done` passed format check, vet/staticcheck, ordinary unit suites, and
  integration/e2e/GNOME tagged vet. It printed **Quality gate passed. Ready to
  commit.** No commit was made.
- Uncached race tests passed for output, config, and CLI, including new owner,
  backend/default/round-trip/validation, selector/Close, tool, doctor, and setup
  regressions. Fixture executables prove unit behavior, not clipboard transfer.
- Real Sway clipboard persistence, paste dispatch, and typing-speed tests passed
  without skips. This is affected-backend coverage, not the full Sway suite.
- `git diff --check` passed before the report edit. Markdown render checks are
  recorded below after writing.

## Remaining genuine limits

No full daemon/audio/model lifecycle, real-model transcription, desktop
login/logout/lock/unlock/restart, physical shortcut/Ctrl+V, terminal Paste, or
clipboard-manager configuration matrix was exercised. The live child calls the
production dispatcher directly; config-to-dispatcher selection and shutdown
wiring are inspected and unit-tested, not a live daemon integration run.
GtkTextView action insertion is real editable-widget behavior, not physical
keyboard acceptance. The mature no-focus state is explicitly established for
this test; it does not assert every overview entry naturally clears focus.

Shell uses private HOME/runtime/buses and lacks several ordinary desktop
services, visible in logs. Software rendering is requested, but logs select
amdgpu `/dev/dri/renderD128`; GPU-free CI remains unproven. Availability failures
are fatal, not skips. Long durable paths can exceed the private D-Bus socket-name
limit; callers should use a short artifact root until the parent decides whether
the harness needs a portability repair.

Startup acknowledgment does not confirm server ownership. A failed replacement
may already disturb the prior selection; there is no rollback through reading
private user data. Retention after owner exit depends on the compositor's
clipboard manager and is demonstrated only through six seconds. External
selection-loss reaping is implemented but not separately tested with an unrelated
real clipboard writer. Abrupt child-process death is covered; all possible Go
thread-lifetime, system shutdown, kernel termination, or supervisor-death cases
are not. No retry, injection, extension, or broad GNOME support is claimed.

The published Vantage checker supplied the style guide and checked this report.
Its older vocabulary lacks planning/index support; no newer planning check or
browser inspection is claimed.
