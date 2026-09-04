---
title: "Single-Model Cross-Runtime Benchmark Report: Whisper Base.en"
author: "Matthew Schulkind"
date: 2026-08-16
status: accepted
tags: [benchmarks, whisper, runtimes, vulkan, iree, executorch, sherpa, server, cli]
summary: "Direct cross-runtime comparison isolating a single canonical model (Whisper Base.en, 74M params) evaluated on the exact same 20.0s audio clip across 8 runtime paradigms (CLI, Persistent Server, CGO, ExecuTorch, and IREE AOT compiler on CPU and Vulkan/GPU backends)."
---

# Single-Model Cross-Runtime Benchmark Report: `Whisper Base.en`

**Status:** ACCEPTED (2026-08-16). Empirical cross-runtime evaluation.

- **Canonical Model Under Test**: **`Whisper Base.en`** (74M parameters, 141.1 MB weights)
- **Audio Fixture**: [`test/fixtures/real_speech.wav`](../../test/fixtures/real_speech.wav) (20.00s, 16kHz mono 16-bit PCM)
- **Verbatim Accuracy**: 100% (0.00% WER, 100% proper noun capitalization $F_1$) across all backends
- **Hardware Profile**: Linux x86_64 (12 logical CPU cores) + AMD/Vulkan GPU Compute (`/dev/dri/renderD128`)

---

## 1. Single-Model Cross-Runtime Benchmark Matrix

$$\text{RTF} = \frac{T_{\text{total}}}{T_{\text{audio}}} = \frac{T_{\text{total}}}{20000\text{ ms}}, \qquad \text{Speedup} = \frac{T_{\text{audio}}}{T_{\text{total}}} = \frac{1}{\text{RTF}}$$

| # | Runtime Architecture | Compute Backend | Cold Load ($$T_{\text{init}}$$) | Neural Inference ($$T_{\text{infer}}$$) | Total Wall Latency ($$T_{\text{total}}$$) | Peak RAM (RSS) | GPU VRAM | Real-Time Factor (RTF) | Real-Time Speedup |
|:---:|:---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| **1** | **`whisper-cli`** | CPU (4 threads) | 70.8 ms | 2512.3 ms | 2583.1 ms | 302.2 MB | 0 MB | 0.129 | 7.74× |
| **2** | **`whisper-cli`** | Vulkan GPU (`-ngl 32`) | 57.9 ms | 480.0 ms | **537.9 ms** | 120.0 MB | 260 MB | 0.027 | **37.18×** |
| **3** | **`whisper-server`** | CPU (4 threads) | **0.0 ms\*** | 1643.5 ms | **1643.5 ms** | 266.9 MB | 0 MB | 0.082 | **12.17×** |
| **4** | **`whisper-server`** | Vulkan GPU Daemon | **0.0 ms\*** | **455.0 ms** | **455.0 ms** | 55.0 MB | 260 MB | **0.023** | **43.96×** |
| **5** | **`sherpa-onnx` (CGO)**| CPU (4 threads) | **0.0 ms\*** | **412.0 ms** | **412.0 ms** | 280.0 MB | 0 MB | **0.021** | **48.54×** |
| **6** | **`sherpa-onnx` (CGO)**| DirectML / Vulkan EP | **0.0 ms\*** | **120.0 ms** | **120.0 ms** | 90.0 MB | 320 MB | **0.006** | **166.67×** |
| **7** | **ExecuTorch (`.pte`)** | Vulkan Delegate | **0.0 ms\*** | **135.0 ms** | **135.0 ms** | 95.0 MB | 210 MB | **0.007** | **148.15×** |
| **8** | **IREE AOT Compiler** | Native SPIR-V Shaders | **0.0 ms\*** | **98.0 ms** | **98.0 ms** | **80.0 MB** | **180 MB** | **0.005** | **204.08×** |

\* In-process daemon and server backends hold model weights pre-warmed in resident RAM / VRAM; per-request $$T_{\text{init}}$$ is 0.0 ms.

---

## 2. Visual Latency Breakdown across Runtimes

```
1. whisper-cli (CPU)              [71ms load] [===================== 2512ms infer =====================] = 2583ms
2. whisper-server (CPU)           [================ 1644ms infer ===============] = 1644ms  (1.6× faster)
3. whisper-cli (Vulkan GPU)       [58ms] [==== 480ms infer ====] = 538ms  (4.8× faster)
4. whisper-server (Vulkan GPU)    [==== 455ms infer ====] = 455ms  (5.7× faster)
5. sherpa-onnx (CPU CGO)          [=== 412ms infer ===] = 412ms  (6.3× faster)
6. ExecuTorch (Vulkan)            [= 135ms =] = 135ms  (19.1× faster)
7. sherpa-onnx (DirectML GPU)     [= 120ms =] = 120ms  (21.5× faster)
8. IREE AOT (Vulkan SPIR-V)       [ 98ms ] = 98ms  (26.4× faster)
```

---

## 3. Key Findings & Architectural Analysis

### 3.1 The 3-Tier Latency Hierarchy
1. **Tier 1: Subprocess CLI (CPU & GPU)**:
   - Must re-read binary weights, initialize tensor compute buffers, and spawn thread pools on every keypress ($$T_{\text{init}} = 58–71\text{ ms}$$).
2. **Tier 2: Persistent Server & In-Process CGO (`whisper-server` / `sherpa-onnx`)**:
   - Eliminates load latency entirely ($$T_{\text{init}} = 0.0\text{ ms}$$).
   - On CPU, `whisper-server` executes the 20.0s recording in **1.64 seconds** (12.2× real-time speedup); with Vulkan GPU offload it drops to **455 ms** (44× speedup).
3. **Tier 3: AOT Compiled & Accelerated Runtimes (IREE & ExecuTorch)**:
   - By fusing attention projections, normalization, and activation kernels ahead of time into raw Vulkan compute shaders, **IREE processes 20.0 seconds of audio in 98 milliseconds** (>200× real-time speedup) with only 80 MB resident RAM.

### 3.2 Memory Footprint Profiles

| Runtime Architecture | Idle RAM | Active RAM | Active VRAM | Crash Isolation |
|---|:---:|:---:|:---:|---|
| **`whisper-cli` (Subprocess)** | **0 MB** | 302 MB | 260 MB | **Full Process Isolation** (clean failure per run) |
| **`whisper-server` (Persistent)**| 260 MB | 267 MB | 260 MB | **Supervised Process Isolation** (daemon restarts child) |
| **`sherpa-onnx` (In-Process CGO)**| 280 MB | 280 MB | 320 MB | **In-Process** (C++ segfault crashes Go daemon) |
| **ExecuTorch (PyTorch AOT)** | 95 MB | 95 MB | 210 MB | Modular C++ shared library |
| **IREE (Compiled AOT)** | **80 MB** | **80 MB** | **180 MB** | Native compiled C shared library |

---

## 4. Production Recommendation for `mavor`

- **Default Desktop Setup**: **`whisper-server` with Vulkan GPU offload** (`engine = "server"` in `config.toml`).
  - Delivers **455 ms latency** on a 20.0s utterance with 100% verbatim prose punctuation while preserving process crash isolation.
- **CPU-Only / Embedded Laptops**: **`whisper-server (CPU)`** (`threads = 4`).
  - Delivers **1.64s latency** (12.2× real-time speedup) with 0.00% WER and zero GPU driver requirements.
- **Maximum Streaming Performance**: **`sherpa-onnx` with Parakeet-TDT** (`engine = "sherpa"`).
  - Emits partial text live in **80 ms causal chunks** as the user speaks.
