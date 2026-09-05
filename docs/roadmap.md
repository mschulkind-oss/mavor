---
title: "Ongoing Work: mavor Roadmap"
author: "Matthew Schulkind"
date: 2026-09-03
status: in-review
tags: [roadmap, benchmarks, doctor, models, gpu, sherpa, whisper, release]
summary: "Living roadmap for the mavor dictation daemon: open decisions, the ready-to-build queue, and active workstreams across benchmarking, diagnostics, and the first public release."
---

# Ongoing Work: `mavor` Voice-to-Text Utility

**Status:** 2 Needs Attention (💬), 4 Ready to Implement (📦), 4 Open Threads (🔒 2, 🛑 1, 🧊 1)

---

## 1. Attention Required (💬)

### 💬 Is GPU acceleration worth pursuing? Now with numbers

Measured, not inferred ([`model-benchmarks.md`](reports/model-benchmarks.md)).
On an RX 9060 XT via Vulkan, comparing **the same binary with and without
`-ng`**, so the only difference is the backend:

| Model | CPU | GPU | Speed-up |
|---|---:|---:|---:|
| `tiny.en` | 920 ms | 408 ms | 2.3× |
| `base.en` | 1.56 s | 508 ms | 3.1× |
| `small.en` | 4.35 s | 743 ms | 5.9× |
| `medium.en` | 15.12 s | 1.57 s | 9.6× |
| `large-v3` | 34.62 s | 5.01 s | 6.9× |

GPU also *lowers* host memory — 174 MB against 1.55 GB for `medium` — because
the weights live on the card instead.

What this changes: the earlier framing was "`base.en` is already fast enough
on CPU, so who cares." That holds for `base.en`. It does not hold for the
accurate models. `medium.en` is 1.3× real time on CPU — a 20-second dictation
takes 15 seconds to transcribe, which is unusable — and 12.7× real time on
GPU, which is comfortable. **GPU is what makes the large models viable at
all**, so the question is really whether mavor wants to offer them.

The cost is unchanged and it is packaging, not code: distro whisper.cpp
builds are CPU-only, so this means shipping or documenting a Vulkan build
(`just bench-gpu-build` does it in about ten minutes). sherpa-onnx stays
CPU-only regardless — see the workstream below.

**Still deferring to you**, but the trade is now priced.

### 💬 Should whisper get vocabulary biasing?

`--verbose` currently reports `vocabulary: none` for all 11 whisper models,
because mavor passes no initial prompt to `whisper-cli`. whisper.cpp supports
`--prompt` for exactly this, and the sherpa transducers already accept a
hotwords file.

There is a full design for the ambitious version —
[`active-window-context-and-vocabulary-prompting.md`](design/active-window-context-and-vocabulary-prompting.md),
still `in-review` — which derives vocabulary from the focused window via
`swaymsg -t get_tree`.

**My leaning:** ship the static half first. A `vocabulary = ["kubernetes", ...]`
config key wired to `--prompt` is a small change that closes the gap for every
whisper model. The window-context half is a much bigger build and can follow if
the static version proves useful.

---

## 2. Up Next (📦)

Ordered by what unblocks other work first, then by cost.

### ✅ 1. All 24 models load (was: ten could not)

Fixed. The catalog-wide benchmark
([`model-benchmarks.md`](reports/model-benchmarks.md)) now reports **48
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
| `parakeet` | 405 ms | 9.28 s | 8.12 s |

`zipformer-streaming` at 114 ms is comfortably inside what reads as live.
`parakeet` is slower streaming than batch, which is worth a look if
streaming becomes a product feature rather than a catalog claim.

### 📦 1b. Feed the measured numbers back into the catalog

`MeasuredRTF` still carries figures for 3 models. The benchmark now measures
every loadable one, so the field can stop being a placeholder — and
`--verbose` can stop saying "relative tier, not measured" for models that
have been measured.

### 📦 1c. The accurate whisper models return worse text than `base.en`

Unexpected, and it inverts the usual advice. From the same run, on the same
audio:

| Model | Transcript | Punct/word | Capitals F1 |
|---|---|---:|---:|
| `base.en` | `Lux is in the pit. He cannot sit still...` | 0.16 | 1.00 |
| `medium.en` | `Lux is in the pit he cannot sit still...` | 0.00 | 0.57 |
| `large-v3` | `lux is in the pit he cannot sit still...` | 0.00 | 0.00 |

`large-v3`, `large-v3-turbo` and `distil-large-v3` all return **lowercase,
unpunctuated** text. Word error rate is essentially identical across the
whole family — the fixture is easy — so a report that measured only WER
would call these models equivalent, and for dictation they are not: one
produces text you can paste into a document, the others produce text you
have to re-punctuate by hand.

This is why the benchmark scores punctuation and capitalization separately
from WER rather than normalizing them away.

**Next step:** find out whether this is fixable from mavor's side. whisper.cpp
takes an initial `--prompt`, and a prompt containing punctuated prose is the
standard way to coax formatted output out of these models — which is the same
mechanism the vocabulary-biasing item above wants. If it works, one change
closes both.

There is now also a way around it rather than through it. With every sherpa
model loading, `canary-180m` scores 1.8% WER with **punctuation 0.18 and
capitalisation 1.00** — the same formatting quality as `base.en` — in 457 MB
and 4.4 s. It is the only model in the catalog that combines large-model
accuracy with usable formatting, and it is a candidate for the accurate
preset that `large-v3` currently cannot fill.

### 📦 2. `mavor doctor` — the checks it still does not do

The GPU check landed. These are the remaining silent-failure modes:

- **Model integrity.** `checkModel` stats the file. A truncated or partial
  download passes and then fails at transcription time. Verify the size against
  the catalog's `DownloadSize` — the field already exists and is exact.
- **whisper-cli capability.** Report the whisper.cpp version and which
  `load_backend:` lines it emits; `cmd/mavor/gpu.go` already parses them.
- **Config coherence.** Flag combinations that cannot work: a
  `sherpa_hotwords_file` set on a CTC or encoder-decoder model, which
  sherpa-onnx cannot apply; `engine = "sherpa"` with a whisper model name.
- **Disk headroom.** `models pull large-v3` wants 2.9 GB and fails partway with
  a confusing error when the cache filesystem is full.
- **Machine-readable output.** `--json` so the checks can run in CI and in the
  integration harness rather than only being read by a human.

### 🛑 3. `engine = "server"` cannot start the packaged whisper-server

Found by the warm-server benchmark, which is the first thing in the project
ever to drive this engine end to end. Two independent breaks, both in
[`internal/speech/supervisor.go`](../internal/speech/supervisor.go) and
[`server.go`](../internal/speech/server.go):

- **A Unix socket cannot work at all.** `DefaultServerCommand` passes
  `--socket <path>` when `server_socket` is a filesystem path. whisper.cpp's
  server has no such flag — it binds host and port — so the child exits
  immediately with `error: unknown argument: --socket` and the supervisor
  reports a readiness failure. This is the *default* shape of `server_socket`
  in the config scaffold.
- **Over HTTP the request 404s.** The client posts to
  `/v1/audio/transcriptions` unless the endpoint already ends in `/inference`.
  whisper.cpp 1.9.2 serves `/inference` and returns `File Not Found` for the
  OpenAI path.

So `engine = "server"` works today only if a user writes
`server_socket = "http://127.0.0.1:8080/inference"` by hand, which nothing
documents. The benchmark uses exactly that, and says so in a comment.

**Next step:** decide whether the supervisor keeps pretending Unix sockets are
available. Cheapest honest fix: bind loopback with a chosen port when a socket
path is configured, and probe `/inference` before falling back to the OpenAI
path — plus a test that the generated argv is one the binary accepts. Whatever
the shape, the fix needs a failing test first; there is none today because
nothing exercised the engine.

### 📦 5. `formatFileSize` labels MiB as "MB"

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

`docs/reports/model_transcription_comparison.md` survives: it stays inside what
`scripts/benchmark-multi-models.py` can actually measure — three whisper models
on CPU across thread counts.

Their replacement is [`model-benchmarks.md`](reports/model-benchmarks.md),
generated by `just bench` and regenerable on any machine.

---

## 4. Open Threads & Workstreams (🏗️, 🔒, 🛑, 🧊)

### 🛑 CI has never run

`.github/workflows/ci.yml` exists and `mise.toml` pins `just` and `staticcheck`,
so it should work — but it has never executed. Treat the workflow as unverified
until a run goes green.

**Next step:** watch the first Actions run. The workflow no longer installs any
system packages — the build is pure Go — so the remaining risk is the mise
toolchain step rather than anything distro-specific.

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

### 🔒 sherpa-onnx GPU is gated on the vendored runtime

Separate from the product question above: even if GPU is wanted, the
`sherpa-onnx-go-linux` module ships a CPU-only ONNX Runtime with no provider
libraries. Nothing in mavor's code can change that — it needs a differently-built
runtime vendored in.

**Next step:** none until the 💬 GPU decision resolves.

### 🧊 AOT-compiled runtimes (ExecuTorch / IREE)

[`next-gen-runtimes-executorch-iree.md`](design/next-gen-runtimes-executorch-iree.md)
evaluates ahead-of-time compiled inference against the graph interpreters mavor
uses today.

Genuinely uncertain this is worth building. The premise is that compilation buys
latency, but `base.en` at 12.2× real time on CPU — 36.6× on a Vulkan build —
is already far past what dictation needs, and both runtimes would add a
heavyweight toolchain to the build.

**Next step:** revisit once item 1 produces catalog-wide numbers. If the fast
sherpa transducers turn out to close the gap on their own, this stays frozen.
