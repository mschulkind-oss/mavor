---
status: accepted
stage: BUILT
next: "Retained with the design doc as the build record"
---

# Plan: six output modes

**Status:** 2026-10-16. Written against `8ec7bd4`; built and landed on main (`6eab774`, `7711473`).
What the plan got wrong: its per-task commits assumed a compiling tree at
each step, but the pre-commit gate requires one, so Tasks 1 and 3 landed as
a single commit. Nothing else had to be rediscovered.
**Design:** [`output-modes.md`](./output-modes.md).
Precedence: the design wins on behavior, the tree wins on fact, this file
is advice and is the first thing to be wrong.

## Map

| Path | Change |
| :--- | :--- |
| `internal/config/config.go` | `Output.Mode` replaces `Driver`/`Clipboard`/`RestoreSelection`; mode constants, validation, predicates; defaults updated |
| `internal/config/config_test.go` | Mode parsing/validation/default tests; legacy driver tests rewritten |
| `internal/output/paste.go` | Pre-restore ownership check: a selection no longer serving the transcript cancels its restore |
| `internal/output/paste_test.go` | External-copy cancellation tests |
| `cmd/mavor/main.go` | `selectOutput` switches on mode |
| `cmd/mavor/config_cmd.go` | Scaffold emits `mode` with the six values |
| `cmd/mavor/config_cmd_test.go` | Scaffold assertions updated |
| `cmd/mavor/doctor.go` | Mode-based verdicts; legacy-key hint naming the replacement mode |
| `cmd/mavor/doctor_test.go` | Verdict tests updated (if present) |
| `docs/reference/paste-based-output-dispatch.md` | Mode table replaces driver/restore defaults; the restore-by-default rationale marked superseded |
| `docs/user-guide.md` | `[output]` section rewritten around modes |
| `docs/design/output-modes.md` | Ledger `Built` column and stage updated at landing |

## Reuse

- Mode validation mirrors `models.ParseFinalMode` handling in
  `LoadFile` (`internal/config/config.go`): resolve the empty value to the
  default, reject unknown values with the valid list in the message.
- `Paste.selectionServes` (`internal/output/paste.go`) already answers "does
  this selection serve this text" with the trailing-newline tolerance the
  ownership check needs.
- `mockLauncher.servedSelection` (`internal/output/paste_test.go`) already
  models live selection contents; extend it with a way to swap in an
  external copy mid-dispatch rather than building a second fake.
- Dispatcher serialization is already the invariant D5 needs:
  `internal/daemon/dispatch.go` emits in spoken order from one goroutine and
  `Paste.Emit` runs cleanup before returning. No daemon change.

## Traps

- **The ownership check must run before the holders are killed.** After
  they die, `wl-paste` fails for lack of an owner and "no longer serves the
  transcript" becomes indistinguishable from "nobody took over." Capture
  the per-selection takeover flags at the end of the lease, before `Kill`,
  and let the deferred restore consult them.
- **`config_cmd.go` prints defaults positionally.** Removing three fields
  and adding one changes the `Sprintf` argument list; the scaffold test
  compares against `config.Default()` and will catch a mismatch, but only
  if it is run — it is the first test to make green.
- **Doctor has four separate `Driver == "clipboard"` checks** (`checkWtype`,
  `checkClipboard`, `checkOutput`, `outputVerdict`, plus tool checks near
  lines 150 and 268). Missing one leaves a mode misreported, not a compile
  error.
- **`ClipboardBackend` survives.** It is valid only with `copy-only` now;
  validation must say so in mode terms.

## Tasks

**Task 1 — the mode type.**
- **Settled:** six mode strings exactly as in the design's table;
  `paste-leave-transcript` is the default; no aliases for the old keys.
  Do not re-open.
- **Tests first:** table test over all six modes for the predicates
  (paste-delivered, leaves-transcript, restores-previous, copy-only);
  unknown mode rejected naming the valid values; empty resolves to the
  default. → `go test ./internal/config/ -run TestOutputMode` (red: no
  `Mode` field yet).
- **Change:** `internal/config/config.go` — `Mode string` field, constants,
  `ValidateMode` (replacing `ValidateDriver`), predicates, `Default()` and
  `Resolve()` updates; delete `DefaultOutputDriver` and
  `DefaultRestoreSelection` (repo-wide grep first — today only this file
  uses them).
- **Gates:** `go test ./internal/config/`.
- **Commit:** `feat(config): output.mode replaces driver, clipboard and restore_selection`.

**Task 2 — cancellation before restore** (after 1, independent files).
- **Settled:** a selection that no longer serves the transcript at the end
  of the lease has been taken over; its restore is skipped and logged.
  Do not re-open.
- **Tests first:** mock dispatch where CLIPBOARD is externally replaced
  during the lease — expect no `wl-copy` restore run for CLIPBOARD and a
  normal PRIMARY restore; and the reverse. → `go test ./internal/output/
  -run TestPasteExternalCopyCancelsRestore` (red).
- **Change:** `internal/output/paste.go` — record takeover per selection
  before killing the holders (reuse `selectionServes` inverted: served
  content that differs from the transcript means takeover; a read error
  means no evidence, so restore proceeds as today). Deferred restore
  skips taken-over selections with a log line.
- **Gates:** `go test ./internal/output/`.
- **Commit:** `fix(output): a newer copy cancels the pending restore`.

**Task 3 — wiring and surfaces** (after 1).
- **Settled:** `selectOutput` maps modes onto the existing dispatchers:
  paste modes → `Paste` with `RestoreSelection`/`Clipboard` set from the
  mode; type modes → native/wtype with `Clipboard` from the mode;
  `copy-only` → the clipboard dispatchers. Do not re-open.
- **Tests first:** `selectOutput` case per mode (extend the existing
  `main_test.go` coverage); scaffold test asserts `mode =
  "paste-leave-transcript"` and the six-value comment; doctor verdict
  test per mode family. → `go test ./cmd/mavor/` (red).
- **Change:** `cmd/mavor/main.go`, `cmd/mavor/config_cmd.go` (+ its
  positional format arguments), `cmd/mavor/doctor.go`. Doctor's
  unknown-key report gains a hint: if the unknown keys include
  `output.driver`, `output.clipboard` or `output.restore_selection`, name
  `output.mode` as the replacement.
- **Gates:** `go test ./cmd/mavor/ ./internal/...`.
- **Commit:** `feat(output): wire output.mode through daemon, scaffold and doctor`.

**Task 4 — docs** (after 3).
- **Change:** `docs/reference/paste-based-output-dispatch.md` (mode table;
  its restore-by-default rationale marked superseded by D1–D2),
  `docs/user-guide.md` (`[output]` section), and the design doc's ledger
  (`Built: Yes`) and stage (`BUILT` with an UNMEASURED clause until the
  integration suite runs).
- **Gates:** `uvx vantage-check check docs/design/output-modes.md
  docs/design/output-modes-plan.md docs/reference/paste-based-output-dispatch.md`.
- **Commit:** `docs(output): describe the six modes`.

## Ships with

- Unit: mode parsing/validation/predicates; external-copy cancellation in
  both directions; `selectOutput` per mode; scaffold/default agreement.
- Existing tests asserting `driver`/`restore_selection` behavior are
  rewritten to the mode that reproduces their intent — not deleted.
- Integration: `just test-int` (paste dispatch paths unchanged in shape,
  but the config they load changed).
- Surfaces: scaffolded `config.toml`, `mavor config show` output,
  `mavor doctor` verdicts, daemon startup log line (mode, not driver).
- Cheap and the implementer's: predicate names and placement.
  **Stop and ask** if a seventh outcome seems necessary — the design's
  matrix claims six is complete.

## Don't

- Don't keep the old keys as aliases "for one release" — D6 rules it out.
- Don't detect clipboard managers anywhere in this change (P3).
- Don't tune `LeaseDuration` (explicit non-goal).
