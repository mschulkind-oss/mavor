---
title: "User Stories: Voice-to-Text Dictation on Linux (mavor)"
author: "Matthew Schulkind"
date: 2026-08-16
status: accepted
tags: [user-stories, personas, ux, wayland, dictation, streaming, cgo]
summary: "Narrative user stories following concrete personas through real voice dictation workflows on Sway and Wayland with in-process CGO engines, streaming tokens, audio ducking, and HUD overlays."
---

# User Stories: Voice-to-Text Dictation on Linux (`mavor`)

This document explores how real developers, writers, and automated test agents interact with `mavor`, a background voice-to-text dictation daemon for Sway and Wayland compositors. It traces concrete workflows—from push-to-talk coding and live token streaming in Neovim to in-process Sherpa-ONNX model inference, PipeWire audio ducking, and automated headless Wayland QA.

---

## 1. Maya — Staff Engineer, Dictating Code into Neovim and Claude Code

**Context:** Maya spends 8 hours a day in tmux across Sway workspaces with Neovim, shell prompts, and Claude Code sessions. Continuous keyboard typing causes strain. She needs low-latency push-to-talk speech injection directly into whatever window holds focus, with live visual feedback and zero hallucinated silence.

**What happens:**

1. Maya holds down `$mod+grave` (`Super + \``) in her Neovim buffer.
   A dark pill HUD overlay appears 8px below Waybar displaying a live animated waveform volume meter:
   ```
   [ ● RECORDING   ▂▃▅▆ ]
   ```

2. While holding the key, Maya dictates:
   *"Refactor the state machine listener to release the mutex before dispatching side effects."*
   As she speaks, the live audio energy meter dynamically scales across six discrete levels (0% to 100%) tracking her vocal loudness.

3. Maya releases `$mod+grave`. The overlay crossfades to amber:
   ```
   [ ◌ TRANSCRIBING... ]
   ```

4. 120 ms later, `wtype` types the sentence directly into Neovim, and `wl-copy` synchronizes the string to the Wayland clipboard. The overlay smoothly closes.

5. Maya pauses for 4 seconds holding the key in a quiet room, then releases without speaking.

   **What happens today:**
   The integrated Silero Voice Activity Detector (VAD) evaluates the audio energy frames. Finding <250ms of speech energy, it silently aborts transcription and resets the daemon directly to `idle` without typing hallucinated filler words like `"you"` into her code.

**What would trip them up:**
- Trying to dictate while background music is playing through external speakers. Without audio ducking enabled, speaker audio bleeds into the microphone stream.

**What makes this work:**
- The layer-shell overlay requests no keyboard interactivity, so focus never leaves Neovim.
- The dual `wtype` + `wl-copy` output guarantees that even if a specialized terminal drops synthetic keystrokes, the transcript is immediately available in the clipboard via `p` or `Ctrl+V`.

---

## 2. Sam — Sway Power User, In-Process Sherpa-ONNX & Live Token Streaming

**Context:** Sam wants sub-100ms transcription latency and real-time streaming feedback without spawning heavyweight subprocesses on every keypress. Sam configures the in-process `sherpa-onnx` CGO engine and enables live streaming tokens.

**First 10 minutes:**

1. Sam clones `mavor` and builds the native CGO binary:
   ```console
   $ git clone https://github.com/mschulkind-oss/mavor && cd mavor
   $ just build
   ```

2. Sam downloads a streaming Zipformer acoustic model:
   ```console
   $ mkdir -p ~/.cache/mavor/models/sherpa/zipformer
   $ cp ~/downloads/sherpa-onnx-streaming-zipformer-en-2023-06-26/* ~/.cache/mavor/models/sherpa/zipformer/
   ```

3. Sam configures `~/.config/mavor/config.toml` with tilde and environment variable path expansion:
   ```toml
   engine = "sherpa"
   sherpa_model = "zipformer"
   sherpa_model_dir = "~/.cache/mavor/models/sherpa"
   duck_audio = true
   duck_volume = "15%"
   duck_streams = ["spotify", "firefox"]
   ```

4. Sam starts the daemon:
   ```console
   $ mavor daemon -v
   time=2026-08-16T15:00:01.120Z level=INFO msg="daemon starting" engine=sherpa model=zipformer provider=cpu ducking=true socket=/run/user/1000/mavor.sock
   ```

5. Sam hits `$mod+grave` while playing music in Spotify and speaking into a terminal prompt:
   - PipeWire instantly ducks Spotify audio down to 15% volume while preserving Sam's voice call in Discord.
   - As Sam speaks, intermediate token predictions appear live in the HUD overlay subtitle and stream into the focused window.
   - On release, Spotify audio smoothly restores to 100% volume.

**The aha moment:**
- Seeing partial words appear on screen in real time with <50ms chunk latency, completely eliminating the post-speech waiting period.

---

## 3. Lisa — Non-Technical Technical Writer, Pure Dictation Flow

**Context:** Lisa writes product documentation in Markdown on a Sway laptop. She wants zero setup friction, automatic temporary file cleanup, and clear error diagnostics if a microphone is disconnected.

**What happens:**

1. Lisa opens a document editor and presses the dictation key. She speaks continuously for 30 seconds:
   *"Section three outlines our data retention policy. All temporary audio recordings are automatically purged from disk upon transcription completion."*

2. She taps the hotkey to stop. Transcription finishes in 280ms, and the clean text lands in her editor:
   ```markdown
   Section three outlines our data retention policy. All temporary audio recordings are automatically purged from disk upon transcription completion.
   ```

3. Lisa checks `/tmp/mavor-recordings/` and confirms that the temporary WAV and sidecar text files were deleted immediately after transcription, leaving zero disk leaks or private audio retention.

4. Later, Lisa unplugs her USB headset while dictating:
   - The audio pipeline detects the disconnected stream and displays a red error visual on the HUD overlay:
     ```
     [ ✕ AUDIO CAPTURE FAILED ]
     ```
   - The overlay auto-clears after 1.5 seconds and returns cleanly to `idle` without wedging the daemon.

**What makes this work:**
- Robust temporary file cleanup in `daemon.go` prevents silent privacy leaks.
- Clear overlay visual state transitions communicate hardware failures instantly.

---

## 4. Derek — Just the Simple, Bulletproof Version

**Context:** Derek wants zero overhead. He wants a clean 3-state daemon that starts on Sway login, uses minimal RAM, and types what he says within a split second without complex external services.

**The minimal workflow:**

1. Add two lines to Sway config:
   ```
   exec mavor daemon
   bindsym $mod+grave exec mavor toggle
   ```
2. Press `$mod+grave` → talk → press `$mod+grave` → text is typed.
3. If no config file exists, the daemon runs with built-in defaults (`base.en` model, 8px margin, CPU inference).

**Progressive Complexity Layers:**
- **Layer 1:** Stock `whisper-cli` batch execution with automatic temporary audio cleanup.
- **Layer 2:** Silero VAD gating to drop silence and kill hallucinations.
- **Layer 3:** Automatic PipeWire audio ducking for music and browser streams.
- **Layer 4:** In-process Sherpa-ONNX CGO engine with live streaming token emission and hotword biasing.

---

## 5. Agent Persona (Aria) — Headless Sway Test Harness & UI Storybook

**Context:** Aria is an automated CI agent validating `mavor` inside a headless Linux container. There is no physical monitor, GPU hardware, or physical microphone.

**What happens:**

1. Aria runs the quality gate and headless integration test harness:
   ```bash
   just check-ci
   just storybook
   ```

2. The integration test suite executes:
   - Launches headless Sway (`WLR_BACKENDS=headless`, `WLR_RENDERER=pixman`) with virtual display output.
   - Spawns Waybar, PipeWire null sink, and virtual audio sources.
   - Drives the daemon through recording, volume changes (0% to 100%), transcribing, and error states.
   - Captures pixel-accurate `grim` screenshots and generates an HTML visual storybook report at `test/reports/ui-storybook.html` (produced by `just storybook`; not committed).

3. Aria verifies:
   - **Overlay Margin:** The layer-shell bar floats 8px below Waybar without overlapping or stealing input focus.
   - **Audio Meter States:** Visual waveform accurately transitions across 6 discrete energy levels.
   - **Output Verification:** `wl-paste` accurately matches the simulated audio transcription.

---

## Open Questions

1. ✅ **In-Process vs Sidecar Engine Architecture — RESOLVED (2026-08-16).**
   Should alternative backends (Sherpa-ONNX, warm server) run as separate external daemons or in-process?
   
   **Answer:**
   > Implemented as in-tree pluggable `Transcriber` engines (`whisper-cli`, `sherpa-onnx` CGO, and `server` socket) configured via `config.toml`. A single standalone binary handles both modes seamlessly.

2. ✅ **Audio Retention Policy — RESOLVED (2026-08-16).**
   Should `mavor` retain recording WAV files on disk?
   
   **Answer:**
   > Default to zero retention. Audio capture files in `/tmp/mavor-recordings/` are automatically unlinked upon transcription completion to eliminate privacy risks and disk leaks.

3. 💬 **Context-Aware Window Vocabulary Prompting.**
   How should `mavor` dynamically extract active window title / process context (e.g. Sway IPC active window) to bias Whisper `--prompt` and Sherpa hotwords without exceeding token budgets?

   _Leaning:_ Query Sway IPC for the focused application `app_id` / window title upon `EventStart`, extract domain identifiers, and pass up to 64 tokens of formatted prefix context to the active engine.

   **Answer:**
   > _(empty — fill in when decided)_
