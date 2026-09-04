---
title: "User Stories: Voice-to-Text Dictation on Linux (mavor)"
author: "Matthew Schulkind"
date: 2026-09-04
status: accepted
tags: [user-stories, personas, onboarding, install, wayland, dictation, models, cli]
summary: "Five concrete narrative user stories tracing new users from discovery and installation through everyday dictation and model switching on Wayland."
---

# User Stories: Voice-to-Text Dictation on Linux (`mavor`)

This document explores how new users discover, install, configure, dictate with, and switch models in `mavor`, a background voice-to-text dictation daemon for wlroots Wayland compositors (Sway, Hyprland, river, Wayfire, niri, labwc). It traces the end-to-end journey across five distinct personas—from an RSI-fatigued engineer and a minimal-dotfiles purist to a non-technical writer, an audio power user, and an automated headless CI agent.

---

## 1. Maya — Staff Engineer, Keyboard Strain & Low-Latency Coding Dictation

**Context:** Maya spends 8 hours a day in tmux across Sway workspaces running Neovim, shell sessions, and Claude Code. Developing bilateral wrist tendonitis forces her to minimize mechanical keystrokes. She wants an offline, local voice dictation tool for drafting code comments, git commit messages, and prompt instructions that runs natively on Wayland without stealing window focus or spinning up an Electron background application.

**First 10 minutes:**

1. Maya discovers `mavor` on an `r/swaywm` discussion comparing local Linux dictation tools. The project claims pure-Go simplicity, native `wlr-layer-shell` HUD overlays, and zero focus-stealing windows.

2. Maya installs the binary using Go:
   ```console
   $ go install github.com/mschulkind-oss/mavor/cmd/mavor@latest
   ```

3. Maya runs the automated first-run setup:
   ```console
   $ mavor setup
   mavor setup — automated first-run configuration & model install
   ================================================================
   ⚙️  Creating configuration file at ~/.config/mavor/config.toml...
   ✅ All required system runtime tools (parec, wtype, wl-copy) are available
   📥 Downloading default voice model "base.en" into ~/.cache/mavor/models...
   ✅ Downloaded and verified voice model "base.en"

   ⚙️  Setting up systemd user service...
   ✅ Created symlink ~/.config/systemd/user/graphical-session.target.wants/mavor.service → ~/.config/systemd/user/mavor.service.

   ================================================================
   🎉 Setup complete! mavor is configured and ready.
   ```

4. Maya verifies system health with `mavor doctor`:
   ```console
   $ mavor doctor
   mavor doctor — system and environment verification
   ==================================================
   ✅ Wayland session:             WAYLAND_DISPLAY=wayland-1
   ✅ Audio capture (parec/Pulse): parec available, PipeWire/PulseAudio server connected
   ✅ Virtual typing (wtype):      wtype installed at /bin/wtype
   ✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
   ✅ Speech engine:               whisper-cli installed at /bin/whisper-cli
   ✅ GPU acceleration:            CPU only (whisper-cli loaded no GPU backend)
   ✅ Configuration file:          valid config (mode=streaming, preset=balanced, model=base.en)
   ✅ Voice model availability:    whisper model found at ~/.cache/mavor/models/ggml-base.en.bin
   ✅ Daemon socket status:        daemon is active (state: idle)
   ✅ Systemd user service:        systemd unit installed and active (active)
   ==================================================
   ✅ All environment checks passed! mavor is ready.
   ```

**What happens:**

5. Maya binds push-to-talk in `~/.config/sway/config`:
   ```
   bindsym $mod+grave exec mavor start
   bindsym --release $mod+grave exec mavor stop
   ```
   She reloads Sway (`swaymsg reload`).

6. Inside a Neovim buffer, Maya holds down `$mod+grave`.
   A dark pill HUD overlay appears 8px below Waybar displaying a live animated volume waveform:
   ```
   [ ● RECORDING   ▂▃▅▆ ]
   ```

7. While holding the hotkey, Maya speaks:
   *"Implement exponential backoff retry policy for transient RPC timeouts."*

8. Maya releases `$mod+grave`. The HUD overlay crossfades to amber:
   ```
   [ ⟳ TRANSCRIBING... ]
   ```

9. Exactly 1.1 seconds later, `wtype` types the sentence directly into her Neovim cursor position and `wl-copy` updates the Wayland clipboard. The HUD smoothly disappears.

10. Maya pauses for 3 seconds holding `$mod+grave` without speaking, then releases.
    The integrated Silero Voice Activity Detector (VAD) evaluates the audio buffer, detects <250ms of vocal energy, and immediately returns to `idle` without typing hallucinated filler text like `"you"` or `"[BLANK_AUDIO]"`.

**Changing the model:**

11. While dictating technical Go comments, Maya notices that `base.en` occasionally transcribes domain terms inaccurately (e.g., typing `"g r p c"` instead of `"gRPC"` or dropping underscore casing).

12. She inspects the built-in model catalog:
    ```console
    $ mavor models list
    NAME                 ENGINE       SIZE  LANGUAGES            STREAM  STATUS         ALIASES
    tiny.en              whisper   74.1 MB  en                   no      –              whisper-tiny.en
    base.en              whisper  141.1 MB  en                   no      ✓ 141.1 MB  ★  whisper-base.en
    small.en             whisper  466.1 MB  en                   no      –              whisper-small.en
    large-v3-turbo       whisper   1.51 GB  multi (99)           no      –              whisper-large-v3-turbo
    parakeet             sherpa   429.4 MB  en                   yes     –              parakeet-tdt
    zipformer-streaming  sherpa   296.0 MB  en                   yes     –              zipformer

    ★ active   ✓ downloaded   – not downloaded
    ```

13. Maya decides to upgrade to `large-v3-turbo` for superior technical accuracy:
    ```console
    $ mavor models pull large-v3-turbo
    📥 Downloading Whisper GGML model "large-v3-turbo" (1.51 GB)...
    1.51 GB / 1.51 GB [======================================] 100% 42 MB/s
    ✅ Downloaded and verified ~/.cache/mavor/models/ggml-large-v3-turbo.bin
    ```

    **Gap:** After `mavor models pull` downloads the model, it does not prompt to activate it or update `config.toml`. Maya must manually remember which file to edit and how to restart the daemon.

14. Maya edits `~/.config/mavor/config.toml` to use the accurate preset:
    ```toml
    preset = "accurate"
    model = "large-v3-turbo"
    ```

15. Maya restarts the background service:
    ```console
    $ mavor service restart
    ✅ Restarted mavor.service
    ```

16. She returns to Neovim and dictates:
    *"Configure gRPC ClientConn with keepalive enforcement and TLS credentials."*
    The text is transcribed with 100% spelling and punctuation fidelity.

**What would trip them up:**
- Setting `model = "large-v3-turbo"` in `config.toml` while leaving `preset = "balanced"` untouched. Because presets provide smart defaults, users can be confused about whether `preset` or `model` takes precedence.
- Forgetting to reload the systemd service after editing `config.toml`, wondering why the new model hasn't taken effect.

**What makes this work:**
- The `wlr-layer-shell` HUD surface requests no keyboard input focus (`keyboard_interactivity = 0`), so Neovim never loses cursor state.
- The dual dispatch via `wtype` (synthetic keystrokes) and `wl-copy` (clipboard) ensures that if an esoteric terminal drops synthetic keystrokes, the full transcript is already in the paste buffer.

**The aha moment:**
- Seeing the HUD pill float unobtrusively below Waybar, watching the volume waveform mirror voice inflections, and having the transcribed sentence land inside the Neovim buffer without any window focus flickering.

---

## 2. Derek — Minimalist Sway User, The Single-Binary "Zero-Bloat" Workflow

**Context:** Derek runs an ultra-minimal Arch Linux installation on a ThinkPad X280 with 8 GB of RAM. He maintains pristine dotfiles, avoids systemd user services when a simple Sway `exec` statement suffices, and distrusts heavy runtimes (Python virtualenvs, Node, Electron). He wants a tiny dictation daemon that uses 0 MB of RAM while idle, starts from his Sway config, and stays out of the way.

**First 10 minutes:**

1. Derek finds `mavor` in a curated list of minimalist Wayland utilities. Seeing that the binary is statically compiled pure Go with zero shared-library baggage, he downloads the pre-built release tarball:
   ```console
   $ tar -xzf mavor_v0.1.0_linux_amd64.tar.gz
   $ install -m 0755 mavor ~/.local/bin/mavor
   ```

2. Derek avoids automated setup scripts that might touch system configuration. Instead, he inspects his system directly:
   ```console
   $ mavor doctor
   mavor doctor — system and environment verification
   ==================================================
   ✅ Wayland session:             WAYLAND_DISPLAY=wayland-1
   ✅ Audio capture (parec/Pulse): parec available, PipeWire/PulseAudio server connected
   ✅ Virtual typing (wtype):      wtype installed at /usr/bin/wtype
   ✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
   ✅ Speech engine:               whisper-cli installed at /usr/bin/whisper-cli
   ✅ GPU acceleration:            CPU only
   ❌ Configuration file:          config file missing at ~/.config/mavor/config.toml
   ❌ Voice model availability:    whisper model missing: ~/.cache/mavor/models/ggml-base.en.bin
   ❌ Daemon socket status:        daemon socket not responding at /run/user/1000/mavor.sock
   ❌ Systemd user service:        unit file not installed
   ==================================================
   ❌ 4 check(s) failed. Fix the issues above before running mavor.
   ```

3. Satisfied with the clear diagnostics, Derek initializes only the configuration file:
   ```console
   $ mavor config init
   ✅ Initialized configuration at ~/.config/mavor/config.toml
   ```

4. He pulls the smallest Whisper model available to keep disk usage under 100 MB:
   ```console
   $ mavor models pull tiny.en
   📥 Downloading Whisper GGML model "tiny.en" (74.1 MB)...
   74.1 MB / 74.1 MB [====================================] 100% 38 MB/s
   ✅ Downloaded and verified ~/.cache/mavor/models/ggml-tiny.en.bin
   ```

**The minimal workflow:**

5. Derek configures toggle mode in `~/.config/sway/config`:
   ```
   exec mavor daemon
   bindsym $mod+grave exec mavor toggle
   ```

6. Derek reloads Sway. `mavor daemon` launches in the background.
   Derek checks process memory with `ps -o rss,comm -p $(pgrep mavor)`:
   The daemon consumes only **11.4 MB RSS**. Because `engine = "cli"` uses an external `whisper-cli` process that only spawns during transcription, idle memory stays essentially zero.

7. Derek hits `$mod+grave` once. The HUD pill turns blue:
   ```
   [ ● RECORDING   ▂▃▅▆ ]
   ```
   He speaks: *"Fix memory leak in network socket poll loop."*
   He hits `$mod+grave` again. The HUD flashes amber, and within 350 ms, the text is typed directly into his terminal.

**Changing the model:**

8. While testing dictation on battery power, Derek tests whether `tiny.en` meets his accuracy threshold. It transcribes short commands instantly (real-time factor ~0.06), but occasionally omits plurals or punctuation.

9. Derek decides to test `base.en` (141.1 MB) to see if the accuracy gain is worth the extra 70 MB of disk space.
   ```console
   $ mavor models pull base.en
   📥 Downloading Whisper GGML model "base.en" (141.1 MB)...
   ✅ Downloaded and verified ~/.cache/mavor/models/ggml-base.en.bin
   ```

10. Derek edits `~/.config/mavor/config.toml`:
    ```toml
    model = "base.en"
    preset = "balanced"
    ```

    **Gap:** Because Derek runs `mavor daemon` via Sway `exec` instead of systemd, there is no `mavor daemon reload` or IPC reload signal (`SIGHUP`). Derek has to find the process ID or run `pkill -f 'mavor daemon'` and restart it manually.

11. Derek reloads the daemon:
    ```console
    $ pkill -f 'mavor daemon' && mavor daemon &
    $ mavor status
    idle
    ```

12. He dictates the same sentence again. Accuracy is noticeably improved, and decode latency remains under 900 ms on his CPU.

**What would trip them up:**
- Starting `mavor daemon` from `.xprofile` or a tty login script before `WAYLAND_DISPLAY` or `XDG_RUNTIME_DIR` is exported into the environment. The daemon exits immediately when it cannot find a Wayland display socket.
- Wondering where daemon logs go when launched via Sway `exec`. Unless `--log-file` is specified, logs default to `~/.local/state/mavor/daemon.log` or stderr.

**What makes this work:**
- The pure-Go binary needs no shared C runtime or GPU libraries.
- The `cli` engine architecture frees 100% of model memory immediately after transcription finishes.

---

## 3. Lisa — Technical Writer, Hands-Free Long-Form Dictation on Fedora

**Context:** Lisa writes end-user documentation, tutorials, and release notes in Markdown using Obsidian and ghostty on a Fedora laptop running Sway. She does not write code, does not use compilers, and prefers one-shot tooling that handles configuration automatically. She needs reliable speech transcription that handles long-form paragraphs, punctuation, and multilingual terminology without requiring complex terminal setup.

**First 10 minutes:**

1. Lisa discovers `mavor` from an accessibility blog post titled *"Native Voice Dictation on Linux Desktops"*.

2. She downloads the Fedora package or binary tarball and places it in `~/.local/bin/`.

3. Lisa runs the automated setup wizard:
   ```console
   $ mavor setup
   mavor setup — automated first-run configuration & model install
   ================================================================
   ⚙️  Creating configuration file at ~/.config/mavor/config.toml...
   ✅ All required system runtime tools (parec, wtype, wl-copy) are available
   📥 Downloading default voice model "base.en" into ~/.cache/mavor/models...
   ✅ Downloaded and verified voice model "base.en"
   ⚙️  Setting up systemd user service...
   ✅ Created symlink ~/.config/systemd/user/graphical-session.target.wants/mavor.service → ~/.config/systemd/user/mavor.service.
   ================================================================
   🎉 Setup complete! mavor is configured and ready.
   ```

4. Lisa starts the background service immediately:
   ```console
   $ mavor service start
   ✅ Started mavor.service
   ```

5. She runs `mavor doctor` to ensure everything is functional:
   ```console
   $ mavor doctor
   mavor doctor — system and environment verification
   ==================================================
   ✅ Wayland session:             WAYLAND_DISPLAY=wayland-1
   ✅ Audio capture (parec/Pulse): parec available, PipeWire/PulseAudio server connected
   ✅ Virtual typing (wtype):      wtype installed at /usr/bin/wtype
   ✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
   ✅ Speech engine:               whisper-cli installed at /usr/bin/whisper-cli
   ✅ GPU acceleration:            CPU only
   ✅ Configuration file:          valid config (mode=streaming, preset=balanced, model=base.en)
   ✅ Voice model availability:    whisper model found at ~/.cache/mavor/models/ggml-base.en.bin
   ✅ Daemon socket status:        daemon is active (state: idle)
   ✅ Systemd user service:        systemd unit installed and active (active)
   ==================================================
   ✅ All environment checks passed! mavor is ready.
   ```

**What happens:**

6. Lisa adds a single hotkey to `~/.config/sway/config`:
   ```
   bindsym $mod+d exec mavor toggle
   ```
   She reloads Sway (`swaymsg reload`).

7. Lisa opens Obsidian, presses `$mod+d`, and dictates continuously for 25 seconds:
   *"Chapter four covers user authorization. When an unauthenticated user attempts to access the billing dashboard, the system redirects them to the identity provider and preserves the return URL as a query parameter."*

8. Lisa presses `$mod+d` again to stop.
   The HUD flips to `[ ⟳ TRANSCRIBING... ]`. In 1.8 seconds, Whisper completes transcription and types the text into Obsidian with accurate punctuation, capitalization, and sentence spacing.

9. While dictating later in the day, Lisa accidentally unplugs her USB microphone headset:
   The daemon detects the broken PipeWire stream, catches the error, and briefly shows a red error state on the HUD:
   ```
   [ ✕ AUDIO CAPTURE FAILED ]
   ```
   After 1.5 seconds, the HUD smoothly disappears and the daemon resets to `idle` without freezing or crashing.

**Changing the model:**

10. Lisa begins writing international documentation referencing German and French technical standards (*"DIN EN ISO 9001"*, *"Prüfung"*, *"Société"*).

11. She tries dictating with `base.en`, but the English-only acoustic model mangles foreign loanwords into nonsensical English phonetic approximations.

12. Lisa searches the model catalog for multilingual options:
    ```console
    $ mavor models list
    NAME                 ENGINE       SIZE  LANGUAGES            STREAM  STATUS         ALIASES
    tiny                 whisper   74.1 MB  multi (99)           no      –              whisper-tiny
    base.en              whisper  141.1 MB  en                   no      ✓ 141.1 MB  ★  whisper-base.en
    large-v3-turbo       whisper   1.51 GB  multi (99)           no      –              whisper-large-v3-turbo
    sensevoice-small     sherpa   999.3 MB  zh, en, ja, ko, yue  no      –              sensevoice
    ```

13. Seeing that `large-v3-turbo` supports 99 languages, Lisa pulls it:
    ```console
    $ mavor models pull large-v3-turbo
    📥 Downloading Whisper GGML model "large-v3-turbo" (1.51 GB)...
    1.51 GB / 1.51 GB [======================================] 100% 35 MB/s
    ✅ Downloaded and verified ~/.cache/mavor/models/ggml-large-v3-turbo.bin
    ```

14. Lisa opens `~/.config/mavor/config.toml` in her text editor and changes:
    ```toml
    preset = "accurate"
    model = "large-v3-turbo"
    ```

15. She restarts the background service:
    ```console
    $ mavor service restart
    ✅ Restarted mavor.service
    ```

    **Gap:** Lisa wants to verify which model is *currently running* inside the live daemon process, but `mavor status` only returns `"idle"`. She has to run `mavor doctor` or inspect `~/.local/state/mavor/daemon.log` to confirm that the daemon picked up the new model.

16. Lisa dictates a paragraph containing German and French phrases. The multilingual Whisper model transcribes the diacritics and foreign nouns accurately.

**What would trip them up:**
- On an immutable Linux distribution (like Fedora Silverblue), `mavor setup` may fail to install missing packages via `dnf` because `rpm-ostree` requires layering and rebooting. Clear manual package advice is needed.
- Leaving dictation running in a noisy environment: without push-to-talk, ambient coffee shop chatter can append stray words to the document.

**What makes this work:**
- One-shot `mavor setup` configures the config file, default model, and systemd service without requiring manual command chaining.
- Whisper's natural language processing handles capitalization, commas, and periods automatically without requiring spoken syntax like *"insert comma"*.

---

## 4. Sam — Audio Power User, Sub-100ms In-Process Streaming & PipeWire Ducking

**Context:** Sam is a DevOps engineer and audio enthusiast on Arch Linux running Hyprland with an XLR studio microphone, PipeWire, and continuous background music playback in Spotify. Sam hates waiting for batch transcription after speaking: waiting 2 seconds breaks their train of thought. Sam wants live, real-time token streaming as they speak, custom hotword boosting for Kubernetes terminology, and automatic media ducking so music doesn't bleed into the microphone.

**First 10 minutes:**

1. Sam finds `mavor` while researching `sherpa-onnx` integrations for Linux. Sam discovers that `mavor` includes an optional in-process CGO backend supporting NVIDIA FastConformer (Parakeet-TDT) with 80ms causal chunk streaming.

2. Because pre-built releases are pure Go, Sam builds the native CGO binary from source:
   ```console
   $ git clone https://github.com/mschulkind-oss/mavor && cd mavor
   $ mise install
   $ just build-sherpa
   $ just deploy
   ```

3. Sam runs `mavor doctor` to verify the CGO build and audio stack:
   ```console
   $ mavor doctor
   mavor doctor — system and environment verification
   ==================================================
   ✅ Wayland session:             WAYLAND_DISPLAY=wayland-1
   ✅ Audio capture (parec/Pulse): parec available, PipeWire/PulseAudio server connected
   ✅ Virtual typing (wtype):      wtype installed at /usr/bin/wtype
   ✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
   ✅ Speech engine:               sherpa-onnx (in-process CGO) enabled
   ✅ GPU acceleration:            CPU (ONNX Runtime CPU provider)
   ✅ Configuration file:          valid config (mode=streaming, preset=balanced, model=base.en)
   ✅ Voice model availability:    whisper model found at ~/.cache/mavor/models/ggml-base.en.bin
   ✅ Daemon socket status:        daemon is active (state: idle)
   ✅ Systemd user service:        systemd unit installed and active (active)
   ==================================================
   ✅ All environment checks passed! mavor is ready.
   ```

**What happens:**

4. Sam configures PipeWire audio ducking in `~/.config/mavor/config.toml`:
   ```toml
   duck_audio = true
   duck_volume = "15%"
   duck_streams = ["spotify", "firefox", "chromium"]
   ```

5. Sam starts Spotify playing music at 100% volume.
   Sam presses `$mod+grave` and begins speaking into the microphone:
   - PipeWire instantly ducks the Spotify playback stream to 15% volume via `pactl`.
   - Voice calls in Discord remain untouched because only streams matching `duck_streams` are attenuated.
   - The HUD overlay shows the microphone's live vocal energy level.
   - Upon releasing `$mod+grave`, Spotify volume smoothly restores to 100%.

**Changing the model to In-Process Streaming (Parakeet-TDT):**

6. While audio ducking works smoothly, Sam is still using `engine = "cli"` (batch Whisper), which only transcribes *after* the key is released. Sam wants sub-100ms live streaming tokens.

7. Sam inspects the verbose model specifications:
   ```console
   $ mavor models list --verbose
   parakeet
     NeMo FastConformer transducer, 80ms chunk — decodes while you speak
     engine      sherpa (in-process sherpa-onnx, CGO)
     download    429.4 MB
     languages   en
     streaming   yes — decodes incrementally while you speak
     speed       fast
     vocabulary  hotwords via sherpa_hotwords_file
     gpu         none in practice — bundled ONNX Runtime is CPU-only
     aliases     parakeet-tdt
     status      – not downloaded
     source      https://github.com/k2-fsa/sherpa-onnx/releases/...
   ```

8. Sam downloads the Parakeet transducer model:
   ```console
   $ mavor models pull parakeet
   📥 Downloading Sherpa-ONNX model archive "parakeet" (429.4 MB)...
   429.4 MB / 429.4 MB [==================================] 100% 45 MB/s
   📦 Extracting archive into ~/.cache/mavor/models/sherpa/parakeet...
   ✅ Verified model assets (encoder.onnx, decoder.onnx, joiner.onnx, tokens.txt)
   ```

9. Sam creates a custom hotwords vocabulary file at `~/.config/mavor/hotwords.txt`:
   ```
   Kubernetes
   kubectl
   ConfigMap
   StatefulSet
   WireGuard
   Prometheus
   ```

10. Sam updates `~/.config/mavor/config.toml`:
    ```toml
    engine = "sherpa"
    sherpa_model = "parakeet"
    mode = "streaming"
    streaming_strategy = "transducer"
    sherpa_hotwords_file = "~/.config/mavor/hotwords.txt"
    sherpa_hotwords_score = 2.0
    ```

11. Sam restarts the daemon:
    ```console
    $ mavor service restart
    ✅ Restarted mavor.service
    ```

12. Sam holds `$mod+grave` and dictates:
    *"Drain node worker-three and apply the Prometheus StatefulSet ConfigMap."*
    - Words decode incrementally every 80 ms as Sam speaks, appearing in the HUD overlay subtitle.
    - Specialized jargon (`StatefulSet`, `ConfigMap`) is boosted by the transducer beam search and transcribed perfectly.
    - The instant Sam releases the hotkey, the text is already completely typed into the terminal. Post-speech wait latency is **less than 60 milliseconds**.

    **Gap:** If a user downloads pre-built release binaries (pure Go) and attempts to set `engine = "sherpa"`, the daemon will fail at startup explaining that `sherpa` requires CGO. `mavor models pull parakeet` could warn upfront if the active binary lacks the `sherpa` build tag.

**What would trip them up:**
- Attempting to use `sherpa_hotwords_file` with Whisper or CTC models. Vocabulary boosting via shallow fusion is an architectural feature of Transducer models (Parakeet-TDT, Zipformer) and has no effect on Whisper.
- Memory usage: Unlike the CLI engine which exits after transcription, `engine = "sherpa"` keeps model weights resident in RAM (~220 MB for Parakeet-TDT).

**What makes this work:**
- The in-process ONNX Runtime eliminates process spawning latency.
- FastConformer transducer decodes 80ms audio chunks in real time, shifting the compute burden into the speaking window rather than after it.

---

## 5. Aria — Automated CI / Ephemeral Dev Container Agent

**Context:** Aria is an autonomous coding and testing agent operating inside a headless Docker / NixOS jail container. Aria has no physical monitor, no interactive terminal user, and no physical microphone. Aria needs to install `mavor`, scaffold configuration, run diagnostic checks, pull a lightweight model for end-to-end testing, simulate audio capture via PipeWire virtual sources, and verify transcription output programmatically.

**First 10 minutes:**

1. Aria inspects the repository layout and development instructions in `AGENTS.md`.

2. Aria builds the static binary:
   ```console
   $ just build
   ```

3. Aria provisions the environment non-interactively:
   ```console
   $ ./bin/mavor setup --force
   mavor setup — automated first-run configuration & model install
   ================================================================
   ⚙️  Creating configuration file at ~/.config/mavor/config.toml...
   ✅ All required system runtime tools (parec, wtype, wl-copy) are available
   📥 Downloading default voice model "base.en" into ~/.cache/mavor/models...
   ✅ Downloaded and verified voice model "base.en"
   ================================================================
   🎉 Setup complete! mavor is configured and ready.
   ```

4. Aria executes `mavor doctor` and asserts a clean exit code (0):
   ```console
   $ ./bin/mavor doctor
   mavor doctor — system and environment verification
   ==================================================
   ✅ Wayland session:             WAYLAND_DISPLAY=wayland-1
   ✅ Audio capture (parec/Pulse): parec available, PipeWire/PulseAudio server connected
   ✅ Virtual typing (wtype):      wtype installed at /bin/wtype
   ✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
   ✅ Speech engine:               whisper-cli installed at /bin/whisper-cli
   ✅ GPU acceleration:            CPU only
   ✅ Configuration file:          valid config (mode=streaming, preset=balanced, model=base.en)
   ✅ Voice model availability:    whisper model found at ~/.cache/mavor/models/ggml-base.en.bin
   ✅ Daemon socket status:        daemon socket not responding at /run/user/1000/mavor.sock
   ==================================================
   ❌ 1 check(s) failed. Fix the issues above before running mavor.
   ```
   Aria correctly observes that the daemon socket check fails because the daemon process has not yet been started.

**What happens:**

5. Aria launches the daemon in the background with explicit file logging:
   ```console
   $ ./bin/mavor daemon --log-file /tmp/mavor.log &
   ```

6. Aria polls `mavor status` until the socket responds:
   ```console
   $ ./bin/mavor status
   idle
   ```

7. Aria simulates a dictation cycle using a PipeWire virtual audio source and IPC commands:
   ```console
   $ ./bin/mavor start
   recording
   $ # Virtual source plays test audio: "Continuous integration test passing."
   $ ./bin/mavor stop
   transcribing
   ```

8. Aria verifies the transcript from the append-only JSONL recovery log:
   ```console
   $ ./bin/mavor history -n 1 --json
   {"timestamp":"2026-09-04T15:48:12Z","text":"Continuous integration test passing.","duration_ms":1120,"model":"base.en"}
   ```

**Changing the model for CI optimization:**

9. In ephemeral CI environments, network bandwidth and test execution time are strictly capped. Downloading the 141.1 MB `base.en` model on every test matrix run consumes unnecessary time.

10. Aria switches the test matrix to use `tiny.en` (74.1 MB):
    ```console
    $ ./bin/mavor models pull tiny.en
    📥 Downloading Whisper GGML model "tiny.en" (74.1 MB)...
    ✅ Downloaded and verified ~/.cache/mavor/models/ggml-tiny.en.bin
    ```

11. Aria updates the configuration file using `sed`:
    ```console
    $ sed -i 's/preset = "balanced"/preset = "fast"/' ~/.config/mavor/config.toml
    $ sed -i 's/model = "base.en"/model = "tiny.en"/' ~/.config/mavor/config.toml
    $ ./bin/mavor config show
    top_margin = 8
    engine = "cli"
    model = "tiny.en"
    preset = "fast"
    mode = "streaming"
    duck_audio = false
    ```

    **Gap:** Updating configuration in scripts requires manual `sed` commands or file overwrites. A dedicated `mavor config set <key> <val>` command would eliminate parsing fragility in automated scripts.

12. Aria terminates and restarts the background daemon:
    ```console
    $ kill %1
    $ ./bin/mavor daemon --log-file /tmp/mavor.log &
    $ ./bin/mavor status
    idle
    ```

13. Aria reruns the synthetic audio dictation test. Transcription latency drops from 1.2s to 190ms, speeding up the CI pipeline by over 80%.

**What would trip them up:**
- Running in headless containers without a virtual Wayland socket: if `WAYLAND_DISPLAY` is missing and cannot be globbed in `XDG_RUNTIME_DIR`, the daemon fails during startup.
- Parsing human-readable tables: if commands like `mavor doctor` or `mavor models list` do not support `--json`, agents must rely on brittle regex matching.

**What makes this work:**
- The JSON-over-Unix-socket IPC protocol returns clean single-token state responses (`idle`, `recording`, `transcribing`) that can be asserted directly in scripts.
- The `history --json` recovery log provides an authoritative, structured record of completed dictations independent of GUI keystroke capture.

---

## Technical Architecture Reference

### Core CLI Subcommand Matrix

| Subcommand | Invocation | Primary Purpose | Key Flags |
|---|---|---|---|
| `setup` | `mavor setup` | One-shot initialization: scaffolds config, checks tools, pulls default model | `--force` (overwrite existing) |
| `doctor` | `mavor doctor` | Comprehensive environment & dependency health check | `--fix` (trigger setup workflow) |
| `daemon` | `mavor daemon` | Long-lived daemon handling audio, overlay, IPC, and inference | `--verbose`, `--log-file PATH` |
| `start` | `mavor start` | Signal push-to-talk press (enter `recording`) | None |
| `stop` | `mavor stop` | Signal push-to-talk release (enter `transcribing` then `idle`) | None |
| `toggle` | `mavor toggle` | Toggle between `idle` and `recording` | None |
| `status` | `mavor status` | Output current daemon state (`idle`, `recording`, `transcribing`) | None |
| `config` | `mavor config <action>` | Scaffold or inspect configuration | `init [--force]`, `show`, `path` |
| `models` | `mavor models <action>` | Download and inspect voice model catalog | `list [--installed] [--verbose]`, `pull <name>` |
| `service` | `mavor service <action>` | Manage systemd user unit | `install [--start]`, `status`, `restart`, `stop` |
| `history` | `mavor history` | Inspect or recover recent transcripts | `-n N`, `--json`, `--copy` |

---

## Open Questions

1. 💬 **Dynamic Model Reloading via IPC.**
   Currently, changing `model` or `preset` in `config.toml` requires restarting the daemon process (`mavor service restart` or `pkill -f 'mavor daemon'`). Could the daemon support a `reload` IPC action (e.g. `mavor reload` or `mavor config reload`) that hot-reloads the active model without interrupting the socket connection?

   _Leaning:_ Add an IPC `reload` action that triggers `config.Load("")` and rebuilds the `speech.Transcriber` instance if the daemon is currently `idle`. If the daemon is `recording` or `transcribing`, queue or reject the reload until the active phrase finishes.

   **Answer:**
   > _(empty — fill in when decided)_

2. 💬 **CLI Machine-Readable Output (`--json`).**
   While `mavor history` provides a `--json` flag, commands like `mavor doctor`, `mavor models list`, and `mavor status` only output human-readable ANSI tables or plain text. Should all diagnostic and inspection commands support `--json` for automated script and agent consumption?

   _Leaning:_ Add `--json` across `mavor doctor`, `mavor models list`, and `mavor config show`. Output structured JSON schemas matching existing CLI records.

   **Answer:**
   > _(empty — fill in when decided)_

3. 💬 🤷 **Model Download Confirmation for Large Artifacts.**
   Models like `large-v3-turbo` (1.51 GB) and `large-v3` (3.1 GB) are substantially larger than `base.en` (141 MB) or `tiny.en` (74 MB). Should `mavor models pull` prompt for confirmation when downloading artifacts > 500 MB if attached to an interactive TTY?

   _Leaning:_ Provide an interactive `Download 1.51 GB? [y/N]` confirmation when `os.Stdin` is a TTY, skippable with a `-y` / `--yes` flag for scripted invocations.

   **Answer:**
   > _(empty — fill in when decided)_

4. ✅ **In-Process vs Sidecar Engine Architecture — RESOLVED (2026-08-16).**
   Should alternative backends (Sherpa-ONNX, warm server) run as separate external daemons or in-process?

   **Answer:**
   > Implemented as in-tree pluggable `Transcriber` engines (`whisper-cli`, `sherpa-onnx` CGO, and `server` socket) configured via `config.toml`. A single standalone binary handles both modes seamlessly.

5. ✅ **Audio Retention Policy — RESOLVED (2026-08-16).**
   Should `mavor` retain recording WAV files on disk?

   **Answer:**
   > Default to zero retention. Audio capture files in `/tmp/mavor-recordings/` are automatically unlinked upon transcription completion to eliminate privacy risks and disk leaks.
