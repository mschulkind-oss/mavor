---
status: accepted
---

# GNOME clipboard verification — 2026-10-01

Uncommitted implementation against detached `10139e1` in the isolated clipboard
worktree. Parent owns final review, repairs, commit and integration; this phase
does not deploy or change silence filtering. The
[plan](../design/gnome-clipboard-design-plan.md) owns behavior.

## Production X11 verification

**PASS for bounded isolated dispatcher acceptance**, not complete GNOME support.
Shell/Mutter **50.4**, xclip **0.13**, GTK **4.22.4**, wl-clipboard **2.3.0**;
[hash-pinned environment](../../test/gnome/environment.nix) uses measured nixpkgs
`b6c8664de9b6cc07fe5666a29f91884ba81197c4`.

The [production harness](../../test/gnome/x11_harness.py) starts the real
`output.X11Clipboard`, not a mock command runner. One production instance owns
successive transcripts. A native Wayland GTK destination and a separate PRIMARY
owner consume actual offers. Both native active-state events and continuous Shell
focus-window events guard the copy intervals. Unsafe Eval establishes only test
focus/input capability and reads private environment; no selection contents,
helper focus, host session or keys enter through Eval.

| Acceptance | Observed result |
| :--- | :--- |
| Continuous destination focus | Three focused copy intervals: no inactive event and no Shell focus changes. |
| Delayed/repeated/replacement | Three UTF-8 transcripts, three native reads each after two-second delay; replaced owners exit and are reaped. |
| Editable GTK Paste action | GtkTextView's own clipboard.paste action inserts each exact transcript; test action activation is not production input injection. |
| Mature overview/no focus | After Shell startup/animation and explicit destination-focus clearing, copy leaves overview visible, focus absent and event array empty. |
| Later native consumption | Overview transcript reads three times after destination reactivation and again after ten seconds. |
| PRIMARY | External sentinel stays unchanged throughout, including after production Close. |
| Successful ownership | Owner survives well past the three-second launch deadline; no timed lease/paste-once expiration. |
| Close | Production Close kills/waits for the final owner; no owner PID remains. |
| After Close | Mutter offers last text at one and six seconds after Close; bounded observation, not indefinite persistence. |
| Failed/canceled startup | Frozen private XWayland causes the actual three-second timeout and earlier 50-ms context deadline. |
| Abrupt daemon-process death | Linux parent-death signal kills owner; private test subreaper reaps it. |
| Compositor loss | Real Shell shutdown ends X11 owner; production wait goroutine reaps it; Close then succeeds. |
| Test teardown | Worker and external supervisor report tracked/adopted process reaping. |

Durable logs (outside Git):

```text
/workspace/.yolo/durable/gnome-headless/
  production-x11-run2.log    # transfer/focus/Close/Paste pass
  production-x11-run4.log    # plus frozen XWayland and compositor loss pass
  production-x11-run5.log    # same acceptance through pinned repository environment
  production-x11-run6.log    # adds abrupt production-parent death; all pass
  production-x11-run7.log    # all checks pass again with unique focus-log barriers
  production-x11-wayland-diagnostics.log
  production-x11-race-final.log
  production-x11-check.log
  production-x11-done.log
  production-x11-sway.log
  tests/x11-brkq9gd8/        # run6 Shell/consumer/dispatcher/deadline/loss logs
  tests/x11-c4g6d9i9/        # run7, including crash/loss owner PID records
```

Each diagnostics directory retains private bus/Shell/consumer logs, native
Wayland receive traces, production acknowledgments, private DISPLAY/auth path
metadata, xclip version and tracked PIDs. Authorization contents are not copied.

## Failures observed and repaired

- Tests failed first on missing X11 constructor/backend config symbols, before
  production wiring. Added implementation and kept regressions permanently.
- Production run1 passed the first transfer/focus interval, then failed PID
  bookkeeping. Go launched later children from another OS thread; the harness
  now enumerates all thread child lists. Run2 passed.
- Production run3 passed transfer/focus/Close checks but its timeout assertion
  failed: stopping Shell alone left XWayland serving X11. The repaired test stops
  only the identified private XWayland child, never a host server. Run4 passed.
- Original wl-copy focus failures persist and are **not repaired or weakened**.
  They describe the Wayland fallback, not the explicit X11 backend.

## Original Wayland diagnostics

Rerun through the pinned environment using unchanged
[Wayland harness assertions](../../test/gnome/harness.py):

- Focused case: native transfer completes, then continuous-focus assertion fails
  because copying stole destination focus.
- Overview case: fails because the initial helper transiently received focus.
- Deadline case: real wl-copy startup cancellation passes.
- Forced worker death: native transfer followed by SIGKILL and supervisor cleanup
  passes.

These remain accessible as `TestGNOMEClipboard`; `just test-gnome` includes them
and therefore intentionally fails on measured GNOME. `just test-gnome-x11`
selects the independent X11 acceptance plus supervisor regressions. Neither
command blesses the wl-copy behavior as focus-safe.

## Local verification

Affected output/config/CLI unit tests pass without cache, including backend
config/default/validation/marshal/show/scaffold, selector/Close wiring,
backend-aware tool requirements and DISPLAY/authorization checks. Owner tests
cover literal shell-looking/leading-option Unicode stdin, exact CLIPBOARD-only
arguments, empty text, failed helper/deadline/cancel, previous-owner retention on
failed startup, successful-context independence, replacement and Close reaping.

Affected race suites pass. Real Sway output tests ran and passed without skips:
clipboard persistence/repeated reads/PRIMARY, paste restore, native typing and
awkward text. No Sway harness or silence-filter changes were needed; parent
already owns the separate lifecycle harness repair.

`just check` and final `just done` passed format/vet/staticcheck, all unit suites
and integration/e2e/GNOME tagged type checks. Final affected race suites ran
without cache and passed. Published Vantage checked every edited Markdown file;
its help has no planning/index capability, so newer planning checks were not run.
The GNOME injection roadmap priority was reviewed and left unchanged; its existing
porting-design link leads to this separate copy-only plan. Recorded final-run
PIDs were checked absent after teardown. `git diff --check` passed.
`just done` printed **Quality gate passed. Ready to commit.** Changes remain
uncommitted by instruction, despite that generic gate message. Real-model
end-to-end transcription and the full Sway suite are outside this output-only
phase; parent integrated-suite verification is separate.

## Remaining limits

No full login/logout/lock/unlock, physical shortcut/Ctrl+V, terminal Paste,
clipboard-manager configuration matrix, daemon/audio/model lifecycle or desktop
restart acceptance. GtkTextView action insertion is stronger than readback alone,
but it is not physical key acceptance. Missing desktop services remain visible
in Shell logs. Software rendering was requested but amdgpu was selected;
GPU-free CI is unproven.

Startup acknowledgment bounds helper launch, not server-confirmed selection
ownership. Partial failed replacement may already replace the previous selection;
there is no rollback by reading private user data. Post-Close retention depends
on the compositor/clipboard manager. History copy commands still use wl-copy,
so X11 history recovery is not claimed. Doctor detects tool/DISPLAY/auth-file
availability, not valid server cookies or successful transfer.

No deployment, publishing, branches, tags, commits, host desktop access,
extension, production input injection or jail configuration edits occurred.
