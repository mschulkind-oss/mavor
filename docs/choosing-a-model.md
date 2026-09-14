---
title: "Choosing a Model"
author: "Matthew Schulkind"
date: 2026-09-13
status: accepted
tags: [models, whisper, sherpa, gpu, accuracy, latency, guide]
summary: "Which of mavor's 31 models to actually use, decided from measurements rather than reputation — including why the largest Whisper models are the wrong choice for dictation, and which streaming model is finally accurate as well as live."
vantage:
  status-chip: true
---

# Choosing a Model

`mavor` ships a catalog of 31 models. This page says which one to use and
why, from measurements rather than reputation. The measurements themselves,
with the machine they came from and the method, are in
[`model-benchmarks.md`](./reports/model-benchmarks.md); rerun them on your own
hardware with `just bench`.

Choosing is one line. There is no engine to pick alongside it: the model
decides its own runtime — a `whisper-*` model runs on whisper.cpp, everything
else runs in-process on ONNX Runtime through sherpa-onnx — and mavor derives
where that runtime runs. `mavor doctor` prints what it chose.

> [!WARNING]
> **Every catalog name carries its model family, and there are no aliases.**
> `whisper-base.en`, not `base.en`; the old bare names and short aliases were
> deleted rather than kept working. A name that is not in the catalog is a
> fatal error at daemon start, naming the closest entries — never a quiet
> substitution. `mavor models list` is the list of names that resolve.

> [!IMPORTANT]
> Read the [caveats](#what-these-numbers-do-and-do-not-tell-you) before
> treating a small accuracy difference as real. The speed, memory and
> **formatting** results are solid; most of the word-error differences are
> one word on one clip.

## The short answer

| If you want… | Use | Why |
|---|---|---|
| **The default** | `whisper-base.en` | Best accuracy measured — the only 0.0% word error rate in the run. 1.64 s for 20 s of speech, 308 MB. |
| **The lightest thing that works** | `whisper-tiny.en` | 856 ms, 198 MB, and still fully punctuated and capitalised. |
| **Languages other than English** | `parakeet-tdt-0.6b` | 25 languages, clean formatting. Costs 1.54 GB of RAM. |
| **A non-English model that stays small** | `canary-180m` | English, Spanish, German, French in 460 MB, formatting as good as `whisper-base.en`. |
| **Words appearing while you speak** | `whisper-base.en`, unchanged | The preview companion — `zipformer-streaming` by default — paints the overlay live; your typed text still comes from `model`. Costs 161 MB resident on top of it. [Below](#you-do-not-have-to-choose-the-preview-companion). |
| **A streaming model as your main model** | `nemotron-streaming-en-560ms` | 1.8% word error rate while decoding live, punctuated and capitalised, first words 433 ms in. Costs 964 MB. [Below](#streaming-text-while-you-speak). |
| **Maximum accuracy** | `whisper-base.en`, still | See below — the large models do not deliver this. |

The six models that joined the catalog on 2026-09-13 were unmeasured when this
page was first written. The 2026-09-13 benchmark run covers them, so they sit in
the tables below like everything else; what that run found about them, and the
three things about them no measurement decides, are
[at the bottom](#the-six-models-added-on-2026-09-13).

Set it in `~/.config/mavor/config.toml`. One key, whichever family you pick:

```toml
model = "whisper-base.en"

# A sherpa model is the same line — the in-process runtime is always linked in:
# model = "canary-180m"
```

Then run `mavor setup` to fetch it and `mavor doctor` to confirm the daemon
will use it. A model the config names but the cache does not have is fatal at
daemon start, so `setup` is the step that makes the edit real.

## Do not reach for the biggest model

This is the result most likely to contradict what you expected.

`whisper-large-v3`, `whisper-large-v3-turbo`, `whisper-distil-large-v3` and
`whisper-medium.en` all return **lowercase, unpunctuated text**:

| Model | Output on the test clip | Punct/word | Capitals F1 |
|---|---|---:|---:|
| `whisper-base.en` | `Lux is in the pit. He cannot sit still…` | 0.16 | **1.00** |
| `whisper-medium.en` | `Lux is in the pit he cannot sit still…` | 0.00 | 0.57 |
| `whisper-large-v3` | `lux is in the pit he cannot sit still…` | 0.00 | **0.00** |

Word error rate is the same across all of them. On a metric that only counted
recognised words, these models would look identical — and for dictation they
are not remotely identical, because one produces text you can paste into a
document and the other produces text you have to re-punctuate by hand.

`whisper-large-v3` is also **nearly 17× slower** than `whisper-base.en` on CPU
(27.38 s against 1.64 s for the same 20 seconds of audio) and wants 3.84 GB of
RAM.

So: the largest Whisper models cost more, take longer, and produce worse
text for this purpose. Unless you have a specific reason, skip them.

## Speed and memory

Every model that produced usable output, fastest first, whisper models on the
stock CPU build. `RTF` is the fraction of real time consumed — below 1.0 is
faster than speech. The streaming models have
[their own table](#streaming-text-while-you-speak), because a total time is not
what you judge them on.

| Model | Engine | Time (20 s audio) | RTF | Peak RAM |
|---|---|---:|---:|---:|
| `whisper-tiny.en` | whisper | 856 ms | 0.043 | 198 MB |
| `whisper-base.en` | whisper | 1.64 s | 0.082 | 308 MB |
| `moonshine-base` | sherpa | 2.05 s | 0.103 | 541 MB |
| `sensevoice-small` | sherpa | 2.19 s | 0.109 | 1.43 GB |
| `canary-180m` | sherpa | 3.73 s | 0.186 | 460 MB |
| `whisper-small.en` | whisper | 4.89 s | 0.244 | 775 MB |
| `parakeet-tdt-0.6b-v2` | sherpa | 5.44 s | 0.272 | 1.56 GB |
| `parakeet-tdt-0.6b` | sherpa | 5.93 s | 0.296 | 1.54 GB |
| `cohere-transcribe` | sherpa | 9.76 s | 0.488 | 3.38 GB |
| `canary-1b` | sherpa | 12.09 s | 0.605 | 2.40 GB |
| `whisper-medium.en` | whisper | 14.70 s | 0.735 | 2.05 GB |
| `whisper-large-v3` | whisper | 27.38 s | 1.369 | 3.84 GB |

`whisper-medium.en` at RTF 0.735 means a 20-second dictation takes nearly 15
seconds to transcribe — you would be waiting. `whisper-large-v3` at 1.369 is
**slower than speaking**. Neither is usable on CPU without a GPU behind it.

`cohere-transcribe` is the memory outlier: **3.38 GB**, more than twice
`parakeet-tdt-0.6b`, for the same 1.8% word error rate and the same formatting.
Nothing in this run pays for that gigabyte and a half.

## GPU makes the large models possible

The packaged `whisper-cpp` on most distributions is CPU-only. Build a
Vulkan-enabled one with `just bench-gpu-build` and the picture changes,
measured with the same binary and only whisper.cpp's `-ng` flag differing:

| Model | CPU | GPU | Speed-up | RAM (CPU → GPU) |
|---|---:|---:|---:|---|
| `whisper-tiny.en` | 884 ms | 412 ms | 2.1× | 223 MB → 107 MB |
| `whisper-base.en` | 1.49 s | 509 ms | 2.9× | 327 MB → 121 MB |
| `whisper-small.en` | 3.95 s | 757 ms | 5.2× | 800 MB → 148 MB |
| `whisper-medium.en` | 12.07 s | 1.55 s | **7.8×** | 2.07 GB → 175 MB |
| `whisper-large-v3` | 23.10 s | 2.49 s | **9.3×** | 3.86 GB → 207 MB |

Two things worth noticing. The speed-up grows with model size, so the GPU
matters least for the model you were probably going to use anyway. And host
memory *falls* — the weights live on the card instead — so
`whisper-large-v3` on a GPU is lighter on RAM than `whisper-base.en` on CPU.

This does not rescue the large models' formatting problem. It only makes them
fast enough to be worth arguing about.

`advanced.gpu` is `"auto"` or `"off"`, and it applies to whisper models only.
whisper.cpp uses whatever GPU backend its build loaded, for the whole model or
not at all; `"off"` is the escape hatch for a broken driver. There is no layer
count to set.

> [!NOTE]
> Sherpa models get no GPU column at all, and no setting either. The ONNX
> Runtime vendored by the Go binding is a CPU-only build carrying no execution
> providers, so a sherpa model runs on the CPU whatever `advanced.gpu` says.
> This is a packaging limitation, not a setting you can fix.

## Streaming: text while you speak

Seven catalog models decode incrementally rather than waiting for you to stop.
Three are long-standing — `zipformer-streaming`, `zipformer-streaming-20m` and
`fastconformer-streaming` — and four arrived on 2026-09-13. All seven are
measured now, and the result changed what this section used to say: **a
streaming model is no longer automatically an inaccurate one.**

**Time to first token** is the gap between the first chunk of audio and the
first text the model emits, with the model already loaded — a daemon holds it
warm long before you speak. It is what decides whether a preview feels live; a
total time cannot tell you that. **Punct/word** is punctuation marks per word of
output and **capitals F1** balances the words a model capitalised correctly
against the ones it missed or invented, 1.00 being agreement with the reference;
both are defined with the rest of the method in
[the report](./reports/model-benchmarks.md#accuracy). Every figure below is that
report's `sherpa / cpu / streaming` row — audio fed in 100 ms chunks, from a
model already loaded. The daemon's own preview tick is 30 ms, which is the
cadence the companion comparison
[below](#you-do-not-have-to-choose-the-preview-companion) uses; the two are not
directly comparable.

| Model | First token | Total | WER | Punct/word | Capitals F1 | Peak RAM |
|---|---:|---:|---:|---:|---:|---:|
| `zipformer-streaming` | **107 ms** | 4.05 s | 7.3% | 0.04 | 0.20 | 161 MB |
| `zipformer-streaming-20m` | 108 ms | 1.46 s | 9.1% | 0.02 | 0.15 | 112 MB |
| `fastconformer-streaming` | 382 ms | 8.17 s | 12.7% | 0.00 | 0.00 | 550 MB |
| `nemotron-streaming-multi-560ms` | 414 ms | 7.01 s | 7.3% | 0.09 | 0.80 | 976 MB |
| `nemotron-streaming-en-560ms` | 433 ms | 7.90 s | **1.8%** | 0.15 | 0.91 | 966 MB |
| `nemotron-streaming-en-80ms` | 1.80 s | 30.23 s | 3.6% | 0.15 | 0.83 | 958 MB |
| `parakeet-unified-en-streaming-240ms` | 5.27 s | 101.16 s | 3.6% | 0.16 | 0.91 | 1024 MB |

`nemotron-streaming-en-560ms` is the row to know about. Its 1.8% WER ties
`parakeet-tdt-0.6b`, `canary-180m`, `cohere-transcribe` and every large Whisper
model in the catalog — one word behind `whisper-base.en`'s 0.0% — and it gets
there while emitting its first words 433 ms in, punctuated and capitalised. It
is the first entry in this table that does not ask you to trade accuracy for
liveness, which makes it a real candidate for `model` and not only for the
overlay. It costs 966 MB resident, six times `zipformer-streaming`. It held the
preview companion slot for a few hours on 2026-09-13 and lost it again on
something this table cannot show —
[below](#you-do-not-have-to-choose-the-preview-companion).

`fastconformer-streaming` sits at the other end: 12.7% WER, the worst of any
streaming model measured, with **no punctuation and no capitalisation at all**.
It held the companion slot until 2026-09-13 and lost it on exactly that —
though the model that ended the day in the slot was picked on a different
question again ([below](#you-do-not-have-to-choose-the-preview-companion)).

Two of the newer entries are far slower than their chunk size suggests:

- **`nemotron-streaming-en-80ms` is not the low-latency one.** The same weights
  as the 560 ms export, cut into chunks seven times shorter, take **1.80 s** to
  first token against the 560 ms tier's 433 ms, and RTF 1.511 puts them slower
  than real time overall. Chunk size is not latency: seven times shorter means
  seven times as many encoder invocations over the same audio, and on this CPU
  each invocation costs more than the shorter chunk saves.
- **`parakeet-unified-en-streaming-240ms` is unusable for dictation.** RTF
  5.058 — five times slower than the speech it is transcribing — and 5.27 s
  before the first word appears. Its own non-streaming export,
  `parakeet-unified-en`, finishes the whole clip in 5.54 s at 1.8% WER.

`zipformer-streaming` still wins on latency and on footprint: first token at
107 ms in 161 MB, which nothing else here approaches. What it does not do is
format — 0.04 punctuation marks per word, capitals F1 0.20 — so its text reads
as a live caption rather than as the sentence about to be typed. That is the
right shape for an overlay and the wrong shape for text you keep, which is why
it is the default
[preview companion](#you-do-not-have-to-choose-the-preview-companion) and not a
suggestion for `model`.

Streaming is worth it when watching words appear matters more than getting them
right first time. For ordinary dictation, where you release a key and want
correct text, a batch model is still the better trade — with
`nemotron-streaming-en-560ms` as the one entry where that reasoning no longer
obviously holds.

Before ranking any of this on WER alone, read the
[caveats](#what-these-numbers-do-and-do-not-tell-you): the models tied at 1.8%
are tied on a single word of a 20-second clip, which is the fixture running out
of resolution rather than a genuine dead heat.

### You do not have to choose: the preview companion

Picking a streaming model as `model` means accepting its accuracy for the text
you keep. You rarely need to, because mavor can run a second streaming model
*alongside* your main model purely to paint the overlay while you speak. That
second model is the **preview companion**: it is fed the same audio, emits
partial text continuously, and **never contributes a word to the final
transcript** — the text that gets typed is always `model`'s, produced once,
when you release the key.

`preview.source = "auto"` loads `zipformer-streaming` — 296.0 MB to download,
320.2 MB unpacked on disk — and `mavor setup` pulls it alongside your main
model.

**A companion is chosen on cadence, not on accuracy.** Because it never emits,
every word it gets right is thrown away when you release the key. What you
actually perceive is whether the line under your cursor keeps moving. The
number that decides that is the **update cadence**: how often the painted text
changes, and how long the longest gap between two changes lasts. (The term is
this project's own — it names the thing the benchmark report has no column
for.) That report's liveness measure is *time to first token*, which says when
the preview starts and nothing at all about what happens after it does.

Getting that backwards is how this slot moved three times in eight days.
Measured directly — feeding
[`real_speech.wav`](../test/fixtures/real_speech.wav) in 30 ms chunks through
the daemon's own preview tick and timing every repaint, which is not something
[the benchmark report](./reports/model-benchmarks.md) can currently tell you:

| Model | Updates | Mean gap | Worst gap | Slowest call | CPU | WER | Peak RAM | Download |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| `zipformer-streaming` (the default) | 53 | 357 ms | **660 ms** | **23 ms** | **0.06×** | 7.3% | 161 MB | 296 MB |
| `fastconformer-streaming` (default until 2026-09-13) | 72 | 259 ms | 960 ms | 122 ms | 0.30× | 12.7% | 550 MB | 429 MB |
| `nemotron-streaming-en-560ms` (default for a few hours) | 26 | **716 ms** | 1140 ms | 139 ms | 0.20× | **1.8%** | 966 MB | 442 MB |

CPU is the fraction of the real-time budget the companion spends decoding: at
0.06× the Zipformer uses a twentieth of the time it has.

The Nemotron took the slot on the morning of 2026-09-13 on the strength of that
1.8%, and a user reported the same day that the preview "comes in chunks rather
than continuously". It does: a 560 ms cache-aware chunk emits about twice a
second no matter how often you feed it, so 26 updates across 20 seconds arrive
as visible lumps. Accuracy bought with smoothness is a bad trade for text that
is thrown away.

Against `fastconformer-streaming`, the default until that morning, the
Zipformer wins on nearly everything: a worst-case gap of 660 ms against 960 ms
— and the worst case is what you perceive as a stall — 23 ms per call against
122 ms, a twentieth of the real-time budget against a third, half the word
error rate, and a smaller download and a smaller resident footprint. It loses
on mean gap alone, 357 ms against 259 ms.

> [!NOTE]
> **This default costs less than either of the two before it.** 296 MB to
> download against 429 MB and 442 MB, and 161 MB held resident for the life of
> the daemon against 550 MB and 966 MB. If you want smaller still,
> `preview.source` takes a model name: `zipformer-streaming-20m` is 112 MB
> resident. If you would rather read accurate text in lumps, it takes
> `nemotron-streaming-en-560ms` (966 MB resident) too. Run `mavor setup` after
> the edit and it fetches whichever you named.

**It shouts, and that is handled.** The Zipformer emits upper case with almost
no punctuation — 0.04 marks per word, capitals F1 0.20, the two lowest scores
in the streaming table. The daemon lowercases an all-upper-case partial before
painting it (`daemon.soften()`), precisely so a model like this one reads as a
preview rather than as shouting. What reaches the overlay is a lower-case live
caption.

It also gets the opening word of the fixture wrong: `LOOK`, then `LOOKS IS`,
where the word was "Lux". So did the FastConformer it replaces (`lux`, then
`luxe`). Only the Nemotron gets it right, and only in lumps — no model in this
size class both opens correctly and updates smoothly, so do not pick one
expecting both. `nemotron-streaming-en-560ms`, `fastconformer-streaming` and
`zipformer-streaming-20m` all stay selectable by name.

Naming the Nemotron here also picks up a licence unlike the rest of the
catalog's — [see below](#the-nemotron-models-and-a-new-family-in-the-listing)
— which matters if you redistribute the weights, and not at all if you dictate
with them. The roadmap records how the slot was decided, and twice undecided,
as
[item 1e](./roadmap.md#-1e-the-preview-companion-default-is-zipformer-streaming--resolved-2026-09-13).

So the streaming table above is about a trade you only make deliberately: for
words on screen while you talk, keep a batch `model` and let the companion do
it.

## Sherpa models

Every non-whisper model in the catalog is one of these. They run in-process
rather than shelling out, and the runtime is linked into the binary — there is
one build and it is cgo, so nothing here needs a build tag or a second
artifact.

The batch ones worth considering, fastest first:

| Model | Languages | Time | RAM | Formatting | Notes |
|---|---|---:|---:|---|---|
| `zipformer-ctc` | en | 1.39 s | 480 MB | None | Fast, 3.6% WER, no formatting |
| `moonshine-base` | en | 2.05 s | 541 MB | None | Fast, but no punctuation or capitals |
| `sensevoice-small` | zh, en, ja, ko, yue | 2.19 s | 1.43 GB | Good | Chinese and Japanese |
| `canary-180m` | en, es, de, fr | 3.73 s | 460 MB | **Excellent** | Full formatting for a fraction of the memory |
| `parakeet-tdt-0.6b-v2` | en | 5.44 s | 1.56 GB | Excellent | The English-only sibling of the row below |
| `parakeet-tdt-0.6b` | 25 languages | 5.93 s | 1.54 GB | Excellent | The multilingual choice |
| `cohere-transcribe` | 14, English in practice | 9.76 s | 3.38 GB | Excellent | Accuracy `canary-180m` matches in 460 MB |
| `canary-1b` | 25 languages | 12.09 s | 2.40 GB | Excellent | Slow for what it adds over `canary-180m` |

`canary-180m` is the one to know about. Four other sherpa models format as well
as it does — `parakeet-tdt-0.6b`, `parakeet-tdt-0.6b-v2`, `cohere-transcribe`
and `canary-1b` each pair punctuation with a 1.00 capitals F1 — and the
lightest of those still wants 1.54 GB. `canary-180m` does it in 460 MB, in less
than a third of `canary-1b`'s time, across four languages.

The remaining catalogued sherpa models — `parakeet-ctc`, `parakeet-unified-en`,
`paraformer`, `zipformer-offline`, `moonshine-tiny` — are measured in
[`model-benchmarks.md`](./reports/model-benchmarks.md) but are not better than
something above at any job. The streaming entries are judged on different
criteria and live in [their own section](#streaming-text-while-you-speak):
`zipformer-streaming` earns its place as the default preview companion rather
than as a `model`, with `zipformer-streaming-20m` as the lighter version of the
same idea, `fastconformer-streaming` is the least accurate model in that
section, and `nemotron-streaming-en-560ms` is the one streaming entry that
competes with this table on its own terms.

## The six models added on 2026-09-13

Six sherpa models joined the catalog on 2026-09-13, and the benchmark run of
that date was the first to cover them. Their figures are in the tables above
alongside everything else; this section is what those figures mean, plus the
three things about these models that no measurement decides — a licence, a
language key mavor does not have, and a model family that is new to the
listing.

Batch rows, fastest first, on the same 20-second clip as the rest of the page:

| Model | What it is | Download | Peak RAM | Total | WER |
|---|---|---:|---:|---:|---:|
| `parakeet-tdt-0.6b-v2` | NVIDIA Parakeet TDT 0.6B **v2**, INT8, English | 460 MB | 1.56 GB | 5.44 s | 1.8% |
| `nemotron-streaming-multi-560ms` | NVIDIA Nemotron 3.5 ASR streaming 0.6B, 560 ms chunk, INT8, 35 languages | 453 MB | 967 MB | 7.67 s | 7.3% |
| `nemotron-streaming-en-560ms` | NVIDIA Nemotron Speech streaming 0.6B, 560 ms chunk, INT8, English | 442 MB | 964 MB | 7.85 s | **1.8%** |
| `cohere-transcribe` | Cohere Transcribe 03-2026, INT8, 14 languages | 1.58 GB | **3.38 GB** | 9.76 s | 1.8% |
| `nemotron-streaming-en-80ms` | The same English Nemotron weights at an 80 ms chunk | 442 MB | 954 MB | 30.83 s | 3.6% |
| `parakeet-unified-en-streaming-240ms` | The streaming export of `parakeet-unified-en`, 240 ms chunk | 478 MB | 1019 MB | 101.67 s | 3.6% |

Download sizes are the archive, as `mavor models list` prints them; a sherpa
archive expands to roughly twice that on disk. The four streaming entries carry
a second set of figures — time to first token above all — in
[the streaming table](#streaming-text-while-you-speak).

One row is the best news in the run and one is the worst.
`nemotron-streaming-en-560ms` is the first streaming model in this catalog that
is also accurate. `parakeet-unified-en-streaming-240ms`, at 101.67 s to
transcribe 20 s of audio, is the slowest row in the entire report.

### `parakeet-tdt-0.6b-v2`, beside the v3 that was already there

`parakeet-tdt-0.6b` is the **v3** export: 25 European languages, and the
multilingual recommendation at the top of this page. `parakeet-tdt-0.6b-v2` is
the previous generation of the same model, and it is English-only.

That sounds like a downgrade and is not one. On this fixture the pair are
indistinguishable on quality — both 1.8% WER, both 0.16 punctuation marks per
word, both a 1.00 capitals F1 — and v2 is the faster of the two, 5.44 s against
5.93 s, for 1.56 GB of peak memory against 1.54 GB. NVIDIA reports v2 ahead of
v3 on English generally; this run neither confirms nor contradicts that,
because a clip on which each model makes exactly one error cannot separate
them. That is the fixture running out of resolution rather than a real tie —
see the [caveats](#what-these-numbers-do-and-do-not-tell-you).

So: if you dictate only in English, v2 is the one of the pair worth a try; if
you switch languages mid-sentence, v3 is the only one that can follow you.

### The Nemotron models, and a new family in the listing

The catalog records a **model family** for every entry — the lineage a set of
weights comes from, which is what groups the names mavor offers you when you
mistype one. Three of the six add a family that was not there before,
`Nemotron`, alongside Whisper, NeMo, Moonshine, SenseVoice, Paraformer and
Zipformer; `cohere-transcribe` adds a `Cohere` family of one.

All three Nemotron entries decode incrementally, and what separates two of them
is **chunk size**: the block of audio the recognizer takes in before it will
emit anything. A short chunk puts words on screen sooner and gives the model
less audio after each word to reconsider it with; a long chunk does the reverse.
80 ms and 560 ms are the two ends the catalog carries, and the run measured what
that trade costs — with an answer that runs the opposite way from the theory:

| Model | First token | RTF (streaming) | WER | Capitals F1 |
|---|---:|---:|---:|---:|
| `nemotron-streaming-en-560ms` | 433 ms | 0.395 | 1.8% | 0.91 |
| `nemotron-streaming-en-80ms` | 1.80 s | 1.511 | 3.6% | 0.83 |

The 80 ms export is **slower to its first word than the 560 ms one**, by well
over a second, and slower than real time overall. Chunk size is not latency: a
chunk seven times shorter means seven times as many encoder invocations over
the same audio, and on this CPU each invocation costs more than the shorter
chunk saves. It is the less accurate of the two as well.

- `nemotron-streaming-en-560ms` is the one to reach for as your actual `model`.
  As the [preview companion](#you-do-not-have-to-choose-the-preview-companion)
  it is not the default: it held that slot for a few hours on 2026-09-13 and
  lost it on update cadence, which is what a companion is judged on.
- `nemotron-streaming-en-80ms` earns its catalog row as the measured
  counter-example rather than as a recommendation. On this machine there is no
  job it does better than the 560 ms export; a faster machine, where the extra
  encoder invocations are cheaper, could read it differently.
- `nemotron-streaming-multi-560ms` is the multilingual Nemotron 3.5 export,
  35 languages, and the only streaming model in the catalog that is not
  English-only. It pays for that in accuracy: 7.3% WER against the English
  export's 1.8%, on English audio.

> [!IMPORTANT]
> **The Nemotron weights are not Apache-2.0.** NVIDIA licenses them under
> OpenMDW-1.1, a model-weights licence rather than a software licence, where
> the rest of the catalog is Apache-2.0. Nothing about running them locally
> changes; if you redistribute the weights or ship them inside a product, read
> that licence rather than assuming the catalog's usual terms.

`nemotron-streaming-multi-560ms` also has a limitation mavor cannot currently
work around. Upstream exposes a `prompt_index` input on the encoder, which is
how you tell the model which language a stream is in. mavor has no
configuration key that reaches it, so the model runs in its own auto-detect
mode and there is no way to pin it to a language. The
[roadmap](./roadmap.md#-8-no-language-selection-for-the-models-that-could-use-one)
carries that as work to do.

### `parakeet-unified-en-streaming-240ms`

The same model as the catalog's `parakeet-unified-en`, exported for incremental
decoding instead of one-shot — and the measurement settles the choice between
them rather than leaving it to taste. The streaming export runs at **RTF
5.058**, five times slower than the speech it is transcribing: 101.16 s of work
for 20 s of audio, with the first word appearing 5.27 s in. The non-streaming
export finishes the same clip in 5.54 s, and scores 1.8% WER against the
streaming one's 3.6%.

Nothing about dictation survives those numbers. Treat the 240 ms entry as the
tier being present for completeness, not as a live-preview option.

### `cohere-transcribe` takes no vocabulary biasing

Fourteen languages in a 1.58 GB download — and **3.38 GB resident, the largest
peak of any sherpa model in the catalog, second across the whole catalog only
to `whisper-large-v3`**. What that buys is 1.8% WER with full punctuation and
capitalisation, which `canary-180m` also scores in 460 MB and
`parakeet-tdt-0.6b` in 1.54 GB. It was the entry most likely to take the
multilingual slot from `parakeet-tdt-0.6b` once the two were measured against
each other; measured, it is slower (9.76 s against 5.93 s), more than twice as
heavy, and no more accurate.

Thirteen of those fourteen are out of reach today, though, and that is mavor's
doing rather than the model's: sherpa-onnx refuses to load Cohere Transcribe
without being told which language to expect, and mavor has no configuration
key that sets one, so it builds the recognizer with English. The same constant
pins `canary-180m` and `canary-1b`, which the catalog advertises as
multilingual. It is the same gap the multilingual Nemotron runs into from the
other direction, and it is
[on the roadmap](./roadmap.md#-8-no-language-selection-for-the-models-that-could-use-one).

One property is decided by its architecture rather than by a measurement: it
is an **attention encoder-decoder**, meaning the decoder attends over the whole
encoded utterance to produce text — the same shape as Whisper, and not the
frame-by-frame transducer decoding that sherpa-onnx implements hotword biasing
inside. So the [`[vocabulary]`](./user-guide.md#74-vocabulary--words-the-model-gets-wrong)
table reaches nothing on this model, exactly as it reaches nothing on the CTC,
paraformer, moonshine and sensevoice entries, and `mavor doctor` says so rather
than failing.

### The chunk sizes the catalog does not carry

Upstream publishes more of these than mavor lists. The English Nemotron export
comes at 80, 160, 560 and 1120 ms; the multilingual Nemotron 3.5 export adds a
320 ms tier to that set; the Parakeet Unified streaming export comes at 240,
560 and 1120 ms. That is twelve exports, and the catalog carries four of them.

This is deliberate. The catalog is a curated shortlist — every row is a row
each user reads past — not a mirror of the upstream release. The 80 ms result
above is also a reason to be sparing with the short tiers in particular: on
this CPU the shortest chunk was the slowest to its first word. A tier the
catalog skips is still perfectly usable: download and unpack the archive into
`paths.models/sherpa/<name>/` and set `model` to that directory name, which is
[§8.4 of the user guide](./user-guide.md#84-installing-a-custom-sherpa-model).
Two things you give up by doing that are named there, and one of them matters
here: a hand-installed model is never assumed to stream, because the catalog
entry is what records that, so `preview.source = "auto"` will not read partials
from it.

## What these numbers do and do not tell you

Stated plainly, because a table invites more confidence than one test clip
earns.

**Trust these.** Speed, real-time factor and peak memory are stable across
runs and differ by large multiples between models. So does formatting:
punctuation and capitalisation are either produced or not, and the gap
between `whisper-base.en` and `whisper-large-v3` is total rather than
marginal.

**Do not over-read these.** The accuracy column comes from **one 20-second
English clip** ([`real_speech.wav`](../test/fixtures/real_speech.wav)), read
clearly by one speaker. On that clip most models score 1.8% word error rate,
which is *a single word* — `path` heard as `patch`. Treating 1.8% as
meaningfully worse than 0.0% is reading one word as a trend. The large
differences (`fastconformer-streaming` at 12.7%, `paraformer` at 16.4%) are
real; the small ones are not.

Nothing here measures accented speech, background noise, technical
vocabulary, long dictation, or any language other than English. If your
dictation looks different from that clip — and it probably does — the ranking
could differ.

**Your hardware is not this hardware.** Every figure came from one machine,
named in the report's header. Rerun `just bench` on yours; it writes the same
tables with your numbers.

## Rerunning this yourself

```console
$ just bench-models     # download the catalog (~16 GB)
$ just bench-gpu-build  # build the Vulkan whisper.cpp, for the GPU column
$ just bench            # every model, on whisper.cpp and in-process sherpa-onnx
```

It writes [`model-benchmarks.md`](./reports/model-benchmarks.md) and the raw
results beside it. A model absent from your cache is reported as absent
rather than silently skipped, and a backend that cannot run says why. Every
number on this page comes from the run of 2026-09-13, which covers all 31
catalog entries and prints them under their current names.
