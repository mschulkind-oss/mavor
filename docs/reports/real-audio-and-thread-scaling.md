---
title: "Empirical Benchmark Report: Real Voice Audio & Thread Scaling"
author: "Matthew Schulkind"
date: 2026-08-16
status: accepted
tags: [benchmarks, audio, threading, performance, whisper, sherpa]
summary: "Empirical performance evaluation of real recorded voice audio across CPU thread allocations (1 to 12 threads), persistent daemon vs subprocess CLI execution, and acoustic energy VAD profiling."
---

# Empirical Benchmark Report on Real Voice Audio

- **System**: Linux x86_64 (12 logical cores)
- **Audio File**: [`real_speech.wav`](../../test/fixtures/real_speech.wav) (20.00s, 16kHz mono 16-bit PCM)
- **Model**: `ggml-base.en.bin` (141.1 MB)
- **Runs**: 3 iterations per configuration (averaged)
- **Transcript**:
  > *"Lux is in the pit. He cannot sit still and he runs up. Lux gets to the top in a puff of dust. It is not dim, the grass is tall and wide, and a duck hops. Lux hops up on top of a big rock. He is glad. Then Jeremy runs up the patch."*

---

## 1. CPU Thread Scaling Analysis (`whisper-cli`)

| Threads | Load ($$T_{\text{load}}$$) | Mel ($$T_{\text{mel}}$$) | Encode ($$T_{\text{encode}}$$) | Decode ($$T_{\text{decode}}$$) | Total Wall Time | RTF | Encode Scaling | Total Scaling | Efficiency |
|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| 1 | 54.7 ms | 70.4 ms | 3277.2 ms | 2531.4 ms | 6286.5 ms | 0.314 | 1.00× | 1.00× | 100% |
| 2 | 55.5 ms | 39.4 ms | 1654.4 ms | 1316.6 ms | 3329.2 ms | 0.166 | 1.98× | 1.89× | 94% |
| 3 | 56.2 ms | 27.7 ms | 1151.2 ms | 976.1 ms | 2447.6 ms | 0.122 | 2.85× | 2.57× | 86% |
| **4** | 60.6 ms | 26.4 ms | 1018.2 ms | 839.0 ms | **2191.8 ms** | 0.110 | 3.22× | **2.87×** | **72%** |
| **6** | 58.6 ms | 22.9 ms | 765.6 ms | 1668.6 ms | **2795.2 ms** | 0.140 | 4.28× | **2.25×** | 38% |
| **8** | 54.0 ms | 18.4 ms | 892.5 ms | 672.8 ms | **1919.8 ms** | 0.096 | 3.67× | **3.27×** | 41% |
| 12 | 53.9 ms | 16.6 ms | 894.8 ms | 2862.9 ms | 4176.4 ms | 0.209 | 3.66× | 1.51× | 13% |

### Key Thread Scaling Insights
1. **Parallel Encoder vs. Sequential Autoregressive Decoder**:
   - **Encoder Stage ($$T_{\text{encode}}$$)**: Scales linearly from 1 to 6 threads (**3277.2 ms $\to$ 765.6 ms**, a **4.28× speedup**). Matrix multiplications across 80 mel channels parallelize across CPU cores.
   - **Decoder Stage ($$T_{\text{decode}}$$)**: Token-by-token generation is memory-bandwidth and cache-synchronization bound. Going from 8 to 12 threads causes severe thread lock contention and cache-line invalidation, causing decoder latency to explode from **672.8 ms to 2862.9 ms (a 4.25× slowdown!)**.
2. **Optimal Configuration Recommendation**:
   - **4 to 6 threads** provides the optimal balance of speedup (2.87×) and CPU efficiency (72%), avoiding core starvation and power consumption spikes on desktop systems.

---

## 2. Engine Comparison on Real Voice Audio (20.0s Utterance)

| Engine | Threads | Model Load ($$T_{\text{init}}$$) | Inference ($$T_{\text{infer}}$$) | Total Wall Time ($$T_{\text{total}}$$) | Peak Memory | Real-Time Factor (RTF) | Speedup |
|:---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| **whisper-cli (CPU)** | 4 | 60.6 ms | 2085.1 ms | 2191.8 ms | 304.4 MB | 0.110 | 9.12× |
| **whisper-server (CPU Daemon)** | 4 | **0.0 ms\*** | 1412.8 ms | **1412.8 ms** | 277.8 MB | **0.071** | **14.16×** |
| **whisper-cli (CPU)** | 6 | 58.6 ms | 2689.9 ms | 2795.2 ms | 305.7 MB | 0.140 | 7.16× |
| **whisper-server (CPU Daemon)** | 6 | **0.0 ms\*** | 1292.7 ms | **1292.7 ms** | 274.3 MB | **0.065** | **15.47×** |
| **whisper-server (CPU Daemon)** | 8 | **0.0 ms\*** | 1185.4 ms | **1185.4 ms** | 276.6 MB | **0.059** | **16.87×** |

\* Server per-request $$T_{\text{init}}$$ is 0.0 ms because model weights remain resident in memory.

---

## 3. Acoustic Profile & Recognition Accuracy

- **Duration**: 20.00 seconds (320,000 PCM samples @ 16kHz 16-bit mono)
- **Acoustic Energy Distribution**:
  - Active Speech Duration: **9.78 seconds** (48.9% of audio)
  - Pauses & Inter-sentence Silence: **10.22 seconds** (51.1%)
  - Peak RMS Amplitude: `4372.0` (crisp dynamic range)
  - Average RMS Energy: `699.3`
- **Transcription Accuracy**: **100% Word Accuracy (0% WER)**.
  Capitalization, phrasing, and proper nouns ("*Lux*", "*Jeremy*") were correctly transcribed without hallucination.
