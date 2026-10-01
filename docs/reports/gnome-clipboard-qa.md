---
status: accepted
---

# Independent GNOME clipboard review

Reviewed the actual uncommitted worktree based on `a2ba579`, 2026-10-01.
Production code and tests were not modified; no commit was made.
This is the historical review of the initial Wayland-helper implementation.
The later [real GNOME review](gnome-headless-qa.md) supersedes its outstanding
live-test verdict: the explicit X11 backend passed, while the Wayland helper's
focus failures remain. Current [implementation QA](../qa/gnome-clipboard-qa.md)
records that correction and its verified scope.

## Verdict

**Blocking findings: none for the explicitly scoped copy-only implementation.**
Live GNOME acceptance remains outstanding. Passing Sway tests do not establish
that Mutter's clipboard fallback works, preserves focus, or cleans up failed
launches. Do not present this as verified GNOME desktop support.

## Source evidence

- [Output selection](../../cmd/mavor/main.go#L377-L404) returns the clipboard
  dispatcher before native keyboard construction. Its only nonempty emission
  command is [wl-copy with a fixed UTF-8 type](../../internal/output/clipboard.go#L21-L39),
  with literal transcript bytes on stdin. No wtype, native injection, key chord,
  PRIMARY write, selection read, restore, or paste-only override is reachable.
- Ordinary backgrounding retains the helper holding the selection; neither
  foreground ownership nor paste-once is requested. The three-second context
  limits launch waiting, not ownership. There is no 350 ms manual-paste lease.
  Earlier cancellation is respected and errors retain their cause.
- Overlay construction is unchanged and independently falls back to a no-op
  implementation when layer-shell is absent. Clipboard selection does not
  require that protocol or virtual-keyboard support. wl-copy still requires
  a working compositor transfer path; no portal implementation was added.
- Config loading validates driver values; paste remains the resolved default,
  typing remains explicit, and clipboard round-trips through config show.
  Empty driver resolves to the existing default; unknown names fail rather
  than enabling injection. Scaffold comments explain ignored knobs and
  unconditional clipboard-mode copying despite the additional-copy flag.
- Setup and doctor require wl-copy, not wtype or wl-paste, for clipboard output.
  Doctor performs availability checks rather than a clipboard write or a
  readiness claim. Audio/model requirements remain intact.
- Transcription, history-before-dispatch, and output-error logging are unchanged.
  No new network request, notification transcript exposure, or cloud dependency
  was introduced. Idle denotes cycle completion, not successful copying.
  Silence filtering is unchanged.
- The [manual-paste guide](../user-guide.md#gnome-wayland-manual-paste) and README
  explicitly document the mode, GNOME shortcut setup, manual paste, absent HUD
  and visible preview, logs/history recovery, and unverified live GNOME behavior.

## Nonblocking findings and coverage gaps

1. **Limited helper diagnostics:**
   [DefaultRunner](../../internal/output/output.go#L35-L38) discards wl-copy
   stderr. Errors identify clipboard copying and preserve launch/exit/timeout
   causes, but a compositor-specific failure may report only an exit status.
   This is preexisting runner behavior that avoids inherited-pipe hangs.
   A future improvement could direct stderr to a bounded, non-pipe destination;
   do not restore unbounded pipe capture for a daemonizing helper.
2. **Real cancellation is not exercised:**
   [deadline/error tests](../../internal/output/clipboard_dispatch_test.go#L40-L68)
   use an injected runner that waits for context cancellation. The real Sway
   test covers successful ownership, not a hung helper, cancellation after
   forking, or descendant cleanup. The runner kills the immediate process only.
   Inferred additional edge case: an unexpected helper that forks before
   consuming a large stdin payload could retain the input pipe, leaving Go's
   stdin-copy wait outstanding even after immediate-child cancellation.
   This was not reproduced with wl-copy and is not evidence of a normal-path
   defect; a context deadline is not proof of an absolute all-process wait bound.
3. **Constructor exclusion is partly source-proven:**
   [selector tests](../../cmd/mavor/clipboard_test.go#L13-L31) assert dispatcher
   type, not a spy on native construction. Source control flow proves exclusion
   now, but a future accidental constructor call before selection could escape
   that assertion. A negative constructor-call test would strengthen regression
   coverage. The real Sway test also cannot prove startup on missing layer-shell.
4. **Stale diagram label:**
   [user-guide diagram](../user-guide.md#L58-L59) still describes output as
   `wtype + wl-copy` and has a widened box label. The new prose correctly
   distinguishes the three modes; update the diagram during parent doc integration.

## Independently observed checks

All commands below passed in this worktree:

```bash
just check-ci
go test ./internal/output ./internal/config ./cmd/mavor -count=1
go test -tags=integration ./test/integration/... -run '^TestClipboardDispatchPersistenceRealSway$' -count=1 -v -timeout=60s
go test -tags=integration ./test/integration/... -run '^(TestPasteDispatchRealSway|TestNativeTypingIsAcceptedByTheCompositor|TestNativeTypingHandlesAwkwardText)$' -count=1 -v -timeout=90s
```

The read-only gate included formatting, vet, staticcheck, both tagged suite
checks, and unit tests (some cached); the targeted unit rerun was uncached.
The clipboard integration test passed in 3.43 seconds, using the real runner:
selection persisted beyond launch timeout, two reads succeeded, PRIMARY stayed
unchanged, and a successive transcript replaced CLIPBOARD. Existing paste
restoration and native typing acceptance/awkward-text tests passed.
Sway startup and Waybar emitted environment warnings without test failure.
The worktree diff whitespace check passed. Published Vantage checker validation
passed all ten reviewed Markdown files, including this report. Newer planning
index capabilities were not checked.

## Outstanding check: Live GNOME manual-paste acceptance

Record GNOME and wl-clipboard versions. With clipboard mode explicitly selected,
verify daemon startup without keyboard protocols, logged no-overlay fallback,
custom-shortcut recording, editor and terminal paste, delayed/repeated paste,
unchanged PRIMARY, successive transcripts, clipboard managers on/off,
lock/unlock, focus effects, failed/hung launches, cancellation, leftover helper
processes, and history recovery. Check logs after failure; do not infer success
from Idle. No live GNOME session was tested here.

Full integration and real-model suites were not run. Review scope excludes
notifications, portals, GNOME HUD, automatic injection, and silence-filter work.

## Finalization disposition

The final phase corrected the stale diagram in the user guide and retained
the existing implementation regression tests. The other three nonblocking
findings remain deferred, not silently treated as verified. Final race, Sway,
Markdown, and local-gate evidence is recorded in the
[delivery QA](../qa/gnome-clipboard-qa.md#local-verification).
This independent report describes the pre-commit review; final delivery is
one coherent commit, with main-checkout integration left to the parent.
