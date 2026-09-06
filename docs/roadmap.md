---
title: "Ongoing Work: mavor Roadmap"
author: "Matthew Schulkind"
date: 2026-09-05
status: in-review
tags: [roadmap, benchmarks, doctor, models, gpu, sherpa, whisper, release]
summary: "Living roadmap for the mavor dictation daemon: open decisions, the ready-to-build queue, and active workstreams across benchmarking, diagnostics, and the first public release."
---

# Ongoing Work: `mavor` Voice-to-Text Utility

**Status:** 1 Needs Attention (💬), 5 Ready to Implement (📦), 4 Open Threads (🏗️ 1, 🔒 1, 🛑 1, 🧊 1)

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

### ✅ Should whisper get vocabulary biasing? — RESOLVED (2026-09-05)

`--verbose` currently reports `vocabulary: none` for all 11 whisper models,
because mavor passes no initial prompt to `whisper-cli`. whisper.cpp supports
`--prompt` for exactly this, and the sherpa transducers already accept a
hotwords file.

There is a full design for the ambitious version —
[`active-window-context-and-vocabulary-prompting.md`](design/active-window-context-and-vocabulary-prompting.md),
still `in-review` — which derives vocabulary from the focused window via
`swaymsg -t get_tree`.

**Yes, the static half, and it is designed.**
[`design/configuration-surface.md`](design/configuration-surface.md) §7 specifies
a runtime-neutral `[vocabulary]` table — a prompt on whisper, a hotwords file on
the transducers, and nothing on the models that cannot use one, which `doctor`
reports rather than failing on. Its OQ-4 settled that this lands now rather than
waiting: the window-context design derives the word list at runtime and still
needs a static one to sit beside, so it adopts this key shape instead of
replacing it.

The window-context half stays where it is, in
[`design/active-window-context-and-vocabulary-prompting.md`](design/active-window-context-and-vocabulary-prompting.md),
still `in-review` and unbuilt.

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

**Left undone:** nothing measures the engine in CI, and `mavor doctor` still
does not check it — a user whose `whisper-server` is missing finds out at the
first dictation. That belongs with the doctor checks in item 2.

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

### 📦 7. The config file has 29 keys and three of them are wrong

[`design/configuration-surface.md`](design/configuration-surface.md) — DECIDED
2026-09-05, all five questions settled, nothing built.

Three findings worth reading even if the redesign is rejected. `gpu_layers`
passes `-ngl` to whisper.cpp, which does not accept it, so **any non-zero value
breaks every transcription** — and `doctor` currently recommends setting it.
`device` is written into a struct field nothing reads. And `mavor config init`
scaffolds a file that disagrees with the compiled defaults on `mode` and
`duck_audio`, so a user who runs it gets different behavior from one who does
not.

The design proposes 20 keys grouped into tables, separates *which model* from
*where its runtime runs* (the `engine` enum welds them together today), prefixes
every whisper catalog name with its family, and makes a small streaming model
the default source of the live preview instead of re-running the main model at
every pause.

One ruling already settled, and it reaches past the config file: **mavor becomes
a cgo-only program.** The pure-Go build and the `sherpa` build tag are deleted
rather than demoted, `build-sherpa` folds into `build`, and the release ships a
42 MB directory instead of an 11.8 MB static binary. That buys the thirteen
sherpa models out of the box and costs cross-compilation without a cross
toolchain. The release recipe must set `$ORIGIN` in its rpath or it will ship a
binary that runs only on the build host.

The first two steps — fixing `gpu_layers`, and the catalog rename — are
self-contained and worth landing on their own.

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
| **Leave ONNX entirely** | [`parakeet.cpp`](https://github.com/mudler/parakeet.cpp) reimplements Parakeet in ggml, validated at WER-0 against NeMo, with published GGUF weights. ggml already has the Vulkan backend giving whisper its 9.6×, so a ggml Parakeet **inherits RDNA4 acceleration for free**. | The only route with a live working precedent on this hardware |

> [!WARNING]
> A trap that applies to both ONNX routes: PR #2370's own author reports that
> **int8 models fall back to CPU** and only fp32 runs on the GPU. Every sherpa
> model in mavor's catalog is int8 or has an int8 variant as the fast path, so
> the payoff could be zero for exactly the models this would be done for.
> Verify that before writing any C++.

> [!NOTE]
> Zipformer has no ggml port that I could find — the third route covers
> Parakeet and, with more work, Moonshine, but not Zipformer. If the streaming
> Zipformer becomes the preview companion
> ([`design/configuration-surface.md`](design/configuration-surface.md) settled
> a 20M-parameter streaming zipformer as that companion), it stays on the CPU
> regardless. That is fine: the preview model is small by
> design and costs about one core.

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
latency, but `base.en` at 12.2× real time on CPU — 36.6× on a Vulkan build —
is already far past what dictation needs, and both runtimes would add a
heavyweight toolchain to the build.

**Next step:** revisit once item 1 produces catalog-wide numbers. If the fast
sherpa transducers turn out to close the gap on their own, this stays frozen.
