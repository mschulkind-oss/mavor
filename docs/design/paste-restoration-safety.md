---
status: in-review
next: "Rule OQ-PRS1 — pick how restoration learns the paste was consumed"
---

# Restoration waits for a receipt, not a stopwatch

**Status:** 2026-10-15, uncommitted changes against `441a839`. MEASURED: the
late-reader hazard reproduced on real headless Sway, and the registration
handshake (P4 in the [dispatch reference](../reference/paste-based-output-dispatch.md))
landed with a failing-then-passing regression. UNMEASURED: the host incident was
never reproduced on the host, and no clipboard manager runs in the test harness,
so the cliphist interaction below is reasoning from cliphist's watch-based design
rather than a measurement.

> **In short:** after pasting a transcript, `output.Paste` rewrites the user's
> previous `CLIPBOARD` and `PRIMARY` on a 350 ms timer. Wayland selection
> transfer is demand-driven, so a reader scheduled after that timer reads the
> restored text instead of the transcript — the wrong thing gets pasted. The
> timer needs to become evidence that the paste was consumed.

**Reads with:** [`paste-based-output-dispatch.md`](../reference/paste-based-output-dispatch.md)
(the driver, its principles and its current defaults),
[`how-mavor-works.md`](../reference/how-mavor-works.md) (daemon architecture).

---

## The problem

`Timed Lease` (see the [dispatch reference](../reference/paste-based-output-dispatch.md#principles--invariants))
holds both selections for a fixed window, kills the holders, and writes the
previous selections back. Nothing in that sequence observes whether the paste was
consumed. A reader that arrives after the window therefore receives the restored
previous text, which is indistinguishable in the log from a paste that worked:
the daemon records what it wrote, never what the reader read.

Reproduced on real headless Sway against `441a839` (`TestScratchLateReaderGetsPreviousSelection`,
a scratch test; the permanent form is the registration regression in
`test/integration/paste_test.go`): with a 200 ms lease, a reader at $+50\text{ ms}$
receives the transcript from both buffers, and the same read at $+300\text{ ms}$
returns `prior clipboard` and `prior primary`.

The host journal shows the same driver reaching this shape: every dispatch logs
`output: paste lease completed duration_ms=350 elapsed_ms=38x` and
`output: holders reaped`, whether or not the paste landed, and a run of dispatches
on 2026-10-15 between 16:10 and 16:21 logged
`output: selection restoration skipped selection=PRIMARY snapshot_err="exit status 1"`
— `PRIMARY` had no owner to snapshot, so it was left empty after each dispatch
rather than restored.

Two edges of the same guess can hand a reader the wrong text, and they are not
distinguishable after the fact:

- **Front edge (closed).** The holders are separate processes. If the chord is
  injected before they own the selections, the reader gets whatever owned them
  before. This was a fixed 20 ms sleep; it is now a bounded read-back
  ([P4](../reference/paste-based-output-dispatch.md#principles--invariants)), and a
  registration that never completes logs
  `output: selection holders did not serve the transcript before the chord`.
- **Back edge (open).** The restore can pass a reader that is scheduled more than
  about 400 ms after the chord. Fixing this needs a receipt, which is
  [OQ-PRS1](#OQ-PRS1).

## Who reads the selection

Which buffer matters depends on the focused application, and mavor does not route
by application ([deferred](./active-application-output-routing.md)). In
practice:

| Reader | Buffer | When it reads |
| :--- | :--- | :--- |
| Kitty's default `shift+insert` → `paste_from_selection` (verified in kitty 0.49's `kitty.conf(5)`) | `PRIMARY` | synchronously, when it handles the injected chord |
| A GUI application's `shift+insert` | `CLIPBOARD` | synchronously, when it handles the injected chord |
| Pi, on `ctrl+v` or right-click, via `readClipboardText` → `wl-paste --no-newline --type text` (`dist/utils/clipboard.js`) | `CLIPBOARD` | asynchronously, after `handleClipboardPaste` has awaited the terminal-clipboard query and the file/image probes |
| cliphist, on every new ownership | `CLIPBOARD` | within milliseconds of the write |

The reader for mavor's own chord on the reporting machine is Kitty reading
`PRIMARY`. Pi's clipboard read is *not* part of the injected paste — it runs on
`ctrl+v` and right-click, not on `shift+insert` — so Pi is the window the paste
lands in, not the reader that could race the restore.

## Why the obvious fix is not available

`wl-copy --paste-once` is exactly the receipt this needs: the holder exits after
its first read, so the driver can restore the moment the paste is served.
Measured on headless Sway: a `--paste-once --primary --foreground` holder served
the transcript once and exited; a second read returned `Nothing is copied`.

It composes only with an unobserved buffer. On `CLIPBOARD`, a watch-based
manager (`cliphist` is deployed on the reporting machine as `cliphist.service`)
reads every new ownership immediately, so the manager's read ends the holder
before the target pastes. Re-arming the holder does not fix that: taking the
selection again is another ownership event, which triggers another manager read.

## Options

See [OQ-PRS1](#OQ-PRS1).

## Open Questions

1. 💬 **OQ-PRS1: How should restoration learn that the paste was consumed?**

   This decides whether `restore_selection = true` can be honored without ever
   handing a late reader the previous selection.

   - **A — Own the selection in-process and count reads.** mavor serves
     `CLIPBOARD`/`PRIMARY` from its own Wayland data source (`internal/wayland`
     already speaks the wire protocol), counts `send` events per buffer, discounts
     the read that arrives within a few milliseconds of the write as the manager's,
     and restores when the later read arrives or at a generous ceiling. The only
     option with no residual window; the largest change, and it needs seat and
     data-device code plus compositor-level acceptance.
   - **B — `--paste-once` on `PRIMARY`, longer configurable floor on
     `CLIPBOARD`.** `PRIMARY` gets an exact receipt (nothing watches it), so the
     terminal path is correct and restores promptly; `CLIPBOARD` keeps the timed
     hold because cliphist reads it. Small and testable, and leaves a
     `CLIPBOARD` reader slower than the floor reading the previous text.
   - **C — Stop restoring; the transcript becomes the clipboard.** No race and no
     new machinery, but this is the behavior `output.clipboard = true` already
     offers and the default deliberately declines: it costs the user their
     clipboard on every utterance.
   - **D — Restore on the next signal instead of a timer.** Leave the transcript
     on both selections and write the previous contents back when the next
     dictation starts or on an explicit request. No race, but the previous
     selection stays replaced for the whole interval, which is most of C's cost.

   <!-- vantage: question id=OQ-PRS1 leaning="B now, A as the end state — B fixes the path mavor's own chord uses and is small enough to test on real compositors; A is the only option with no residual window, and is what to do if any late reader still loses." -->

   _Leaning:_ B now, A as the end state — B fixes the path mavor's own chord uses
   and is small enough to test on real compositors; A is the only option with no
   residual window, and is what to do if any late reader still loses.

   **Answer:**
   > _(empty — fill in when decided)_
