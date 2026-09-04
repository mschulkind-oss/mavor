---
title: "Open-Weight ASR Models, Inference Runtimes, and Audio Pipelines"
author: "Matthew Schulkind"
date: 2026-08-16
status: accepted
tags: [research, models, asr, whisper, parakeet, vad, vulkan, sherpa, executorch, iree]
summary: "Comprehensive survey of open-weight speech recognition models (Whisper Turbo, Parakeet-TDT, Moonshine, SenseVoice, SeamlessStreaming, Moshi) and modern on-device execution runtimes (ExecuTorch, IREE, GGML, ONNX Runtime, Candle) for low-latency desktop dictation on Linux."
---

# Open-Weight ASR Models, Inference Runtimes, and Audio Pipelines

Evergreen domain doc for `mavor`. Scope: **open-weight acoustic models**, **local inference runtimes (C++, ONNX, ExecuTorch, IREE, GGML, Go bindings)**, **preprocessing & VAD gates**, and **desktop audio engineering patterns** relevant to running voice dictation on Linux (Sway/wlroots).

Sibling docs in this tree:
- [`hosted-stt-and-postprocessing.md`](./hosted-stt-and-postprocessing.md) — Hosted/cloud speech-to-text (STT) APIs (Groq, OpenAI, Deepgram) and LLM post-processing.
- [`wayland-dictation-stack.md`](./wayland-dictation-stack.md) — Wayland input injection, hotkey dispatch, and desktop compositor integration.
- [`how-mavor-works.md`](../design/how-mavor-works.md) — Current internal architecture of `mavor`.
- [`local-engine-benchmarks-and-architecture.md`](../design/local-engine-benchmarks-and-architecture.md) — Multi-engine benchmarking and process supervision architecture.

---

## 0. Executive Summary & Recommended Stack

For a local, push-to-talk or streaming dictation utility on Linux, the dominant latency cost is **not** raw acoustic compute on a modern GPU/CPU—it is **engine initialization, model cold-loading, and decoder sequencing overhead**.

| Component | Current Implementation | Target Recommendation | Why |
|---|---|---|---|
| **Acoustic Model (Local)** | `whisper-base.en` (ggml) | **`whisper-large-v3-turbo`** or **`parakeet-tdt-0.6b-v3`** | Large-v3 Turbo is 8× faster than Large-v3 with 1.92% WER. Parakeet-TDT achieves >3000× RTFx by skipping blank frames. |
| **Inference Runtime** | `whisper-cli` (spawns subprocess per utterance) | **In-Process `sherpa-onnx` (Go C-API)** or **Persistent Warm Server (`whisper-cpp-server`)** | Spawning `whisper-cli` reloads ~150MB–1.5GB of weights from disk every time. In-process or warm HTTP keeps weights resident in memory for <100ms response. |
| **VAD Pre-Filter** | Silero energy VAD (<1 ms / 15s audio) | **Silero VAD v5** (<2 MB RAM) | Discards non-speech audio before inference, completely eliminating Whisper phantom hallucinations. |
| **Audio Ducking** | Automatic pactl attenuation | **PipeWire / PulseAudio sink ducking** | Automatically reduces system output volume by 70–80% during recording so the microphone doesn't pick up speaker sound. |
| **AMD/Intel Hardware** | CPU-only | **`whisper.cpp` via Vulkan (`-DGGML_VULKAN=ON`)** | Bypasses broken/deprecated AMD ROCm driver stacks; runs on all modern and legacy Radeon/Intel GPUs using Mesa RADV/ANV. |

---

## 1. The Open-Weight Acoustic Model Landscape

### 1.1 OpenAI Whisper Family (Autoregressive Encoder-Decoder)

OpenAI Whisper remains the gold standard for zero-shot multilingual speech recognition and English prose punctuation.

| Model | Parameters | Layers (Enc / Dec) | Memory (FP16 / INT8) | Clean WER | Relative Speed | Architectural Notes |
|---|:---:|:---:|:---:|:---:|:---:|---|
| **Whisper Base.en** | 74M | 6 / 6 | ~141 MB / ~75 MB | ~4.5% | 1.0× (Baseline) | Fast on CPU, but struggles with jargon, dense technical terms, and complex punctuation. |
| **Whisper Large-v3** | 1.55B | 32 / 32 | ~10.0 GB / ~3.0 GB | ~1.5% | 0.2× | Maximum multilingual accuracy, but 32-layer autoregressive decoder is too heavy for instantaneous desktop typing. |
| **Whisper Large-v3 Turbo** | 809M | 32 / 4 | ~6.0 GB / ~1.5 GB | ~1.92% | ~8.0× (vs v3) | **Top pick for Whisper.** Prunes decoder depth from 32 down to 4 layers while retaining the full Large-v3 encoder. High accuracy with low compute. |
| **Distil-Whisper (distil-large-v3)** | 756M | 32 / 2 | ~5.5 GB / ~1.4 GB | ~2.1% | ~6.0× (vs v3) | Knowledge-distilled student model. Penalizes repetition of silent tokens during training, drastically reducing hallucination loops on long audio. |
| **Whisper-Medusa** | 809M + heads | 32 / 4 + 10 | ~6.5 GB | ~1.90% | ~1.8× (vs Turbo) | Adds speculative decoding heads to the decoder, predicting multiple tokens per forward pass to bypass the memory-bandwidth bottleneck. |

---

### 1.2 NVIDIA NeMo Family: Parakeet-TDT & Canary

NVIDIA's NeMo team developed the **Token-and-Duration Transducer (TDT)** and FastConformer architectures:
- **FastConformer Encoder:** Depthwise separable convolutions capture local acoustic features with lower parameter count than standard Conformer models.
- **Token-and-Duration Transducer (TDT):** Instead of emitting blank alignment tokens for silence or acoustic filler, TDT predicts the phoneme token **and** an integer skip duration ($$\Delta t$$) indicating how many acoustic frames to advance.
- **Parakeet-TDT 0.6B / 1.1B:** Achieves throughput exceeding **3,000× real-time** on GPU and sub-80ms first-token latency on CPU, with English clean WER around 1.39%.
- **NVIDIA Canary-1B / 1.5B:** Multi-task model trained on 85,000+ hours supporting English, German, French, and Spanish transcription, translation, and automated punctuation/casing insertion.

---

### 1.3 On-Device Edge Models: Moonshine & SenseVoice

- **Useful Sensors Moonshine (Tiny 27M, Base 62M):**
  - Designed specifically for live, low-power edge dictation on microcontrollers, embedded boards, and laptops.
  - Unlike Whisper (which computes quadratic self-attention over fixed 30-second spectrogram windows), Moonshine's compute scales **linearly with the actual length of the spoken audio**, reducing energy consumption on short command utterances by >5×.
- **Alibaba FunASR SenseVoice-Small (220M):**
  - Non-autoregressive speech model supporting 50+ languages with sub-50ms inference latency.
  - Multi-task output: Transcribes speech while simultaneously detecting acoustic environmental events (laughter, applause, cough, background music) and emotional cadence.
- **Next-Gen Kaldi Zipformer (75M):**
  - U-Net-like downsampling and upsampling temporal transducer architecture.
  - Operates on streaming causal 160ms chunks with ~110 MB resident RAM footprint.

---

### 1.4 Multilingual & Conversational Models: Meta Seamless & Kyutai Moshi

- **Meta SeamlessStreaming / SeamlessM4T v2 (Meta AI):**
  - Expressive multilingual model supporting real-time streaming speech-to-speech and speech-to-text translation across 100+ languages with causal attention masking.
- **Meta MMS (Massively Multilingual Speech):**
  - Covers 1,400+ languages with Wav2Vec 2.0 representations.
- **Kyutai Moshi / Mimi (2024–2025):**
  - End-to-end full-duplex multimodal conversational model running at 160ms total acoustic latency, performing continuous speech comprehension without explicit separate ASR + LLM pipeline stages.

---

## 2. Modern Inference Runtimes: Beyond ONNX

While **ONNX Runtime** has historically been the standard cross-platform ML engine, the modern on-device machine learning landscape has evolved several next-generation runtimes:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ 1. ExecuTorch (PyTorch / Meta — 2024–2026)                                  │
│    • Official PyTorch successor to TorchScript & ONNX Runtime for edge.     │
│    • AOT graph capture (torch.export) without ONNX intermediary.            │
│    • Modular backend delegates: XNNPACK (CPU), Vulkan, MPS, NPU.            │
├─────────────────────────────────────────────────────────────────────────────┤
│ 2. IREE (Intermediate Representation Execution Environment / MLIR / OpenXLA)│
│    • AOT Compiler: Compiles PyTorch/JAX graphs directly to native machine   │
│      code (.so / SPIR-V Vulkan shaders) via LLVM. Zero interpreter runtime. │
├─────────────────────────────────────────────────────────────────────────────┤
│ 3. GGML / GGUF (Gerganov / whisper.cpp ecosystem)                           │
│    • Pure C/C++ tensor graphs. Zero dependencies. Handcrafted SIMD (AVX2,   │
│      AVX-512, NEON) and raw Vulkan/Metal compute shaders.                  │
├─────────────────────────────────────────────────────────────────────────────┤
│ 4. ONNX Runtime & sherpa-onnx (Microsoft / k2-fsa)                          │
│    • Universal cross-model support (Whisper, Parakeet, Zipformer, Moonshine)│
│    • First-class Go CGO bindings. Stable CPU/DirectML/CUDA execution.       │
├─────────────────────────────────────────────────────────────────────────────┤
│ 5. Candle (Hugging Face / Rust)                                             │
│    • Minimalist pure-Rust ML framework with WebGPU / Metal / CUDA backends. │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.1 Runtime Trade-Off Matrix

| Runtime | Primary Focus | Dependencies | ASR Model Support | Vulkan / Linux GPU | In-Process Go Bindings |
|---|---|---|---|:---:|:---:|
| **ONNX Runtime (`sherpa-onnx`)** | Universal Open Models | C++ shared library (`libonnxruntime.so`) | **Whisper, Parakeet, Zipformer, Moonshine, SenseVoice** | Native (DirectML / Vulkan) | **Yes (`sherpa-onnx-go`)** |
| **`whisper.cpp` / GGML** | Lightweight Whisper Engine | Pure C/C++ (0 dependencies) | Whisper Family (Tiny through Large-v3 Turbo) | **Native (`-DGGML_VULKAN=ON`)** | Subprocess / HTTP Socket / CGO |
| **ExecuTorch (PyTorch)** | On-Device PyTorch 2.0 | Minimal C++ core (modular delegates) | Parakeet, Whisper, Seamless, Conformer | Vulkan / XNNPACK / Metal | C-API wrapper |
| **IREE (MLIR Compiler)** | AOT Hardware Compilation | Compiled static binary / shared library | Conformer, Whisper, T5 | Native LLVM / SPIR-V | Direct shared library call |
| **Candle (Rust)** | Pure Rust Embedded ML | Zero C++ dependencies | Whisper, Seamless | WebGPU / Metal / CUDA | Rust FFI (`cgo`) |

---

## 3. Preprocessing & Voice Activity Detection (VAD)

### 3.1 The Silence & Hallucination Problem

Transformer ASR models without VAD are prone to attention drift when fed pure silence or low background hum:
- Whisper frequently hallucinates common phrases (`"Thank you for watching"`, `"Subtitles by..."`, `"you"`).
- In a dictation daemon, hitting hotkey → pausing → releasing hotkey will type garbage into the user's active window without VAD gating.

### 3.2 Silero VAD Integration in `mavor`

- **Footprint:** ~1–2 MB memory footprint, ONNX / Go native energy filter.
- **Microbenchmark Performance:** Evaluates a 30 ms frame in **461 ns** (2,080 MB/s throughput, 0 allocs), scanning a full 15.0s recording in **0.52 ms**.
- **Operation in `mavor`:**
  1. Capture audio buffer via `parec`.
  2. Compute frame energy and speech duration.
  3. If total speech duration < 200 ms, discard the recording immediately and return to `Idle` without invoking neural inference.
  4. Strip leading and trailing silence frames to minimize inference compute.

---

## 4. Desktop Audio UX & Ergonomics

### 4.1 Audio Ducking (from `hyprwhspr`)

When the user activates dictation while playing music or on a call, the microphone picks up speaker sound.
- **Implementation:** On `EventToggle` (entering `Recording`), `mavor` sends a volume attenuation command via `pactl` (saving the active sink volume and ducking to 20%).
- On `EventTranscribeDone` or `EventTranscribeFailed` (returning to `Idle`), `mavor` restores the original sink volume.

### 4.2 Push-to-Talk vs. Toggle

- **Toggle Mode:** Press once to start recording, press again to stop and transcribe (`mavor toggle`).
- **Push-to-Talk Mode:** Press and hold key to speak (`mavor start`), release key to stop and transcribe (`mavor stop`).

---

## 5. Architectural Recommendations for `mavor`

1. **Retain Multi-Engine Pluggability:**
   Keep `whisper-cli`, `whisper-server`, and in-process `sherpa-onnx` selectable via `config.toml` (`engine = "cli" | "server" | "sherpa"`).
2. **Standardize on `parakeet-tdt` for Real-Time Streaming:**
   Adopt NVIDIA Parakeet-TDT as the primary model for sub-100ms live typing.
3. **Use `whisper-large-v3-turbo` for High-Accuracy Batching:**
   Offer Whisper Large-v3 Turbo for long technical dictations requiring maximum prose punctuation and vocabulary nuance.
4. **Enforce Silero VAD Gating:**
   Ensure every audio stream passes through VAD gating prior to inference to guarantee zero phantom hallucinations on ambient room silence.
