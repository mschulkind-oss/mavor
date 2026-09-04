---
title: "Ongoing Work: mavor Roadmap"
author: "Matthew Schulkind"
date: 2026-09-03
status: in-review
tags: [roadmap, benchmarks, doctor, models, gpu, sherpa, whisper, release]
summary: "Living roadmap for the mavor dictation daemon: open decisions, the ready-to-build queue, and active workstreams across benchmarking, diagnostics, and the first public release."
---

# Ongoing Work: `mavor` Voice-to-Text Utility

**Status:** 2 Needs Attention (💬), 6 Ready to Implement (📦), 4 Open Threads (🔒 2, 🛑 1, 🧊 1)

---

## 1. Attention Required (💬)

### 💬 🤷 Is GPU acceleration worth pursuing at all?

`mavor doctor` now reports the truth, and the truth is that neither engine
accelerates on a stock install:

- **whisper.cpp** — the nixpkgs build ships CPU backends only. GPU needs a
  whisper.cpp built with `-DGGML_VULKAN=ON`, which is a packaging problem, not a
  code one.
- **sherpa-onnx** — the ONNX Runtime bundled with the Go binding carries no
  provider libraries at all, so `sherpa_provider = "cuda"` can only fall back to
  CPU. Fixing this means vendoring a GPU-built runtime and shipping a much
  larger, hardware-specific binary.

Meanwhile `base.en` already runs at **7.3× real time on CPU**, which is well
inside the latency budget for dictation.

This is a product call about how much you care, not a technical one — deferring
to you. The options: leave it CPU-only and document that clearly; or take on
GPU packaging as a real workstream.

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

### 📦 1. Benchmark the whole catalog, not three models of it

The single biggest gap. `mavor models list --verbose` shows a real, measured
real-time factor for **3 of 24 models** — `tiny.en`, `base.en`,
`large-v3-turbo` — and a hand-assigned relative tier for the other 21.
`scripts/benchmark-multi-models.py` hardcodes those same three at
[`scripts/benchmark-multi-models.py:52`](../scripts/benchmark-multi-models.py).

**No sherpa model has ever been benchmarked**, so every claim about the
streaming transducers being fast is inference from architecture, not evidence.

The work:

- Drive the benchmark harness from `modelCatalog` instead of a hardcoded list,
  so a new catalog entry is automatically in scope.
- Cover both engines — the sherpa path is in-process CGO and needs different
  instrumentation than the `whisper-cli` subprocess.
- Report accuracy alongside speed. `test/fixtures/real_speech.wav.txt` is the
  ground truth; a word error rate per model would turn "slow but accurate" from
  an assumption into a number.
- Feed the results back into `MeasuredRTF` so `--verbose` stops estimating.

This unblocks honest model recommendations everywhere else — the config presets,
the README, and the `speed` field all currently rest on three data points.

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

### 📦 3. Fix `DetectSherpaModelType` misclassifying `parakeet-ctc`

[`internal/speech/sherpa.go:442`](../internal/speech/sherpa.go) matches
`strings.Contains(cleanName, "parakeet")` **even when the directory has no
joiner file**, so `parakeet-ctc` — a CTC model — is classified as a transducer.
The fix is to let the file-layout evidence win over the name match.

Held back only because it wants a real `parakeet-ctc` download to verify
against rather than a guess. Fold it into item 1, which will have every model
on disk anyway.

### 📦 4. `mavor models list --json`

Machine-readable catalog output. Roughly ten lines, consistent with
`mavor history --json`, and it makes the catalog scriptable for the benchmark
harness in item 1.

### 📦 5. `formatFileSize` labels MiB as "MB"

[`cmd/mavor/models_cmd.go`](../cmd/mavor/models_cmd.go) divides by 1024² and prints
"MB", so the catalog shows `74.1 MB` where Hugging Face shows `77.7 MB` for the
same file. Harmless in isolation, confusing when a user compares the two.

Pick one convention and apply it everywhere — the same formatter renders
installed sizes and doctor output, so this is a small change with a wide blast
radius.

### 📦 6. Retire the design docs that have shipped

[`how-mavor-works.md`](design/how-mavor-works.md) and
[`local-engine-benchmarks-and-architecture.md`](design/local-engine-benchmarks-and-architecture.md)
are both `status: accepted` and describe systems that are built. Per the
`system-doc` convention they should become evergreen system references and the
design docs deleted.

---

## 3. Open Threads & Workstreams (🏗️, 🔒, 🛑, 🧊)

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
latency, but `base.en` at 7.3× real time is already fast enough for dictation,
and both runtimes would add a heavyweight toolchain to the build.

**Next step:** revisit once item 1 produces catalog-wide numbers. If the fast
sherpa transducers turn out to close the gap on their own, this stays frozen.
