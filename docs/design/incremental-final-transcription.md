---
status: accepted
stage: BUILT
next: "Graduate the verified opt-in lifecycle into the system reference"
depends-on: [../research/incremental-final-transcription.md]
---

# Final recognition can run during capture without emitting during capture

**Status:** 2026-10-02, implemented against `779917b`. **MEASURED:** both requested
models ran paced fixtures; [results and limits](../reports/incremental-final-prototype.md)
and [QA](../qa/incremental-final-transcription-qa.md) record verification.

> **In short.** These explicit live-final prototypes, not promoting preview
> caches: a successful main session finishes once after release without replaying
> the whole recording, and a failed session safely replays the original audio.

**Cost:** A single audio reader, bounded decode work and joined resource lifetimes
replace independent preview and final feeding in opted-in cycles.

**Start at:** [Lifecycle](#lifecycle).
**Needs your ruling:** None; implementation decisions are authorized within scope.
**Reads with:** [research](../research/incremental-final-transcription.md),
[implementation plan](../plans/incremental-final-transcription-plan.md).

## Mode definitions and compatibility

**Final session** *(coined here)* means the cycle-owned main recognizer work whose
finished result alone may enter history/output; it is not the preview recognizer.
**Native streaming** means a model intrinsically consumes audio incrementally;
**segment processing** means mavor transcribes separate completed audio portions
with an offline model. Neither describes quality or a speed guarantee.

Add one opt-in key, `advanced.final_mode`, with exact values:

| Value | Behavior |
| :--- | :--- |
| `after-stop` | Default, including unset/empty: existing full WAV transcription after release. |
| `streaming` | Main native stream fed during recording; only its finalized result emits. |
| `segments` | Offline v2 processes completed silence-bounded portions during recording; ordered aggregate emits after release. |

Unknown values fail validation, never silently normalize to a prototype.
Unsupported model/mode combinations fail startup with supported choices. Catalog
eligibility comes from one shared models helper. Streaming is supported only for
catalog Sherpa entries marked intrinsically streaming and actually loaded online;
segments only for `parakeet-tdt-0.6b-v2`. Explicit custom model paths remain usable
in `after-stop` but reject these prototype modes: a path/layout does not prove
catalog identity or vetted segment eligibility. Placement cannot broaden support.

No migration: existing configs and main/preview defaults remain unchanged.
`silence_filter` stays false by default; existing enabled rejection and preview
recognition override remain intact in `after-stop`. In live modes finish the main
session before evaluating rejection: recognized main or companion words override
low energy; with no recognition, enabled energy rejection still applies. A failed
energy check still proceeds. Silence detection for a segment boundary never drops
quiet/short audio or rejects a segment.

## Lifecycle

1. Warm the selected main model before accepting capture. Create a new cycle
   context/session; start native main streaming independent of preview.enabled.
2. One reader consumes all new 16 kHz mono signed 16-bit little-endian PCM.
   Copy it into the final queue, count contiguous bytes, and separately offer
   copies to a companion if selected. No preview/final competing reads.
3. Release invalidates overlay writes immediately. Stop/join the reader while
   preserving final work, stop capture and obtain its flushed original WAV.
   Finish reconciles all unread WAV bytes after the counted prefix, including a
   sub-tick tail. Never rely on `ReadChunk` after recorder stop.
4. Drain final work in order, then finish native input (including right-context
   padding) or decode the last offline segment, even if very short or quiet.
   Successful live work returns its finalized text and **does not** call full
   `Transcribe(WAV)`. Empty successful text is a no-op, not a replay trigger.
5. Start/feed/finish/read/coverage/overload failure invalidates the entire live
   aggregate. Join/abort it before a single complete `Transcribe(original WAV)`
   using the same selected main model and existing chunking policy. Log requested
   and actual mode plus reason; no model or quality fallback is introduced.
6. Clean annotations and whitespace once, apply enabled energy policy, check
   cancellation/current cycle, append history and dispatch once. Retain original
   WAV through any replay and dispatch; then remove recording and segment files.

A recorder without live reads truthfully logs complete replay after stop, not
successful incremental work. Zero-byte input finishes safely without fabricated
text; malformed or inconsistent WAV coverage replays or returns the underlying
error. Odd PCM boundaries carry one byte into the next feed; a dangling final
byte invalidates live coverage. Read failures are fatal to live coverage, not
silently skipped ticks.

Cancellation abandons output/history, stops capture, cancels workers and waits
for native calls to return before deleting streams or closing recognizers. Native
calls cannot promise immediate cancellation. Never use an unbounded background
finalization context on shutdown. A new recording cannot reuse a recognizer until
its previous feed/finish/abort is joined. No detached work survives cycle cleanup.

## Preview ownership

- With streaming main + auto/main preview, show that session's partial callback;
  never start a second stream on the same recognizer. With preview disabled,
  continue final work but suppress overlay writes.
- With segment main + auto/phrase preview, show the ordered main segment aggregate
  as provisional overlay text; do not launch concurrent main phrase decoding.
- An explicitly selected distinct companion gets its own bounded queue and stream
  via the same reader. Slow/full/failed preview work can be dropped or disabled
  with a warning; it must not stall main feeding or trigger final replay.
- Explicit `preview.source = "phrases"` in live final modes shares main partials
  or segment aggregates; the preview plan reports that no separate phrase decode
  runs. A companion resolving to the main object is rejected. Companion configuration
  does not inherit main final mode. Only one selected source writes overlay text.
- Discard companion final text, except boolean recognition evidence for the
  legacy enabled energy filter. Companion drain does not gate ordinary final
  output; shutdown and next companion reuse still join it.

## Bounded segment and backlog policy

Use a sequential decode worker; never accumulate unbounded concurrent inference.
Live queued/in-flight audio has a 24-second budget (768000 PCM bytes). Crossing
it abandons live mode and logs overload; capture continues intact for replay.
Count queued work plus currently decoding audio, not merely channel length.

Segment boundaries use 30 ms audio frames, not ticker counts: accept a boundary
at the end of 450 ms continuous low-energy audio once the segment is at least
2 seconds long. Use the existing energy threshold solely to locate a cut.
No overlap: adjacent sample ranges cover every captured sample exactly once;
join segment texts in order with spaces, never suffix/prefix token deduplication.
This preserves intentional repetition and avoids claiming arbitrary text matches
prove acoustic overlap. Maximum unsplit portion is 12 seconds; if no safe pause
exists before it exceeds that bound, abandon segments and replay the full WAV
rather than forcing a cut through a word. Exact thresholds are prototype policy,
not measured tuning. Finishing always submits the remaining tail, below minimum
length included. Segment temporary files are private to the cycle and removed.

## Truthful capability and execution reporting

Plain/verbose/JSON listing must separate intrinsic native streaming, supported
main final modes, and the configured mode for the active catalog row. Preserve
JSON `streaming` as intrinsic meaning; add capability fields without changing
its meaning. Non-active rows have no selected execution mode. Unknown/custom
paths do not activate a guessed catalog row. A configured mode is not proof a
cycle ran live: runtime logs/result statistics disclose actual replay and why.
Unsupported selections report unsupported, not an invented effective live mode.

## Evidence and non-goals

A permanent paced harness uses the same final-session implementation as production,
with an independent wall-clock producer. Warm loading is timed separately; record
release instant, capture-stop duration, pending audio at release, queue drain from finish entry,
finalization, replay, dispatch and total release-to-final/dispatch durations.
Compare actual finalized and after-stop transcripts with fixture references using
word and character edit error rates; retain texts and coverage counts. Record
CPU/thread/library/model paths and concurrent load. Exceeding arrival capacity
must show overload/backlog/replay, not claim rescued latency by starting early.
Use both real speech and the 67.86-second technical prompt; the reference is an
annotation to compare, not an exact-match guarantee. Repeat short/quiet/tail and
natural repetition cases in permanent deterministic tests.

Out of scope: GPU readiness/retry, changed default model/preview, host deployment,
cloud calls, unsolicited downloads, automatic quality fallback, generated report
hand edits, GNOME HUD changes, or claims that any architecture is immune to loops.
CPU large-v3 at roughly twice audio duration cannot keep up merely by starting early.

## Alternatives and success criteria

| Alternative | Verdict |
| :--- | :--- |
| Reuse preview StopStream cache | Rejected: no ownership or complete coverage guarantee. |
| Forced overlap with text deduplication | Rejected for this prototype: natural repeated words can disappear. |
| Allow every offline model | Rejected: capability is not measured real-time suitability. |
| Change defaults based on historical report | Rejected: release latency was not measured. |

Success means paced runs actually execute both requested modes, full live coverage
is observable, successful cycles avoid full replay, failures disclose complete
replay, cancellation emits nothing, and all unchanged default-mode tests pass.
Latency/accuracy results may be unfavorable; honest measurements still satisfy
the prototype, but missing requested real-model execution is incomplete evidence.
