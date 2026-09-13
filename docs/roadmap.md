---
title: "Ongoing Work: mavor Roadmap"
author: "Matthew Schulkind"
date: 2026-09-13
status: in-review
tags: [roadmap, benchmarks, doctor, models, gpu, sherpa, whisper, release]
summary: "Living roadmap for the mavor dictation daemon: open decisions, the ready-to-build queue, and active workstreams across benchmarking, diagnostics, and the first public release."
---

# Ongoing Work: `mavor` Voice-to-Text Utility

**Status:** 2 Needs Attention (💬), 7 Ready to Implement (📦), 6 Open Threads (🏗️ 1, 🔒 1, 🛑 1, 🧊 3)

---

## 1. Attention Required (💬)

### 💬 Is GPU acceleration worth pursuing? Now with numbers

Measured, not inferred ([`model-benchmarks.md`](reports/model-benchmarks.md)).
On an RX 9060 XT via Vulkan, comparing **the same binary with and without
`-ng`**, so the only difference is the backend:

| Model | CPU | GPU | Speed-up |
|---|---:|---:|---:|
| `whisper-tiny.en` | 884 ms | 412 ms | 2.1× |
| `whisper-base.en` | 1.49 s | 509 ms | 2.9× |
| `whisper-small.en` | 3.95 s | 757 ms | 5.2× |
| `whisper-medium.en` | 12.07 s | 1.55 s | 7.8× |
| `whisper-large-v3` | 23.10 s | 2.49 s | 9.3× |

GPU also *lowers* host memory — 175 MB against 2.07 GB for `whisper-medium.en`
— because the weights live on the card instead.

What this changes: the earlier framing was "`whisper-base.en` is already fast
enough on CPU, so who cares." That holds for `whisper-base.en`. It does not
hold for the accurate models. `whisper-medium.en` runs at 1.7× real time on
CPU — a 20-second dictation takes 12 seconds to transcribe, which is unusable
— and 12.9× real time on GPU, which is comfortable. **GPU is what makes the
large models viable at all**, so the question is really whether mavor wants to
offer them.

The cost is unchanged and it is packaging, not code: distro whisper.cpp
builds are CPU-only, so this means shipping or documenting a Vulkan build
(`just bench-gpu-build` does it in about ten minutes). sherpa-onnx stays
CPU-only regardless — see the workstream below.

**Still deferring to you**, but the trade is now priced.

### 💬 arm64 releases: a native runner, or a cross toolchain?

Opened by the cgo ruling below. mavor links sherpa-onnx through cgo, and cgo
cannot cross-compile to arm64 from the amd64 GitHub runner:

```console
$ GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go build ./cmd/mavor
# runtime/cgo
gcc_arm64.S:30: Error: no such instruction: `stp x29,x30,[sp,'
```

`.goreleaser.yaml` is therefore **amd64-only**, with a comment saying so rather
than leaving the gap to be rediscovered. An arm64 user builds from source, and
the PyPI wrapper says as much.

This is a packaging question, not a portability wall — the `sherpa-onnx-go-linux`
module vendors prebuilt shared objects for arm64 as well as amd64, so the arch
is reachable. What is missing is a decision about *where* to build it:

| Route | What it costs | Trade |
|---|---|---|
| **A native arm64 runner** | A second `goreleaser` build matrix entry pinned to an arm64 runner. GitHub's `ubuntu-24.04-arm` runners are free for public repositories. | Simplest, and the binary is built by the toolchain that will run it. Adds a second runner to every release. |
| **An `aarch64-linux-gnu` cross toolchain** | Install the cross gcc in `release.yml` and set `CC`/`CXX` per target. One runner, one job. | Cheaper per release, and one more thing that can silently produce a binary nobody has run. |

**Deferring to you:** which of the two, or whether amd64-only is fine until
somebody asks for arm64. Nothing else is blocked on it.

### ✅ Should whisper get vocabulary biasing? — RESOLVED (2026-09-05)

`--verbose` currently reports `vocabulary: none` for all 11 whisper models,
because mavor passes no initial prompt to `whisper-cli`. whisper.cpp supports
`--prompt` for exactly this, and the sherpa transducers already accept a
hotwords file.

There is a full design for the ambitious version —
[`active-window-context-and-vocabulary-prompting.md`](design/active-window-context-and-vocabulary-prompting.md),
still `in-review` — which derives vocabulary from the focused window via
`swaymsg -t get_tree`.

**Yes, the static half, and it is built** (`7e52f94`).
[`configuration-surface.md` §7](design/configuration-surface.md#7-vocabulary-and-decoding)
specified a runtime-neutral `[vocabulary]` table — `words`, `file`, `boost` —
and `internal/speech/vocabulary.go` is the one place it becomes something a
runtime understands: whisper's `--prompt` (truncated at a phrase boundary
against the 224-token cap, with a warning, because whisper.cpp clips silently),
a hotwords file plus `modified_beam_search` on the transducers, and nothing at
all on the CTC and encoder-decoder models — which `mavor doctor` now reports
rather than failing on. The user never picks a decoding method; beam search
follows from configuring a vocabulary on a model that can use one.

[`OQ-4`](design/configuration-surface.md#decision-ledger) settled that this
landed now rather than waiting: the window-context design derives the word list
at runtime and still needs a static one to sit beside, so it adopts this key
shape instead of replacing it.

The window-context half stays where it is, in
[`design/active-window-context-and-vocabulary-prompting.md`](design/active-window-context-and-vocabulary-prompting.md),
still `in-review` and unbuilt.

---

## 2. Up Next (📦)

Ordered by what unblocks other work first, then by cost.

### ✅ 1. All 24 models load (was: ten could not)

The catalog is 31 entries now. `zipformer-streaming-20m` joined it as the
preview companion (item 7), `fastconformer-streaming` took that slot from it
(`08ce3e3`), and six more arrived on 2026-09-13. The sweep of that date
(`4b1d2d4`) ran over all 31, so every catalog entry has speed, memory and
accuracy figures. [Item 1d](#-1d-nothing-compares-the-catalog-against-upstream)
and [item 1e](#-1e-the-preview-companion-default-is-now-a-decision-not-a-measurement)
below are what that leaves open — comparing the catalog against upstream, and a
companion default that is now a choice rather than a gap.

Fixed. The catalog-wide benchmark
([`model-benchmarks.md`](reports/model-benchmarks.md)) now reports **60
measured rows and no failures**, against 38 rows and 12 failed cells before.

The ten sherpa models that could not be loaded shared one root cause and
three narrower bugs, all in `internal/speech/sherpa.go`:

- **The detector asked the name before the files.** `parakeet-ctc` has a
  single `model.onnx` and no joiner, but its name contains "parakeet", so it
  was declared a transducer and failed looking for a joiner. Detection is
  layout-first now; the name decides only where the layout genuinely cannot,
  which is SenseVoice against NeMo CTC.
- **`findFile` matched exact names only.** sherpa ships zipformer models as
  `encoder-epoch-99-avg-1.onnx`, so a transducer looked like it had no
  encoder at all. It takes globs now.
- **mavor forced sherpa's `model_type`** with its own vocabulary, which made
  sherpa skip its own detection and use the wrong reader. Left empty, sherpa
  infers correctly from the populated sub-config.
- **Canary had no support**, fell through to paraformer, and failed on
  metadata paraformer expects and Canary does not carry.

Worth recording: **the catalog was right the whole time.** Every
`Transducer` and `Streaming` flag matches what the file layouts actually
contain — only the loader disagreed with it.

### ✅ 1a. Streaming works, and is measured

Also fixed, and it was a bigger hole than "a missing number":
`BuildSherpaOnlineConfig` and `newCGOOnlineRecognizer` both existed and
**nothing ever called them**. Every model went through the offline builder,
and loading a streaming transducer offline is not a soft failure — sherpa
rejects the encoder's input shapes and aborts the process.

First measurements, from a warm model:

| Model | First token | Streaming total | Batch total |
|---|---:|---:|---:|
| `zipformer-streaming` | 114 ms | 4.33 s | 4.65 s |
| `fastconformer-streaming` (then named `parakeet`) | 405 ms | 9.28 s | 8.12 s |

`zipformer-streaming` at 114 ms is comfortably inside what reads as live.
`fastconformer-streaming` was slower streaming than batch here, which was worth
a look if streaming became a product feature rather than a catalog claim. The
2026-09-13 sweep supersedes both rows and reverses that second finding — it
measures every streaming entry, and
[item 1e](#-1e-the-preview-companion-default-is-now-a-decision-not-a-measurement)
reads the result.

### 📦 1b. Feed the measured numbers back into the catalog

`MeasuredRTF` still carries figures for 3 models. The benchmark now measures
every loadable one, so the field can stop being a placeholder — and
`--verbose` can stop saying "relative tier, not measured" for models that
have been measured.

> [!NOTE]
> [`model-benchmarks.md`](reports/model-benchmarks.md) was regenerated on
> 2026-09-13 and prints current catalog names — `whisper-base.en`, not
> `base.en`. Item 1a's table above is the exception on this page: it records an
> older run, and its numbers are superseded by the 2026-09-13 sweep. `just
> bench` regenerates the report; do not hand-edit it.

### 📦 1c. The accurate whisper models return worse text than `base.en`

Unexpected, and it inverts the usual advice. From the same run, on the same
audio:

| Model | Transcript | Punct/word | Capitals F1 |
|---|---|---:|---:|
| `whisper-base.en` | `Lux is in the pit. He cannot sit still...` | 0.16 | 1.00 |
| `whisper-medium.en` | `Lux is in the pit he cannot sit still...` | 0.00 | 0.57 |
| `whisper-large-v3` | `lux is in the pit he cannot sit still...` | 0.00 | 0.00 |

`whisper-large-v3`, `whisper-large-v3-turbo` and `whisper-distil-large-v3` all
return **lowercase, unpunctuated** text. Word error rate is essentially identical across the
whole family — the fixture is easy — so a report that measured only WER
would call these models equivalent, and for dictation they are not: one
produces text you can paste into a document, the others produce text you
have to re-punctuate by hand.

This is why the benchmark scores punctuation and capitalization separately
from WER rather than normalizing them away.

**Next step:** find out whether this is fixable from mavor's side. **The
mechanism now exists** — `[vocabulary]` became whisper's `--prompt` in
`7e52f94`, so a prompt reaches the model on every placement — and what is
untested is whether *punctuated prose* in that prompt coaxes formatted output
out of `whisper-large-v3`. That is a benchmark run, not a code change: put a
punctuated sentence in `vocabulary.words`, rerun `just bench`, and compare the
punctuation and capitalization columns.

There is now also a way around it rather than through it. With every sherpa
model loading, `canary-180m` scores 1.8% WER with **punctuation 0.18 and
capitalisation 1.00** — the same formatting quality as `whisper-base.en` — in
460 MB and 3.73 s. The 2026-09-13 sweep found four more sherpa models that pair
that accuracy with a 1.00 capitals F1 (`parakeet-tdt-0.6b`,
`parakeet-tdt-0.6b-v2`, `cohere-transcribe`, `canary-1b`), so `canary-180m` is
no longer the only one — but the lightest of the others costs 1.54 GB, so it is
still the candidate for the accurate preset that `whisper-large-v3` cannot
fill.

### 📦 1d. Nothing compares the catalog against upstream

Six models were added on 2026-09-13, and the exports four of them come from had
been sitting upstream for months: sherpa-onnx published the Cohere Transcribe
export on 2026-04-01 and the Nemotron 3.5 streaming export on 2026-06-11, while
[`internal/models/catalog.go`](../internal/models/catalog.go) was last extended
on 2026-09-05 and picked up neither. The defect is not the missing rows. It is
that nobody could have known they were missing: the catalog is hand-maintained,
and no step in CI or in the `just` recipes ever asks upstream what it has.

**What would fix it:** a check that lists what the `asr-models` release of
`k2-fsa/sherpa-onnx` holds and the catalog does not. The data is one request
away — `gh api repos/k2-fsa/sherpa-onnx/releases/tags/asr-models` returns every
asset with its name and byte size — and every sherpa entry already records the
asset URL it downloads, so this is a set subtraction on filenames rather than a
scrape.

Three things it has to get right to be worth running:

- **It is a development check, never a daemon one.** The only outbound request
  in the program is `mavor models pull`, and that property is worth more than
  this check is. So: a `just` recipe, or a test that skips when the network is
  absent, in the shape the live OSC test already uses.
- **It has to be quiet.** That release carries roughly 500 assets against the
  catalog's twenty sherpa entries, so a raw diff is 480 rows nobody reads.
  Filter to assets published since the catalog was last extended, and keep a
  checked-in ignore list for what was deliberately passed over — the fp16
  variants, the single-language exports, and the streaming chunk tiers
  [`choosing-a-model.md`](choosing-a-model.md#the-chunk-sizes-the-catalog-does-not-carry)
  names as an intentional omission.
- **It covers half the catalog.** Whisper GGML models come from a Hugging Face
  repository, not from that release. Leaving the eleven whisper entries out is
  defensible — they change about as often as Whisper does — but the check
  should say it is a sherpa check rather than implying coverage it does not
  have.

### 📦 1e. The preview companion default is now a decision, not a measurement

[`speech.DefaultCompanionModel`](../internal/speech/companion.go) is
`fastconformer-streaming`, and the doc comment on that constant records the
measurement that put it there. Against `zipformer-streaming-20m`, same fixture,
same 30 ms chunks, same decode loop: first output at 1.53 s against 1.68 s and,
the part that actually decided it, the FastConformer got the opening words and
returned them in lower case where the zipformer lost them and shouted.

That comparison had two candidates because the catalog had two candidates. It
now has seven streaming entries, and the 2026-09-13 sweep
([`model-benchmarks.md`](reports/model-benchmarks.md)) measured every one of
them. **The incumbent came last on accuracy, by a wide margin.**

| Model | First token | WER | Punct/word | Capitals F1 | Peak RSS |
|---|---:|---:|---:|---:|---:|
| `zipformer-streaming` | 107 ms | 7.3% | 0.04 | 0.20 | 161 MB |
| `zipformer-streaming-20m` | 108 ms | 9.1% | 0.02 | 0.15 | 112 MB |
| `fastconformer-streaming` (the default) | 382 ms | **12.7%** | 0.00 | 0.00 | 550 MB |
| `nemotron-streaming-multi-560ms` | 414 ms | 7.3% | 0.09 | 0.80 | 976 MB |
| `nemotron-streaming-en-560ms` | 433 ms | **1.8%** | 0.15 | 0.91 | 966 MB |
| `nemotron-streaming-en-80ms` | 1.80 s | 3.6% | 0.15 | 0.83 | 958 MB |
| `parakeet-unified-en-streaming-240ms` | 5.27 s | 3.6% | 0.16 | 0.91 | 1024 MB |

`nemotron-streaming-en-560ms` makes **seven times fewer word errors** than the
default for **51 ms** more to first token and about 420 MB more resident, and it
punctuates and capitalises where the default does neither at all — which is the
same criterion that settled the slot last time, since a preview that disagrees
with the text that lands reads as a bug. The two entries this item named as
untried are answered too, and negatively: `nemotron-streaming-en-80ms` needs
1.80 s to first token and `parakeet-unified-en-streaming-240ms` 5.27 s, so
neither is a companion at any price.

One companion criterion the sweep still does not cover: **whether the opening
words survive the first chunks.** That is what took the slot from
`zipformer-streaming-20m` in the first place, and a whole-clip word error rate
cannot see it. Checking it is a look at the partial stream, not another
benchmark run.

**Next step, and it is the user's call rather than an implementation task:**
decide whether `speech.DefaultCompanionModel` becomes
`nemotron-streaming-en-560ms`. The download is 442 MB against 429 MB, so
`mavor setup` costs about the same; the price is roughly 420 MB more resident in
the daemon for the whole session, and a shipped default whose weights are under
NVIDIA's OpenMDW-1.1 model-weights licence rather than Apache-2.0 like the rest
of the catalog
([what that changes](choosing-a-model.md#the-nemotron-models-and-a-new-family-in-the-listing)). Leaving the default where it is remains a
legitimate answer — the finding is recorded either way, in
[`choosing-a-model.md`](choosing-a-model.md#you-do-not-have-to-choose-the-preview-companion),
and anyone can set `preview.source` to a model name today without waiting for
the default to move.

**Separately, the docs disagree with the code.** `08ce3e3`
changed the default companion and updated none of the prose;
[`choosing-a-model.md`](choosing-a-model.md), [`user-guide.md`](user-guide.md)
and [`../README.md`](../README.md) were corrected on 2026-09-13, and
[`quickstart.md`](quickstart.md),
[`reference/how-mavor-works.md`](reference/how-mavor-works.md) and
[`planning/dictation-workflows.md`](planning/dictation-workflows.md) still name
`zipformer-streaming-20m` as what `preview.source = "auto"` loads.

### 📦 2. `mavor doctor` — the checks it still does not do

The GPU check landed, and the config rewrite added five more: runtime and
placement with the reason, the thread count and where it came from, an
all-unknown-keys config reported as the stale schema it is, where the preview
text will come from, and whether the `[vocabulary]` table can reach this model
at all. **Config coherence is therefore closed** — the two examples this item
used to name (`sherpa_hotwords_file` on a CTC model, `engine = "sherpa"` with a
whisper model) are keys that no longer exist, and the coherence errors that
replaced them are refused at daemon start rather than merely reported.

These are the remaining silent-failure modes:

- **Model integrity.** `checkModel` resolves the path and stats it. A truncated
  or partial download passes and then fails at transcription time. Verify the
  size against the catalog's `DownloadSize` — the field already exists and is
  exact.
- **whisper-cli capability.** Report the whisper.cpp version and which
  `load_backend:` lines it emits; `cmd/mavor/gpu.go` already parses them.
- **Disk headroom.** `models pull whisper-large-v3` wants 2.9 GB and fails
  partway with a confusing error when the cache filesystem is full.
- **Machine-readable output.** `--json` so the checks can run in CI and in the
  integration harness rather than only being read by a human.

### ✅ 3. `engine = "server"` starts and transcribes — RESOLVED (2026-09-05)

Found by the warm-server benchmark, which was the first thing in the project
ever to drive this engine end to end. Two independent breaks, both fixed in
`dbe6592` and `fd20208`:

- **A Unix socket could not work at all.** `DefaultServerCommand` passed
  `--socket <path>` — the shape `mavor config init` writes — to a binary that
  binds a host and a port and has no such flag. The child printed its usage and
  exited. The supervisor now reads a filesystem path as *intent* rather than
  transport: it takes a free loopback port, starts the child there, logs where
  it went, and `Supervisor.Endpoint` tells the client. An `http://` endpoint is
  still used exactly as written.
- **Over HTTP the request 404'd.** The client posted to
  `/v1/audio/transcriptions`; whisper.cpp serves `/inference`. It now tries both
  and remembers which answered — once per daemon, not once per dictation.

Neither could have been caught by the tests that existed: the fake server
accepted any flag and answered on any path, because it was written from the
same assumption as the code. It now behaves like the real binary, and an
`e2e`-tagged test in [`internal/speech`](../internal/speech/server_e2e_test.go)
runs a real `whisper-server` through the scaffolded config.

**Left undone:** nothing measures this path in CI. The `doctor` half is closed
— `checkRuntime` prints the runtime, the placement and the reason, and
`speech.AdjustForEnvironment` downgrades a derived `local-server` placement to
`subprocess` with a warning when no whisper server is on `$PATH`, so a missing
server costs a warm model instead of a failed dictation. But a whisper server is
still only ever exercised by hand or by the `e2e`-tagged test.

### 📦 5. `formatFileSize` labels MiB as "MB"

Still true after the config rewrite.
[`cmd/mavor/models_cmd.go`](../cmd/mavor/models_cmd.go) divides by 1024² and prints
"MB", so the catalog shows `74.1 MB` where Hugging Face shows `77.7 MB` for the
same file. Harmless in isolation, confusing when a user compares the two.

Pick one convention and apply it everywhere — the same formatter renders
installed sizes and doctor output, so this is a small change with a wide blast
radius.

### ✅ 6. The design docs that have shipped are retired

`how-mavor-works.md` was `status: accepted` and described a system built two
weeks and 18,000 lines of Go earlier — no push-to-talk, no VAD, no history, no
engine selection, and a stuck-in-Recording bug that has since been fixed. It is
now [`how-mavor-works.md`](reference/how-mavor-works.md) in the reference tree,
reconciled against `c2a3a48`, and the design doc is deleted.

Its companion, `local-engine-benchmarks-and-architecture.md`, is already gone —
not retired but withdrawn. See the note on fabricated reports below.

What is left in `docs/design/`: `active-window-context-and-vocabulary-prompting.md`,
which is `in-review` and unbuilt, and `next-gen-runtimes-executorch-iree.md`,
which is frozen. Both are proposals, which is what that tree is for.

Two more are there and should not be: `configuration-surface.md` and
`configuration-surface-plan.md` shipped on 2026-09-05 and are now describing a
system rather than proposing one. Item 7 below carries the graduation.

### ✅ 7. The config file had 29 keys and three of them were wrong — RESOLVED (2026-09-05)

Built across six commits, `adb0760`..`7e52f94`, in the order
[`configuration-surface.md` [§1](#1-attention-required-)4](design/configuration-surface.md#14-what-i-would-build-in-order)
laid out. The three broken keys are gone: `gpu_layers` passed `-ngl` to
whisper.cpp, which does not accept it, so any non-zero value broke every
transcription and `doctor` recommended setting it; `device` was written into a
struct field nothing read; and `mavor config init` scaffolded a file that
disagreed with the compiled defaults on `mode` and `duck_audio`.

What landed:

- **No `engine` key, and nothing reads one.** The two axes it welded together
  are derived separately now: the model name decides the **runtime**
  (whisper.cpp, or ONNX Runtime through sherpa-onnx) because the catalog
  records it, and the runtime plus `[advanced]` decides the **placement**
  (`in-process`, `local-server`, `subprocess`, `remote`). `internal/models` is
  a new package holding both, extracted from `cmd/mavor` so `internal/speech`
  could import it.
- **20 keys in six tables** — `model` at the top, then `[preview]`,
  `[ducking]`, `[vocabulary]`, `[overlay]`, `[advanced]`, `[paths]`.
  `config.Default()` is the single source of the defaults and the scaffold is
  generated from it, with a test asserting the two parse to the same value, so
  the drift that produced the third bug cannot recur.
- **Every catalog name begins with its model family.** `whisper-base.en`, not
  `base.en`; the bare aliases were deleted rather than kept resolving, and a
  stale name errors with the nearest entries named. On-disk filenames stay
  upstream's, and `speech.WhisperModelPath` is the only place the two
  vocabularies meet.
- **A companion model drives the preview.** A 20M streaming zipformer runs
  alongside a main model that cannot decode incrementally, replacing the
  re-transcribe-at-every-pause behaviour — which survives as the named
  fallback, "phrase mode". The preview still never emits.
- **The build is cgo, always.** The pure-Go build and the `sherpa` tag were
  deleted rather than demoted; `build-sherpa` and `bench-sherpa` folded into
  `build` and `bench`; `scripts/sherpa-libs.sh` stages the two shared objects
  and the `$ORIGIN` rpath makes the artifact relocatable. That buys the
  thirteen sherpa models out of the box and costs cross-compilation — see the
  arm64 question above, which is the one loose end.
- **A stale config is detected, not silently ignored.** There are no
  compatibility aliases, so a pre-rewrite file parses to zero known keys;
  `config.File.SchemaLooksStale` catches exactly that and `doctor` names
  `mavor config init --force`.

**Next step: retire the design docs.** The feature has shipped, so
[`design/configuration-surface.md`](design/configuration-surface.md) and
[`design/configuration-surface-plan.md`](design/configuration-surface-plan.md)
should graduate into the reference tree — the runtime/placement model, the
preview resolution rule and the vocabulary mapping belong in
[`how-mavor-works.md`](reference/how-mavor-works.md), which already carries the
first pass of all three — and then be deleted, the same lifecycle item 6
describes. Until that happens the design doc is the only statement of the
`[advanced]` semantics, so do not delete it early.

### 📦 8. No language selection, for the models that could use one

`nemotron-streaming-multi-560ms` is multilingual and mavor cannot tell it which
language you are speaking. Upstream exposes a `prompt_index` input on that
model's encoder for exactly this, chosen per stream; nothing in mavor's
configuration reaches it, so the model runs in its own auto-detect mode and
cannot be pinned.

That model is where the gap becomes visible rather than where it starts.
**mavor has no language key at all**, and every place a sherpa recognizer needs
a language today gets a literal in
[`internal/speech/sherpa.go`](../internal/speech/sherpa.go): Cohere Transcribe
is built with `"en"` (sherpa-onnx refuses to load it without one, and it was
trained on fourteen languages), Canary with an `"en"` source and target
language (the catalog advertises `canary-180m` as en/es/de/fr and `canary-1b`
as 25 languages), and SenseVoice with `"auto"`. Three of the catalog's
multilingual models are therefore pinned to English by a constant, and a fourth
guesses — which makes this a correctness problem for models mavor already
recommends, not only a missing feature on a model just added.

**What it costs:** one key, and a decision about where it lives. A top-level
`language` beside `model` reads best, because the language is a property of the
person speaking rather than of the runtime — but the key has to degrade
honestly, since most of the catalog takes no such setting and a key that
silently does nothing on `whisper-base.en` is worse than no key. `mavor doctor`
is the precedent for saying so: it already reports whether the `[vocabulary]`
table can reach the configured model, and where the language went belongs on
the same page.

**Open first:** whether the Go binding surfaces `prompt_index` at all. The
Cohere and Canary halves are reachable today — they are struct fields with
constants in them — so those could land without waiting for the answer.

---

## 3. Withdrawn: the fabricated benchmark reports

Four documents claimed "Empirical" measurements that this project never took.
They are deleted rather than superseded, because a reader who finds them in
git history should find them labelled, not merely outdated:

- `docs/reports/local-engine-benchmarks.md` — reported sherpa Parakeet-TDT on
  **DirectML**, a Windows-only ONNX Runtime provider, on a Linux-only project,
  and Vulkan whisper figures from a `whisper-cli` that loads no GPU backend.
- `docs/reports/single_model_runtime_benchmark.md` — reported empirical
  ExecuTorch and IREE AOT results. Neither toolchain has ever been in the tree
  or the container.
- `docs/reports/real-audio-and-thread-scaling.md` — tagged `sherpa` and
  presented as covering it; no sherpa model had ever been downloaded.
- `docs/design/local-engine-benchmarks-and-architecture.md` — carried the same
  DirectML table, and described a "Silero VAD gate (0.52 ms)". mavor's VAD is
  energy-threshold RMS; there is no Silero model anywhere in the project.

`docs/reports/model_transcription_comparison.md` survived the first cull,
because it stayed inside what its python harness could actually measure. It is
now retired too, along with `scripts/benchmark-multi-models.py`,
`benchmark-real-audio.py` and `benchmark-engines.sh`: the two things it
measured that nothing else did — thread scaling, and warm `whisper-server`
against cold `whisper-cli` — are sections of the generated report as of
2026-09-05. It is superseded rather than withdrawn; its numbers were real.

Two of its tables would not have survived a rerun regardless. "VRAM Required
(GPU Offload)" was never measured by anything — the generated report says
outright that nothing in it measures VRAM — and the token-latency table divided
by "~68 tokens" estimated for every model alike.

Their replacement is [`model-benchmarks.md`](reports/model-benchmarks.md),
generated by `just bench` and regenerable on any machine.

---

## 4. Open Threads & Workstreams (🏗️, 🔒, 🛑, 🧊)

### 🛑 CI has never run

`.github/workflows/ci.yml` exists and `mise.toml` pins `just` and `staticcheck`,
so it should work — but it has never executed. Treat the workflow as unverified
until a run goes green.

**Next step:** watch the first Actions run. The workflow installs no system
packages, which is now a bet rather than a fact: since `f3f8fd9` the build is
cgo and needs a C toolchain, which `ubuntu-latest` happens to preinstall. So the
risks are the mise toolchain step and that preinstalled gcc, and a runner image
that drops it would break the build with no warning.

Same caveat for `.github/workflows/release.yml`, which additionally has never
built a release artifact.

### 🔒 Integration and e2e suites are unverified in CI

`just test-int` and `just test-e2e` need a headless Wayland compositor and a
PipeWire stack. Both compile and `test-int` now passes reliably — eight
consecutive green runs, from four failures in eight — but neither runs in CI,
so the screenshot assertions and the real-whisper path are only ever exercised
by hand.

`test-int` additionally cannot run under `-race`: the detector's `checkptr`
aborted inside a transitive cgo dependency of the GTK bindings. That dependency
is gone with the overlay rewrite, so this should be retried: the integration
path may now have race coverage available to it for the first time.

**Next step:** decide whether CI grows a Wayland service container, or whether
these stay local-only and CI covers unit tests alone.

### 🏗️ AMD GPU acceleration for the sherpa models — three routes, all uphill

whisper.cpp already gets 2.3× to 9.6× on the RX 9060 XT via Vulkan (table
above). The thirteen sherpa models get nothing, and the reason is not mavor's
code: the `sherpa-onnx-go-linux` module vendors a CPU-only ONNX Runtime with no
provider shared objects, and sherpa-onnx answers a provider it cannot honor by
logging `Fallback to cpu!` and continuing. Researched 2026-09-05; here is what
the ground actually looks like.

**The hardware is not the blocker.** RDNA4 code generation landed in MIGraphX
2.12, shipped with ROCm 6.4, and the RX 9060 XT became an officially supported
product in ROCm 7.0.2. Both are in the past.

**ONNX Runtime's AMD story changed underneath us.** The ROCm execution provider
was *removed* in ORT 1.23 — AMD's docs name ROCm 7.0 as the last supported
release and point users at MIGraphX instead. MIGraphX is the surviving path, it
builds on Linux with `--use_migraphx`, and AMD publishes prebuilt wheels at
`repo.radeon.com`, so the runtime half is largely pre-solved.

**sherpa-onnx is the blocker.** Its `Provider` enum is `cpu, cuda, coreml,
xnnpack, nnapi, trt, directml, spacemit` — no ROCm, no MIGraphX, no WebGPU.
The one open PR ([#2370](https://github.com/k2-fsa/sherpa-onnx/pull/2370)) adds
ROCm, was tested on a Hygon DCU rather than a Radeon, and targets the execution
provider ONNX Runtime just deleted. A WebGPU request
([#3665](https://github.com/k2-fsa/sherpa-onnx/issues/3665)) sits open with no
maintainer response.

The three routes, worst to best:

| Route | What it costs | Verdict |
|---|---|---|
| **MIGraphX EP** | Write the provider plumbing sherpa-onnx does not have (a few hundred lines of C++, modelled on the CUDA provider), build ORT against ROCm, vendor the result into a forked Go module, then maintain that version matrix forever. Multi-day to multi-week. | Plausible, unmerged, and yours to own indefinitely |
| **WebGPU EP** | ONNX Runtime's WebGPU provider runs natively on Linux through Dawn, which dispatches to Vulkan — vendor-neutral, no ROCm at all. But sherpa-onnx has no plumbing for it, op coverage is unpublished, and the only prototype anyone claims is macOS/Metal. | Most interesting long-term, least evidence |
| **Leave ONNX entirely** | [`parakeet.cpp`](https://github.com/mudler/parakeet.cpp) reimplements Parakeet in ggml, validated at WER-0 against NeMo, with published GGUF weights. ggml already has the Vulkan backend giving whisper its 7.8×, so a ggml Parakeet **inherits RDNA4 acceleration for free**. | The only route with a live working precedent on this hardware |

> [!WARNING]
> A trap that applies to both ONNX routes: PR #2370's own author reports that
> **int8 models fall back to CPU** and only fp32 runs on the GPU. Every sherpa
> model in mavor's catalog is int8 or has an int8 variant as the fast path, so
> the payoff could be zero for exactly the models this would be done for.
> Verify that before writing any C++.

> [!NOTE]
> Zipformer has no ggml port that I could find — the third route covers
> Parakeet and, with more work, Moonshine, but not Zipformer. The streaming
> Zipformer **is** the preview companion as of `7e52f94` (the 20M variant), so
> it stays on the CPU regardless of how this workstream lands. That is fine:
> the companion is small by design and costs about one core.

**Next step:** measure before building. Run the ggml Parakeet against the
catalog's ONNX Parakeet on this machine, CPU and Vulkan, through `just bench`.
If ggml on Vulkan beats ONNX on CPU by the margin whisper sees, route three
answers the question without a line of provider plumbing, and routes one and
two can stay closed.

### 🧊 AOT-compiled runtimes (ExecuTorch / IREE)

[`next-gen-runtimes-executorch-iree.md`](design/next-gen-runtimes-executorch-iree.md)
evaluates ahead-of-time compiled inference against the graph interpreters mavor
uses today.

Genuinely uncertain this is worth building. The premise is that compilation buys
latency, but `whisper-base.en` at 12.2× real time on CPU — 39.3× on a Vulkan
build — is already far past what dictation needs, and both runtimes would add a
heavyweight toolchain to the build.

**Next step:** revisit once item 1 produces catalog-wide numbers. If the fast
sherpa transducers turn out to close the gap on their own, this stays frozen.

### 🧊 GNOME, and the compositors without the wlroots protocols

[`porting-to-gnome.md`](design/porting-to-gnome.md) prices the port and comes
back with a split verdict: build a second `output.Dispatcher`, do not build a
GNOME HUD. The reframing is that **typing is broken on everything that is not
wlroots, not just on GNOME** — KWin implements `wlr-layer-shell` but not
`virtual-keyboard-v1`, so KDE gets the pill and never types a character. One
dispatcher buys both desktops; a HUD buys only GNOME and can only be a GNOME
Shell extension, which is a second painter in JavaScript on a six-month
breakage cadence. Both shipped GNOME dictation extensions gave up on injection
and paste from the clipboard instead.

The document also names a bug on the platform mavor already supports: `doctor`
checks `$WAYLAND_DISPLAY` and `$PATH`, never what the compositor implements, so
on GNOME every check is green and every dictation silently fails to type.

**Next step:** answer [`OQ-GN2`](design/porting-to-gnome.md#OQ-GN2) with a
one-day spike — a keyboard-only RemoteDesktop portal session with
`persist_mode=2`, restarted and screen-locked — because whether GNOME prompts
once or every session decides whether any of the rest is worth doing.

### 🧊 Porting to macOS — a bigger job than four interfaces

[`porting-to-macos.md`](design/porting-to-macos.md) tests
[`../AGENTS.md`](../AGENTS.md)'s claim that porting is "a matter of implementing
those, not of restructuring" and finds it half true. The seams are the easy
half: `paint.Render` is already portable, `internal/ipc` is already portable,
and exactly one file — [`internal/wayland/shm.go#L22`](../internal/wayland/shm.go#L22),
`memfd_create` — blocks a darwin build. The expensive half is everything with
no interface in front of it: eight Linux assumptions below the seams, AppKit's
claim on the process's main thread, and a keybind that has to move *inside*
mavor because macOS has no compositor config file.

Two findings reorder the plan. macOS keys permission to a code signature, and
on macOS 26.1 a path-identified (non-bundled) client requesting Accessibility
does not appear in System Settings at all — so the artifact has to become a
signed, notarized `.app`, at 99 USD a year forever. And the ONNX Runtime
vendored for `darwin/arm64` in the pinned `sherpa-onnx-go-macos` declares a
minimum of **macOS 26.5**, which would make mavor's default engine refuse to
launch on nearly every Mac in service.

**Next step:** answer [`OQ-MAC2`](design/porting-to-macos.md#OQ-MAC2) — whether
anyone will own an Apple Developer Program membership — because a "no" there is
a "no" to the whole port rather than a smaller version of it.
