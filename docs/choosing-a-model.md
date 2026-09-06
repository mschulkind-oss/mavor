---
title: "Choosing a Model"
author: "Matthew Schulkind"
date: 2026-09-05
status: accepted
tags: [models, whisper, sherpa, gpu, accuracy, latency, guide]
summary: "Which of mavor's 24 models to actually use, decided from measurements rather than reputation — including why the largest Whisper models are the wrong choice for dictation."
vantage:
  status-chip: true
---

# Choosing a Model

`mavor` ships a catalog of 24 models. This page says which one to use and
why, from measurements rather than reputation. The measurements themselves,
with the machine they came from and the method, are in
[`model-benchmarks.md`](./reports/model-benchmarks.md); rerun them on your own
hardware with `just bench`.

> [!IMPORTANT]
> Read the [caveats](#what-these-numbers-do-and-do-not-tell-you) before
> treating a small accuracy difference as real. The speed, memory and
> **formatting** results are solid; most of the word-error differences are
> one word on one clip.

## The short answer

| If you want… | Use | Why |
|---|---|---|
| **The default** | `base.en` | Best accuracy measured, 1.6 s for 20 s of speech, 302 MB. Nothing beat it. |
| **The lightest thing that works** | `tiny.en` | 1.0 s, 196 MB, and still fully punctuated and capitalised. |
| **Languages other than English** | `parakeet-tdt-0.6b` | 25 languages, clean formatting. Costs 1.6 GB of RAM. |
| **A non-English model that stays small** | `canary-180m` | English, Spanish, German, French in 457 MB, formatting as good as `base.en`. |
| **Words appearing while you speak** | `zipformer-streaming` | First text 114 ms after you start talking. Accuracy is markedly worse. |
| **Maximum accuracy** | `base.en`, still | See below — the large models do not deliver this. |

Set it in `~/.config/mavor/config.toml`:

```toml
model = "base.en"

# Or a sherpa model — the in-process engines are always linked in:
# engine = "sherpa"
# sherpa_model = "canary-180m"
```

## Do not reach for the biggest model

This is the result most likely to contradict what you expected.

`large-v3`, `large-v3-turbo`, `distil-large-v3` and `medium.en` all return
**lowercase, unpunctuated text**:

| Model | Output on the test clip | Punct/word | Capitals F1 |
|---|---|---:|---:|
| `base.en` | `Lux is in the pit. He cannot sit still…` | 0.16 | **1.00** |
| `medium.en` | `Lux is in the pit he cannot sit still…` | 0.00 | 0.57 |
| `large-v3` | `lux is in the pit he cannot sit still…` | 0.00 | **0.00** |

Word error rate is the same across all of them. On a metric that only counted
recognised words, these models would look identical — and for dictation they
are not remotely identical, because one produces text you can paste into a
document and the other produces text you have to re-punctuate by hand.

`large-v3` is also **20× slower** than `base.en` on CPU (33.6 s against 1.6 s
for the same 20 seconds of audio) and wants 3.9 GB of RAM.

So: the largest Whisper models cost more, take longer, and produce worse
text for this purpose. Unless you have a specific reason, skip them.

## Speed and memory

Every model that produced usable output, fastest first. `RTF` is the fraction
of real time consumed — below 1.0 is faster than speech.

| Model | Engine | Time (20 s audio) | RTF | Peak RAM |
|---|---|---:|---:|---:|
| `tiny.en` | whisper | 1.05 s | 0.05 | 196 MB |
| `base.en` | whisper | 1.63 s | 0.08 | 302 MB |
| `moonshine-base` | sherpa | 1.98 s | 0.10 | 538 MB |
| `sensevoice-small` | sherpa | 3.88 s | 0.19 | 1.46 GB |
| `canary-180m` | sherpa | 4.40 s | 0.22 | 457 MB |
| `small.en` | whisper | 5.10 s | 0.26 | 768 MB |
| `parakeet-tdt-0.6b` | sherpa | 5.82 s | 0.29 | 1.56 GB |
| `canary-1b` | sherpa | 14.77 s | 0.74 | 2.32 GB |
| `medium.en` | whisper | 19.08 s | 0.95 | 2.02 GB |
| `large-v3` | whisper | 33.55 s | 1.68 | 3.81 GB |

`medium.en` at RTF 0.95 means a 20-second dictation takes 19 seconds to
transcribe — you would be waiting. `large-v3` at 1.68 is **slower than
speaking**. Neither is usable on CPU without a GPU behind it.

## GPU makes the large models possible

The packaged `whisper-cpp` on most distributions is CPU-only. Build a
Vulkan-enabled one with `just bench-gpu-build` and the picture changes,
measured with the same binary and only whisper.cpp's `-ng` flag differing:

| Model | CPU | GPU | Speed-up | RAM (CPU → GPU) |
|---|---:|---:|---:|---|
| `tiny.en` | 0.91 s | 0.40 s | 2.2× | 221 MB → 107 MB |
| `base.en` | 1.87 s | 0.55 s | 3.4× | 327 MB → 118 MB |
| `small.en` | 5.37 s | 0.82 s | 6.5× | 785 MB → 147 MB |
| `medium.en` | 20.20 s | 1.58 s | **12.8×** | 2.07 GB → 174 MB |
| `large-v3` | 41.16 s | 2.91 s | **14.2×** | 3.91 GB → 206 MB |

Two things worth noticing. The speed-up grows with model size, so the GPU
matters least for the model you were probably going to use anyway. And host
memory *falls* — the weights live on the card instead — so `large-v3` on a
GPU is lighter on RAM than `base.en` on CPU.

This does not rescue the large models' formatting problem. It only makes them
fast enough to be worth arguing about.

> [!NOTE]
> Sherpa models get no GPU column at all. The ONNX Runtime vendored by the Go
> binding carries no execution providers, so `sherpa_provider = "cuda"` falls
> back to CPU without saying so. This is a packaging limitation, not a setting
> you can fix.

## Streaming: text while you speak

Two models decode incrementally rather than waiting for you to stop:

| Model | First token | Total | Accuracy (WER) |
|---|---:|---:|---:|
| `zipformer-streaming` | **114 ms** | 4.33 s | 9.1% |
| `parakeet` | 405 ms | 9.28 s | 12.7% |

`zipformer-streaming` genuinely feels live. Both are considerably less
accurate than any of the batch models above — 9.1% against 1.8% is not a
rounding difference, it is roughly five times the errors.

Streaming is worth it when watching words appear matters more than getting
them right first time. For ordinary dictation, where you release a key and
want correct text, a batch model is the better trade.

`parakeet` is slower streaming than batch and less accurate than
`zipformer-streaming`, so despite its reputation there is currently no
configuration in which it is the right pick.

## Sherpa models

These run in-process rather than shelling out, and are linked into every
build.

| Model | Languages | Time | RAM | Formatting | Notes |
|---|---|---:|---:|---|---|
| `canary-180m` | en, es, de, fr | 4.40 s | 457 MB | **Excellent** | Best formatting of any sherpa model |
| `parakeet-tdt-0.6b` | 25 languages | 5.82 s | 1.56 GB | Excellent | The multilingual choice |
| `sensevoice-small` | zh, en, ja, ko, yue | 3.88 s | 1.46 GB | Good | Chinese and Japanese |
| `canary-1b` | 25 languages | 14.77 s | 2.32 GB | Excellent | Slow for what it adds over `canary-180m` |
| `moonshine-base` | en | 1.98 s | 538 MB | None | Fast, but no punctuation or capitals |
| `zipformer-ctc` | en | 1.59 s | 477 MB | None | Fast, 3.6% WER, no formatting |

`canary-180m` is the one to know about: it is the only sherpa model that
formats its output as well as `base.en` does, and it does so in 457 MB while
covering four languages.

The remaining catalogued sherpa models — `parakeet`, `parakeet-ctc`,
`parakeet-unified-en`, `paraformer`, `zipformer-offline`,
`zipformer-streaming`, `moonshine-tiny` — are measured in
[`model-benchmarks.md`](./reports/model-benchmarks.md) but are not better than
something above at any job.

## What these numbers do and do not tell you

Stated plainly, because a table invites more confidence than one test clip
earns.

**Trust these.** Speed, real-time factor and peak memory are stable across
runs and differ by large multiples between models. So does formatting:
punctuation and capitalisation are either produced or not, and the gap
between `base.en` and `large-v3` is total rather than marginal.

**Do not over-read these.** The accuracy column comes from **one 20-second
English clip** ([`real_speech.wav`](../test/fixtures/real_speech.wav)), read
clearly by one speaker. On that clip most models score 1.8% word error rate,
which is *a single word* — `path` heard as `patch`. Treating 1.8% as
meaningfully worse than 0.0% is reading one word as a trend. The large
differences (`parakeet` at 12.7%, `paraformer` at 16.4%) are real; the small
ones are not.

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
$ just bench            # every backend: whisper and the sherpa engines
```

It writes [`model-benchmarks.md`](./reports/model-benchmarks.md) and the raw
results beside it. A model absent from your cache is reported as absent
rather than silently skipped, and a backend that cannot run says why.
