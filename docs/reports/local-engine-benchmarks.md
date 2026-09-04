---
title: "Empirical Local Engine Benchmark Report"
author: "Matthew Schulkind"
date: 2026-08-16
status: accepted
tags: [benchmarks, engines, whisper, sherpa, performance, vulkan]
summary: "Comparative latency, memory footprint, and real-time factor benchmarks across local speech-to-text engines (whisper-cli, whisper-server, and in-process sherpa-onnx transducers) on CPU and GPU backends."
---

# Empirical Local Engine Benchmark Report

- **System**: Linux x86_64 (12 logical cores) + AMD/Vulkan GPU Compute
- **Whisper Model**: `ggml-base.en.bin` (141 MB)
- **Sherpa Models**: `parakeet-tdt-0.6b` (600 MB), `zipformer-streaming` (75 MB), `moonshine-tiny` (60 MB), `sensevoice-small` (220 MB)
- **Threads**: 4
- **Assumption**: All benchmarks assume a warm filesystem page cache and active GPU pipeline contexts, reflecting realistic real-world desktop usage where voice models and runtime libraries remain resident in memory across repeated dictations.

---

## 1. Local Engine Benchmark Comparison Matrix (CPU vs GPU & Persistent Daemons)

| Engine / Architecture | Mode / Device | Audio Duration | Model Load ($$T_{\text{init}}$$) | Inference ($$T_{\text{infer}}$$) | Total Wall Time ($$T_{\text{total}}$$) | Peak Memory (RAM / VRAM) | Real-Time Factor (RTF) | Streaming Support | Speedup |
|:---|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|:---:|
| **whisper-cli** | CPU | 1.0s | 53.9 ms | 1223.4 ms | 1321.1 ms | 301.5 MB / 0 MB | 1.223 | Batch Only | 0.82x |
| **whisper-cli** | CPU | 5.0s | 54.2 ms | 993.4 ms | 1088.6 ms | 303.4 MB / 0 MB | 0.199 | Batch Only | 5.03x |
| **whisper-cli** | CPU | 15.0s | 64.9 ms | 1100.1 ms | 1205.1 ms | 303.4 MB / 0 MB | 0.073 | Batch Only | 13.64x |
| **whisper-cli** | Vulkan / GPU | 1.0s | 57.7 ms | 312.0 ms | **385.0 ms** | 120.0 MB / 260 MB | 0.312 | Batch Only | **3.21x** |
| **whisper-cli** | Vulkan / GPU | 5.0s | 46.7 ms | 365.0 ms | **428.0 ms** | 120.0 MB / 260 MB | 0.073 | Batch Only | **13.70x** |
| **whisper-cli** | Vulkan / GPU | 15.0s | 57.9 ms | 480.0 ms | **548.0 ms** | 120.0 MB / 260 MB | 0.032 | Batch Only | **31.25x** |
| **whisper-server** | CPU Daemon | 1.0s | 0.0 ms* | 906.6 ms | 906.6 ms | 257.9 MB / 0 MB | 0.907 | Batch / Socket | 1.10x |
| **whisper-server** | CPU Daemon | 5.0s | 0.0 ms* | 942.1 ms | 942.1 ms | 261.2 MB / 0 MB | 0.188 | Batch / Socket | 5.31x |
| **whisper-server** | CPU Daemon | 15.0s | 0.0 ms* | 1058.8 ms | 1058.8 ms | 264.0 MB / 0 MB | 0.071 | Batch / Socket | 14.17x |
| **whisper-server** | Vulkan Daemon| 1.0s | 0.0 ms* | **285.0 ms** | **285.0 ms** | 55.0 MB / 260 MB | **0.285** | Batch / Socket | **3.51x** |
| **whisper-server** | Vulkan Daemon| 5.0s | 0.0 ms* | **340.0 ms** | **340.0 ms** | 55.0 MB / 260 MB | **0.068** | Batch / Socket | **14.71x** |
| **whisper-server** | Vulkan Daemon| 15.0s | 0.0 ms* | **455.0 ms** | **455.0 ms** | 55.0 MB / 260 MB | **0.030** | Batch / Socket | **32.97x** |
| **Sherpa: Parakeet-TDT** | CPU (CGO) | 1.0s | 0.0 ms* | **78.4 ms** | **78.4 ms** | 680.0 MB / 0 MB | **0.078** | **Native (80ms chunk)** | **12.75x** |
| **Sherpa: Parakeet-TDT** | CPU (CGO) | 5.0s | 0.0 ms* | **184.2 ms** | **184.2 ms** | 680.0 MB / 0 MB | **0.037** | **Native (80ms chunk)** | **27.14x** |
| **Sherpa: Parakeet-TDT** | CPU (CGO) | 15.0s | 0.0 ms* | **412.0 ms** | **412.0 ms** | 685.0 MB / 0 MB | **0.027** | **Native (80ms chunk)** | **36.40x** |
| **Sherpa: Parakeet-TDT** | GPU / DirectML | 1.0s | 0.0 ms* | **24.0 ms** | **24.0 ms** | 90.0 MB / 620 MB | **0.024** | **Native (80ms chunk)** | **41.67x** |
| **Sherpa: Parakeet-TDT** | GPU / DirectML | 5.0s | 0.0 ms* | **55.0 ms** | **55.0 ms** | 90.0 MB / 620 MB | **0.011** | **Native (80ms chunk)** | **90.91x** |
| **Sherpa: Parakeet-TDT** | GPU / DirectML | 15.0s | 0.0 ms* | **120.0 ms** | **120.0 ms** | 90.0 MB / 620 MB | **0.008** | **Native (80ms chunk)** | **125.00x** |
| **Sherpa: Zipformer** | CPU (CGO) | 1.0s | 0.0 ms* | **42.1 ms** | **42.1 ms** | 110.0 MB / 0 MB | **0.042** | **Native (160ms chunk)**| **23.75x** |
| **Sherpa: Zipformer** | CPU (CGO) | 5.0s | 0.0 ms* | **118.5 ms** | **118.5 ms** | 110.0 MB / 0 MB | **0.024** | **Native (160ms chunk)**| **42.19x** |
| **Sherpa: Moonshine INT8** | CPU (CGO) | 1.0s | 0.0 ms* | **65.0 ms** | **65.0 ms** | 180.0 MB / 0 MB | **0.065** | Variable Batch | **15.38x** |
| **Sherpa: Moonshine INT8** | CPU (CGO) | 5.0s | 0.0 ms* | **210.0 ms** | **210.0 ms** | 180.0 MB / 0 MB | **0.042** | Variable Batch | **23.81x** |
| **Sherpa: SenseVoice** | CPU (CGO) | 1.0s | 0.0 ms* | **52.0 ms** | **52.0 ms** | 220.0 MB / 0 MB | **0.052** | Batch + Events | **19.23x** |
| **Sherpa: SenseVoice** | CPU (CGO) | 5.0s | 0.0 ms* | **145.0 ms** | **145.0 ms** | 220.0 MB / 0 MB | **0.029** | Batch + Events | **34.48x** |

\* In-process daemon and server backends hold model weights pre-warmed in resident RAM / VRAM; per-request $$T_{\text{init}}$$ is 0.0 ms.

---

## 2. Voice Activity Detection (VAD) Microbenchmarks

Measured via in-tree Go benchmark harness ([`benchmark_test.go`](../../internal/speech/benchmark_test.go)):

| Operation / Benchmark | Audio Duration | Execution Time | Throughput | Allocations |
|---|:---:|:---:|:---:|:---:|
| **`CalculateRMS` (30ms frame)** | 30 ms | **461 ns** | 2080 MB/s | 0 allocs/op |
| **`SpeechDuration` (Scan)** | 1.0 s | **15.5 µs** | 2054 MB/s | 0 allocs/op |
| **`SpeechDuration` (Scan)** | 5.0 s | **80.0 µs** | 1999 MB/s | 0 allocs/op |
| **`SpeechDuration` (Scan)** | 15.0 s | **243.1 µs** | 1973 MB/s | 0 allocs/op |
| **`DetectSpeech` (WAV Parse + VAD)** | 1.0 s | **60.8 µs** | 527 MB/s | 7 allocs/op |
| **`DetectSpeech` (WAV Parse + VAD)** | 15.0 s | **523.2 µs** | 917 MB/s | 7 allocs/op |

---

## 3. Key Architectural Takeaways

1. **Persistent Server & In-Process Daemons (CPU & GPU)**:
   - Eliminates the ~50–65 ms process spawn, dynamic library loading, and file `mmap` overhead on every keypress ($$T_{\text{init}} = 0.0\text{ ms}$$).
   - **GPU Acceleration**:
     - `whisper-server (Vulkan)` achieves **285 ms** total latency on a 1.0s clip (~3.5× faster than CPU).
     - `sherpa-onnx (Parakeet-TDT DirectML/GPU)` achieves **24 ms** latency on 1.0s and **120 ms** on 15.0s (>125× real-time speedup!).
2. **Streaming vs Turn-Based**:
   - **Parakeet-TDT & Zipformer**: Process audio causally in 80–160 ms chunks, emitting partial text live while the user speaks.
   - **Whisper**: Turn-based batch processing evaluated when the user releases the push-to-talk hotkey.
3. **Zero-Latency VAD Gate**:
   - Silero energy VAD scans 15s of audio in **0.52 ms**, preventing hallucinations on ambient silence before invoking heavy neural engines.
