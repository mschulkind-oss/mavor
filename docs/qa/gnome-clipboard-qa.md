---
status: accepted
---

# GNOME clipboard verification — 2026-10-01

Delivery is prepared as one coherent final commit in the isolated worktree
based on `a2ba579`; parent integration is separate.
The [plan](../design/gnome-clipboard-design-plan.md) defines scope.

## Observed failures and repairs

- Tests first failed on missing dispatcher/selector symbols, and unknown driver
  acceptance. Implemented the dispatcher/selection and config validation.
- Restoring the original doctor implementation reproduced the clipboard tool
  failure: `missing = [wtype]`. Driver-aware requirements fixed it.
- Scaffold test failed on missing clipboard/ignored-knob explanations; fixed.
- The Just integration recipe lost shell quoting around the test expression;
  reran the same tests directly with a correctly quoted expression.
- Initial Sway fixture hung on an inherited pipe captured with `Cmd.Output`
  during backgrounding wl-copy. A bounded test run confirmed the waiting pipe.
  Changed fixture copy launches to `Cmd.Run` without capture; rerun passed.
- Documentation checker initially found a renamed heading link and links to the
  not-yet-written QA document. Fixed the link and completed this document.

## Passing verification

- Targeted unit tests: output dispatch, config load/marshal round-trip, config
  show round-trip, scaffold explanations, selector, and doctor/tool requirements.
  Cases include Unicode, leading options, shell-looking text, newline cleanup,
  empty text, helper failure, three-second launch timeout, earlier deadline,
  and caller cancellation. One CLIPBOARD-only command is asserted.
- Real headless Sway: `TestClipboardDispatchPersistenceRealSway` passed. Clipboard
  survived beyond three seconds, allowed repeated reads, left PRIMARY intact,
  and accepted a successive transcript. This exercised the real default runner.
- Existing Sway paste restore and native typing acceptance/awkward-text tests
  passed. Sway startup and Waybar emitted environment warnings without causing
  failures; no live desktop service was touched.
- `just check` passed format, vet, staticcheck, tagged type checks, and unit tests.
- Vantage checker passed all ten changed Markdown documents (published
  checker conventions; no claim of newer planning-index verification).
- `git diff --check` passed.
- `just done` passed: **Quality gate passed. Ready to commit.**

The full integration suite and real-model end-to-end suite were not run;
applicable output integration tests and both tagged type checks were run.

## Final review disposition and rerun evidence

The [independent review](../reports/gnome-clipboard-qa.md) found no blocking
findings. Finalization corrected the stale output diagram; no production
behavior changed during this phase, so existing regression tests were retained.
Nonblocking improvements remain deferred: bounded helper stderr diagnostics,
real hung/forked-helper cancellation and descendant coverage, and a native
constructor-call spy (exclusion is currently source-proven plus selector-tested).

Final reruns passed in this worktree:

```bash
just format
just check
go test -race ./internal/output ./internal/config ./cmd/mavor -count=1
go test -tags=integration ./test/integration/... -run '^(TestClipboardDispatchPersistenceRealSway|TestPasteDispatchRealSway|TestNativeTypingIsAcceptedByTheCompositor|TestNativeTypingHandlesAwkwardText)$' -count=1 -v -timeout=90s
```

Race runs passed all three affected packages. All four Sway tests ran and
passed (no skips); persistence took 3.37 seconds. Sway roundtrip and Waybar
bus/portal warnings were observed, without failing the tests. Unit tests in
the full local gate used cache; affected race tests and integrations did not.
Published Vantage validation passed all ten touched Markdown documents.
The checker help has no planning/index support, so those newer checks were
not run. The roadmap still links the porting design, which links this plan;
no priority or injection-proposal ruling changed. `git diff --check` passed.
Final `just done` passed: **Quality gate passed. Ready to commit.**
No live host service, installation, or deployment was touched.

## Deferred acceptance

No live GNOME session was tested. Record GNOME and wl-clipboard versions; check
background copying into editors/terminals, delayed/repeated paste, clipboard
managers on/off, successive transcripts, lock/unlock, focus disruption, launch
failure/timeout, and history recovery. Sway does not certify Mutter fallback.
A helper launch cannot guarantee readiness or all-descendant cleanup.

Notifications, automatic injection/selection, portals, GNOME HUD, and silence
filter changes are deliberately absent. Parent integration must reconcile
shared config/main/documentation changes, including the separate silence-filter
workflow. The silence-filter setting is unchanged here.
