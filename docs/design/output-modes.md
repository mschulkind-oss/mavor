---
status: accepted
stage: DECIDED
next: "Build it — see the companion plan"
---

# One output key, six named outcomes

**Status:** 2026-10-16. Every decision below was ruled in conversation on
2026-10-16 and is recorded in the [Decision Ledger](#decision-ledger). Evidence
verified against the tree at `8ec7bd4`.

> **In short:** `[output]` asks one question — what happens to the transcript,
> and to what was on the selections before it — and today three keys answer it
> jointly, with a precedence rule and a gap. One `mode` key with six named
> outcomes replaces them.

**Why it matters.** The author's own config carried `clipboard = false` and
read as "do not touch the clipboard," while the system pasted through the
clipboard on every utterance and restored it afterward. The name was accurate
when it was coined (`aace1f5`, when the copy was an optional side effect of
typing) and silently changed meaning when the paste driver made the copy the
transport (`16eb273`). Nobody made that decision; the name just stayed.

**The shape.** `[output] mode` names an outcome. The drivers, the chord, the
copy backends and the typing delay stay where they are — as implementation,
not as user-facing choices.

**Cost.** The `driver`, `clipboard` and `restore_selection` keys are deleted
with no compatibility aliases, following the precedent
[`configuration-surface.md`](./configuration-surface.md) set for this file.
Existing configs name the old keys as unknown until they are edited.

**Start at [The six modes](#the-six-modes)** — everything else is the
justification for that table.

**Needs your ruling:** None.

**Reads with:** [`output-modes-plan.md`](./output-modes-plan.md) (the
implementation plan), [`paste-restoration-safety.md`](./paste-restoration-safety.md)
(the still-open question of how restoration learns a paste was consumed),
[`paste-based-output-dispatch.md`](../reference/paste-based-output-dispatch.md)
(the dispatch reference this amends).

---

## What exists today

Three keys in `[output]` jointly decide one thing:

| Key | Default | What it actually decides |
| :--- | :--- | :--- |
| `driver` | `"paste"` | How the transcript reaches the window |
| `clipboard` | `false` | Whether the transcript stays on CLIPBOARD afterward |
| `restore_selection` | `true` | Whether the previous selections are written back |

They do not compose independently. In the paste driver, `clipboard = true`
overrides the CLIPBOARD half of `restore_selection` and says nothing about
PRIMARY; both false leaves both selections empty. A reader of the config file
cannot derive the outcome without reading the dispatch code, and the scaffold
comments describe the typing driver's model ("your transcript is always
typed… this also copies it") although paste has been the default since the
driver landed.

## The six modes

**Mode** *(coined here)*: one of six named outcomes for a finished
transcript, selected by `[output] mode`. Each name states its object — what
is left, restored, or cleared — because the ambiguity of the bare word
"keep" is what this design exists to remove.

| Mode | Delivery | Selections afterward |
| :--- | :--- | :--- |
| `paste-leave-transcript` *(default)* | Paste chord | Transcript remains on CLIPBOARD |
| `paste-restore-previous` | Paste chord | Previous selections restored |
| `paste-clear` | Paste chord | Both selections cleared |
| `type-only` | Keystrokes | Untouched |
| `type-leave-transcript` | Keystrokes | Transcript remains on CLIPBOARD |
| `copy-only` | None — the user pastes manually | Transcript remains on CLIPBOARD |

The set is the full matrix of meaningful combinations:

- Paste delivery needs the selections as transport, so its afterward is a
  real choice: leave the transcript, restore the previous contents, or clear.
- Typing never touches the selections to deliver, so its afterward is leave
  the clipboard alone or add the transcript to it. "Restore" after typing is
  meaningless — nothing was displaced.
- No delivery is exactly copy-only.

`paste_chord`, `copy_command`, `typing_delay_ms` and `clipboard_backend`
remain as expert keys. They tune a mode; they are not modes, and the scaffold
stops presenting them as peers of the decision.

## Principles

- **P1. One key, one decision.** The fate of the transcript and of the
  previous selections is a single choice and gets a single key. No
  combination table, no precedence rule, no gap where two falses compose
  into a third unstated outcome.
- **P2. Names state their object.** `leave-transcript` and
  `restore-previous` say which text they act on. The word "clipboard" alone
  never again names a policy about it.
- **P3. One default, unconditionally.** The default is
  `paste-leave-transcript` on every machine. Detecting a clipboard manager
  and changing the default to suit was considered and rejected: it is
  fragile, it makes the same config file mean different things on different
  machines, and it hides the choice the user is actually making. A user with
  a clipboard manager gets their previous contents from its history; that is
  the manager's job, and mavor does not need to detect it to let it work.
- **P4. Fail toward nothing, never toward the wrong text.** Where an
  outcome cannot be guaranteed, the acceptable failure is a paste that does
  not happen — not a paste of something else.
- **P5. The user's newest act wins.** A copy the user makes after a
  dictation supersedes anything mavor is still planning to do to the
  selections.
- **P6. Emissions settle independently.** Output is serialized in spoken
  order; each emission's business with the selections finishes before the
  next emission begins, regardless of what is still recording or
  transcribing.

## The race, stated precisely

Wayland selection transfer is demand-driven: the paste chord is a signal,
not a transfer. The focused application asks the selection's owner for the
data when its event loop processes the chord, and the paste driver cannot
observe that request. A timed hold therefore has two bounds set by two
different actors:

- **Below**, the application's read latency — normally milliseconds, since
  the read is part of handling the chord.
- **Above**, the user's next deliberate use of the previous selection —
  pasting what they copied before dictating, or middle-clicking the previous
  PRIMARY. Nothing technical requires the previous contents back by any
  deadline; the deadline exists because the user will reach for them, and
  that reach is user-initiated.

The current 350 ms lease is a bet that the first latency is shorter than the
second. Both distributions overlap — a busy application can read late, a
fast user can paste within 350 ms — so no value of the timer makes a timed
restore or clear *correct*; it only chooses which failure occurs:

| Outcome on a lost race | Failure the user sees |
| :--- | :--- |
| Timed restore (previous behavior as default) | The previous text pastes — indistinguishable from "the wrong thing got pasted" |
| Timed clear | Nothing pastes |
| Leave the transcript | Nothing to lose: a late reader still gets the transcript |

This asymmetry is why the default is `paste-leave-transcript` (no second
timed write exists to lose a race), and why `paste-clear` exists although it
shares the timer: by [P4](#principles), a missed paste is a better failure
than a wrong one. `paste-restore-previous` remains available, documented as
timed and therefore imperfect. How restoration could instead learn that the
paste was consumed — a receipt rather than a stopwatch — stays open in
[`paste-restoration-safety.md`](./paste-restoration-safety.md); this design
does not settle it and does not claim to.

One further race is closed here, by [P5](#principles): today the restore
writes the snapshot back unconditionally, so a copy the user makes during
the lease is overwritten by their own pre-dictation contents. Under this
design, before restoring or clearing, the driver checks whether each
selection still serves the transcript. One that no longer does has been
taken over by a newer copy, and the pending restore or clear for that
selection is canceled. (For clear, killing mavor's holder cannot clear
another owner's selection in any case; the check makes the cancellation
explicit and logged rather than incidental.) The check narrows the window
rather than eliminating it — a copy landing between the check and the write
can still be overwritten — and the design accepts that residue for the same
reason it accepts the timed lease: the alternative is the receipt machinery
that remains an open question.

## Settlement and stacked dictations

A pending restore belongs to the emission that created it, not to any later
event. In particular, the start of the next dictation means nothing for the
selections: in a stacked dictation it can precede the previous emission
entirely. So:

- Each emission restores or clears its own **baseline** *(coined here)* —
  the selection contents it captured before dispatch — once its paste has
  settled, even while a later dictation is recording or transcribing.
  Recording does not use the selections, so this cannot disturb it.
- An external copy during the pending window cancels the pending operation
  ([P5](#principles)).
- The next emission never begins while a previous one is unsettled; it
  would otherwise snapshot the transcript as its baseline. Output is
  already serialized in spoken order and `Emit` already runs its cleanup
  before returning, so this is a statement of the existing invariant, not
  new machinery.

## Existing configs

There are no compatibility aliases, per the precedent in
[`configuration-surface.md`](./configuration-surface.md): the project has not
shipped publicly, and aliases would permanently complicate the surface to
serve files that one edit fixes. Concretely:

- `driver`, `clipboard` and `restore_selection` become unknown keys. The
  daemon already logs unknown keys as ignored and `mavor doctor` already
  reports them; doctor additionally names the mode that replaces them, so
  the fix is stated where the problem is reported.
- A config that set none of the three — including one carrying only
  `clipboard = false`, the line that prompted this design — silently moves
  from paste-restore to paste-leave: the previous live clipboard stops
  being restored. That is the intended new default, and the scaffold says
  so in as many words.
- `mavor config init` scaffolds `mode` with the six values listed, and
  `mavor doctor` reports the active mode and what it will do.

## Non-goals

- **No receipt mechanism.** Whether restoration can learn that a paste was
  consumed stays open in
  [`paste-restoration-safety.md`](./paste-restoration-safety.md).
- **No environment detection.** No default in this design depends on what
  else is running ([P3](#principles)).
- **No per-application routing.** Which buffer a given application reads
  remains the dual-buffer driver's business, unchanged.
- **No change to the lease duration.** The timer is not tuned here; the
  modes that keep it are documented as timed instead.

## Decision Ledger

| ID | Ruling / Decision | Date | Settled in | Built |
| :--- | :--- | :--- | :--- | :--- |
| D1 | Six modes under one `output.mode` key replace `driver`, `clipboard` and `restore_selection` | 2026-10-16 | [The six modes](#the-six-modes) | No |
| D2 | Default is `paste-leave-transcript`, unconditionally — no clipboard-manager detection | 2026-10-16 | [The six modes](#the-six-modes) | No |
| D3 | `paste-clear` is offered although timed, because a missed paste beats a wrong one | 2026-10-16 | [The race, stated precisely](#the-race-stated-precisely) | No |
| D4 | A new external copy cancels a pending restore or clear | 2026-10-16 | [The race, stated precisely](#the-race-stated-precisely) | No |
| D5 | Each emission settles its own baseline; the next emission never starts unsettled; next-dictation start has no selection meaning | 2026-10-16 | [Settlement and stacked dictations](#settlement-and-stacked-dictations) | No |
| D6 | Old keys are deleted with no aliases; doctor names the replacement mode | 2026-10-16 | [Existing configs](#existing-configs) | No |
