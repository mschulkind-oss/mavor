---
title: "The overlay resizes itself, and that is the bug"
author: "Matthew Schulkind"
date: 2026-09-06
status: accepted
tags: [design, overlay, wayland, layer-shell, preview, performance]
summary: "The overlay resizes its Wayland surface to hug its contents. That one decision produces the stalls, the jumping, the vanishing pill and the buffer churn. This proposes one fixed surface, laid out internally, that never resizes."
vantage:
  status-chip: true
---

# The overlay resizes itself, and that is the bug

**Status:** DECIDED (2026-09-06). Every open question is settled; the Decision
Ledger carries the three rulings. Implementation in progress. Evidence gathered from a live
daemon and a headless sway on 2026-09-06.

**The short version.** The overlay asks the compositor for a new surface size
every time its contents change shape. That single decision is upstream of every
overlay symptom reported this week: a blocking round-trip inside the render loop
(the preview appearing ~15 s late), a re-centred surface on every width change
(the sideways walk), a race with a stale `configure` (the pill reduced to a
sliver), and a fresh buffer per resize. I propose the overlay allocate **one
surface, once, at a fixed size** and do all layout inside it. Nothing resizes,
so none of those failure modes have anywhere to live.

**The most important section is [§3](#3-why-a-fixed-surface-fixes-all-four)** —
that one change removes four distinct failures, which is the whole argument.

**Reads with:**
[`configuration-surface.md`](configuration-surface.md) (`overlay.preview_width`,
whose meaning this changes), [`../reference/how-mavor-works.md`](../reference/how-mavor-works.md)
(the overlay as built).

---

## 1. The verdict

**Stop resizing. Allocate one surface at a fixed size and lay out inside it.**

Three principles, cited later by number.

**P1. The compositor is not a layout engine.** Asking it to re-size and
re-position a surface every time a word arrives outsources layout to an
asynchronous protocol with its own opinions and its own latency. mavor knows
exactly what it wants to draw. It should draw it.

**P2. The render loop must never block.** It is a single goroutine that owns the
connection, drains a command queue and paints. Any blocking call inside it stops
the overlay reacting to anything — including the state changes that are the
overlay's entire job.

**P3. The pill must not move.** It is a status indicator. A thing that reports
status by sitting still is readable; a thing that hops around while reporting
status is what a user described as "all sorts of not good".

---

## 2. What happens today

The overlay is a `wlr-layer-shell` surface anchored to the top edge, which means
the compositor centres it horizontally. Its size is computed from its contents
by `SceneSize`, and when that changes the render loop calls `Surface.Resize`
mid-paint.

Four distinct failures come out of that, and it is worth being precise about
each because they look like four bugs and are one.

### 2.1 A blocking round-trip inside the render loop

`Resize` sends `set_size`, commits, and then waits for the compositor to answer.
The wait is a blocking read on the Wayland socket, performed *inside* `paint`,
which is called from the loop that also drains the command queue. While that
read blocks, `Show(Transcribing)` and `SetText` sit in the queue untouched.

This is why the preview "took maybe 15 s to pop in" and why "transcribing didn't
show up on time". Neither is slow recognition. The recogniser had already
produced the text — the log shows chunks being fed and returning characters —
and the overlay simply was not listening.

### 2.2 Re-centring on every width change

A top-anchored surface is centred by the compositor. A surface that grows to hug
its text therefore *moves* every time the text grows. From one dictation:

```
surface resized from_w=528 to_w=536   preview_chars=75
surface resized from_w=536 to_w=564   preview_chars=80
surface resized from_w=564 to_w=585   preview_chars=83
surface resized from_w=585 to_w=631   preview_chars=90
surface resized from_w=631 to_w=639   preview_chars=91
```

Five width changes in under two seconds, each one a re-centre. That is the
jumping, and capping the width only bounds how far it walks.

### 2.3 A race with a stale configure

The compositor may already have a `configure` in flight for the previous size
when `set_size` arrives. Traced against sway:

```
WL configure serial=9  w=329 h=56   (requested 960x91)
WL configure serial=11 w=960 h=91   (requested 960x91)
```

Accepting serial 9 leaves the surface 329 px wide for good, because the resize
path only runs when the *scene* size changes and it had not. Every later frame
then drew a 960 px scene into a 329 px buffer. The pill is centred in the wide
scene, so the crop showed a sliver of its left edge — "the pill doesn't even
always show up".

> [!WARNING]
> This one has a fix in the tree already ([`internal/wayland/protocol.go`](../../internal/wayland/protocol.go),
> `waitForSize`), and it is a patch over the resize rather than a cure. Keep the
> fix — a stale configure is a real protocol hazard worth handling — but do not
> mistake it for having solved the problem. A design with no resize never
> reaches that code path.

### 2.4 A new buffer per resize

Every accepted size change frees the shared-memory buffer and allocates another.
On a growing preview that is a reallocation every few hundred milliseconds, on
the render loop's own goroutine.

---

## 3. Why a fixed surface fixes all four

One surface, allocated once, at a size chosen before the first frame. The scene
is drawn *into* a region of it rather than defining it.

| Failure | Why it disappears |
| :--- | :--- |
| Blocking round-trip ([§2.1](#21-a-blocking-round-trip-inside-the-render-loop)) | `Resize` is never called in the steady state, so nothing in `paint` waits on the compositor |
| Re-centring ([§2.2](#22-re-centring-on-every-width-change)) | The surface's width never changes, so the compositor never re-centres it |
| Stale configure ([§2.3](#23-a-race-with-a-stale-configure)) | There is one configure, at startup, with nothing in flight to race |
| Buffer churn ([§2.4](#24-a-new-buffer-per-resize)) | One buffer for the life of the surface |

That is the entire argument. Four symptoms, one cause, one change.

---

## 4. The proposed geometry

**Overlay surface** *(coined here)* — the single fixed-size layer-shell surface.
It is not the pill and it is not the preview strip; it is the canvas both are
drawn on, and it is larger than either.

### 4.1 Size, chosen once

- **Width** is `overlay.preview_width` of the output width (0.5 by default),
  floored at the pill's natural width so a narrow screen still fits the pill.
  Where no `wl_output` is advertised, a fixed fallback is used.
- **Height** is the pill's height plus the gap plus the preview strip's height —
  **always**, whether or not a preview is showing.

Reserving the strip's height unconditionally is the deliberate part. The
alternative — a short surface that grows when a preview arrives — is a resize,
and resizes are what this document exists to remove. The reserved region is
transparent and nothing is drawn in it when there is no preview, so it costs
nothing visually: an idle overlay looks exactly as it does today.

`overlay.preview_width` keeps its name even though it now sets the width of the
whole surface, because the preview is still the thing that drives that width.
The comment on the key says so; the key does not change.

### 4.2 Where things are drawn

- The **pill** is drawn at a fixed origin: horizontally centred within the
  surface, vertically at the top. Because the surface is centred on screen and
  never changes width, the pill is screen-centred and **never moves**. This
  satisfies P3 directly, and it is the reason the strip's height is reserved at
  the bottom rather than the pill being centred vertically.
- The **preview strip** occupies the reserved region below the pill. When there
  is no preview text the region is simply transparent.

### 4.3 The input region — required, not optional

The surface is now much larger than its ink, and most of it is transparent. A
Wayland surface accepts pointer input across its whole extent unless told
otherwise, so a half-screen transparent overlay would swallow clicks in the
region it covers.

> [!IMPORTANT]
> `wl_surface.set_input_region` with an **empty** region makes the surface
> click-through. mavor sets no input region today, which is survivable only
> because the surface is currently small and hugs its ink. It is not survivable
> under this design, and shipping the fixed surface without it would trade four
> visual bugs for one much worse functional one.

### 4.4 What still triggers a resize

Exactly one thing: the output geometry changing — a monitor swap, a resolution
change, a hotplug. That is rare, it is not on the hot path, and handling it by
tearing down and rebuilding the surface is acceptable.

---

## 5. Threads, and what may be dropped

Geometry is half the story. The other half is that the overlay and the audio
path are on separate goroutines and must stay that way — neither should ever
wait on the other.

**P4. Nothing on the dictation path waits on the overlay.** Recording,
recognition and typing are the product. The overlay is a report on them, and a
report that stalls its subject is worse than no report.

### 5.1 Who owns what

| Goroutine | Owns | Must never |
| :--- | :--- | :--- |
| Daemon event loop | the state machine, the IPC socket | block on the overlay or on transcription |
| Recorder + level monitor | the capture subprocess, the level ring | block on the overlay |
| Preview feed | the streaming recogniser and its stream | block on the overlay |
| Transcription (per cycle) | the main model, history, the emitter | touch the overlay's internals |
| Render loop | the Wayland connection, the surface, the buffer, the scene | block on the compositor ([§2.1](#21-a-blocking-round-trip-inside-the-render-loop)) |

The render loop stays the single writer of everything Wayland. That part is
already right and is worth keeping explicit.

### 5.2 The queue is the wrong shape

Today every update is a closure pushed onto one buffered channel, and a full
channel drops the closure. The comment on that drop says it is better than
stalling the audio path, which is true — and it is applied to **every** update
including `Show`.

> [!WARNING]
> A dropped level sample is invisible. A dropped `Show(Transcribing)` is the
> overlay never changing state, which is exactly the "transcribing didn't show
> up" report. The two must not share a policy, and today they share a code path.

### 5.3 Latest-wins state instead of a queue of edits

Replace the queue with a small piece of shared state the producers write and the
render loop reads once per frame: the visual state, the preview text, and the
level ring.

The insight is that **every one of these is idempotent** — only the newest value
has any meaning. A queue is the wrong structure for that, because it can only
choose between blocking the producer and losing the newest value. A
latest-wins slot has neither problem: producers never block, and nothing that
matters is ever lost, because being overwritten by something newer is exactly
what should happen to a superseded value.

- **Producers** take a mutex, write, and return. Never block on the loop.
- **The render loop** takes a snapshot each frame and paints it.
- **Nothing is dropped in a way that changes the outcome.** A level sample
  overwritten before it was painted was never going to be seen; a `Show`
  overwritten by a newer `Show` is correct by definition.

Close stays a signal rather than a value: it happens once and must not be lost.

### 5.4 Timing in the logs

Every log line already carries a wall-clock timestamp, which answers "when" and
not "how long". The stages that can be slow each record their own duration, so a
report of "it lagged" is answerable from the log alone rather than by
reproducing it:

- Per frame: render duration, and the age of the scene being drawn — how long
  the newest update waited before it reached the screen. That second figure is
  the one that would have identified [§2.1](#21-a-blocking-round-trip-inside-the-render-loop)
  immediately.
- Per preview chunk: bytes in, characters out, recogniser time.
- Per dictation: recording duration, transcription time, emit time, and
  characters per second typed.
- At every state transition: how long the previous state lasted.

---

## 6. Behaviour this must get right

- **Degenerate: no `wl_output`.** Width unknown. Use the fallback and carry on;
  an overlay with an unexpected width beats no overlay.
- **Degenerate: an output narrower than the pill.** Width floors at the pill's
  natural width. The surface may then exceed the configured fraction, which is
  correct — the fraction is a cap on the preview, not a promise to hide the pill.
- **Degenerate: empty preview.** The reserved region is transparent. No layout
  changes, nothing moves.
- **Degenerate: preview longer than the strip.** Already handled: the tail is
  shown with a leading ellipsis, on one line.
- **Failure: the compositor imposes a size.** It is entitled to. Accept it,
  lay out within whatever was granted, and do not argue by re-requesting — that
  is a resize loop.
- **Failure: a frame cannot be rendered.** Skip the frame, keep the loop alive.
  Already true in the tree and worth keeping: a render loop that stops is an
  overlay that never returns.
- **Ordering:** the render loop remains the single writer of the surface, the
  buffer and the scene. No second goroutine draws.
- **Forbidden:** `paint` must never block on the compositor. That is the rule
  [§2.1](#21-a-blocking-round-trip-inside-the-render-loop) was broken by, and it
  should be stated where the loop is, not only here.
- **Done looks like:** across a two-minute dictation, the log records zero
  `surface resized` lines after startup; the pill's screen position is identical
  in a screenshot taken before the preview appears and one taken after; a state
  change to Transcribing is visible within one frame of the daemon requesting
  it; and a click in the transparent region reaches the window underneath.

---

## 7. Alternatives considered

**Keep resizing, but debounce it.** Fewer resizes, same four failure modes,
plus a new latency knob to tune. Rejected: it makes the symptom rarer without
making it impossible, which is the worst place to stop.

**Keep resizing, but do it asynchronously.** Moves the blocking round-trip off
the paint path and fixes [§2.1](#21-a-blocking-round-trip-inside-the-render-loop)
only. The re-centring and the buffer churn remain, and a resize racing a paint
is harder to reason about than either alone. Rejected.

**A full-screen-width surface.** Simplest possible geometry, and it never
re-centres because it cannot. Rejected as overkill: it makes the input region a
whole-screen hazard rather than a half-screen one, and buys nothing over a fixed
half-width surface.

**Two surfaces, one for the pill and one for the strip.** The pill would be
genuinely independent of the preview. Rejected for now: two layer surfaces mean
two configure lifecycles and two anchoring problems, which is more protocol
surface than the problem needs. Worth revisiting only if the reserved-height
approach proves visually wrong.

**Left-align the pill instead of centring it.** Would stop the pill moving under
the *current* resizing design without any of this work. Rejected as a patch: it
addresses [§2.2](#22-re-centring-on-every-width-change) alone and leaves the
stalls, the stale configure and the churn untouched.

---

## 8. Risks

| Risk | Mitigation |
| :--- | :--- |
| A large transparent surface swallows pointer input | [§4.3](#43-the-input-region--required-not-optional): an empty input region, and an integration test that clicks through it |
| Blitting a larger surface every frame costs more | It is a memory copy of a fixed buffer, against the reallocation and round-trip it replaces. Measure `render_ms` before and after rather than assuming |
| The reserved strip height makes the overlay look tall when idle | The region is transparent, so only the pill is visible. If it still reads wrong, the two-surface alternative in [§7](#7-alternatives-considered) is the escape hatch |
| Output changes are now the only resize path, and therefore the least tested | Rebuild the surface wholesale on output change rather than resizing it, so the rare path uses the same code as startup |

---

## 9. Non-goals

- **Not** a redesign of what the overlay looks like. Same pill, same colours,
  same waveform, same preview strip. This is about where they sit and when the
  surface changes size.
- **Not** a fix for preview latency or the preview's starting point. Those are
  real and are [OQ-3](#decision-ledger); this design removes one large cause of
  apparent latency but does not claim to be the whole answer.
- **Not** a change to the preview's content rules — one line, tail, capped, and
  lower-cased for shouty models all stay as they are.
- **Not** a second overlay backend. GNOME and macOS have their own documents.

---

## 10. What I would build, in order

**First, the input region**, before anything grows. It is independently correct,
it is small, and doing it first means the fixed surface never ships in a state
where it eats clicks.

**Second, the fixed geometry**: compute the surface size once, reserve the strip
height, draw the pill at a fixed origin, and delete the resize call from the
paint path. This is the change that makes the four failures unreachable.

**Third, the integration tests that prove it** — no `surface resized` after
startup across a long preview, the pill in the same pixels before and after the
preview appears, and a click reaching the window below. These belong with the
change, not after it.

**Fourth, output-change handling**: rebuild the surface when the output geometry
changes, reusing the startup path.

---

## Decision Ledger

| ID | Ruling / Decision | Date | Settled in |
| :--- | :--- | :--- | :--- |
| OQ-1 | Reserve the strip height. Nothing is shown when idle, so the transparent region costs nothing visually | 2026-09-06 | [§4.1](#41-size-chosen-once) |
| OQ-2 | Keep `overlay.preview_width` as a fraction and keep the name; it is still the preview that drives the width. Fix the comment, not the key | 2026-09-06 | [§4.1](#41-size-chosen-once) |
| OQ-3 | Preview latency and its starting point stay a separate investigation, measured after the render loop stops stalling | 2026-09-06 | [§9](#9-non-goals) |
