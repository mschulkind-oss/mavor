---
title: "User Stories: Voice-to-Text Dictation on Linux (mavor)"
author: "Matthew Schulkind"
date: 2026-09-05
status: in-review
tags: [user-stories, personas, onboarding, install, wayland, dictation, models, preview, vocabulary, cli]
summary: "Five concrete narrative user stories tracing new users from discovery and installation through everyday dictation, the live preview, vocabulary biasing, and model switching on Wayland."
---

# User Stories: Voice-to-Text Dictation on Linux (`mavor`)

> [!NOTE]
> This is a planning document, not a description of the built system. The
> stories walk workflows as they are meant to feel, and the inline **Gap:**
> notes name what is missing — read them as intent, not as behaviour that
> exists. A **Closed:** note is the opposite: a gap an earlier draft claimed
> that has since been built, kept here rather than deleted so the story still
> reads as a story. [`../reference/how-mavor-works.md`](../reference/how-mavor-works.md)
> is the account of what mavor actually does today, and
> [`../user-guide.md`](../user-guide.md) is the manual these stories are
> written against.

> [!IMPORTANT]
> **Every config snippet below is the rewritten schema.** One top-level `model`
> key plus six tables replaced twenty-nine flat keys, and there are no
> compatibility aliases — `preset`, `engine`, `mode`, `streaming_strategy`,
> `sherpa_model`, `gpu_layers` and `duck_audio` do not exist and are not
> silently translated. Model names carry their family as a prefix, so `base.en`
> is `whisper-base.en` and the old `parakeet` is `fastconformer-streaming`. The
> reasoning is in
> [`../design/configuration-surface.md`](../design/configuration-surface.md);
> the annotated file is
> [§7 of the user guide](../user-guide.md#7-configuration-reference).

This document explores how new users discover, install, configure, dictate with, and switch models in `mavor`, a background voice-to-text dictation daemon for wlroots Wayland compositors (Sway, Hyprland, river, Wayfire, niri, labwc). It traces the end-to-end journey across five distinct personas—from an RSI-fatigued engineer and a minimal-dotfiles purist to a non-technical writer, an audio power user, and an automated headless CI agent.

Two terms recur, and both are defined in
[§2 of the user guide](../user-guide.md#2-how-a-model-runs-runtime-and-placement):

- **Runtime** — the inference library that executes a model. There are exactly
  two, whisper.cpp and ONNX Runtime reached through sherpa-onnx, and a runtime
  is never configured: it is a property of the model, recorded in the catalog.
- **Placement** — where that runtime executes relative to the daemon process
  (`in-process`, `local-server`, `subprocess`, `remote`). mavor derives it from
  the model; `advanced.placement` accepts only `"auto"` and `"subprocess"`.

---

## 1. Maya — Staff Engineer, Keyboard Strain & Low-Latency Coding Dictation

**Context:** Maya spends 8 hours a day in tmux across Sway workspaces running Neovim, shell sessions, and Claude Code. Developing bilateral wrist tendonitis forces her to minimize mechanical keystrokes. She wants an offline, local voice dictation tool for drafting code comments, git commit messages, and prompt instructions that runs natively on Wayland without stealing window focus or spinning up an Electron background application.

**First 10 minutes:**

1. Maya discovers `mavor` on an `r/swaywm` discussion comparing local Linux dictation tools. The project claims a single self-contained daemon, native `wlr-layer-shell` HUD overlays, and zero focus-stealing windows.

2. Maya builds and installs from source. `mavor` links the sherpa-onnx
   recognizers through cgo, so `go install` of a bare binary is not the install
   path — the two shared objects have to land beside it:
   ```console
   $ git clone https://github.com/mschulkind-oss/mavor && cd mavor
   $ mise install
   $ just install   # binary to ~/.local/bin, shared objects to ~/.local/lib
   ```

3. Maya runs the automated first-run setup. It is config-driven: it pulls every
   model the config names, which on a fresh install is the default model *and*
   the preview companion:
   ```console
   $ mavor setup
   mavor setup — automated first-run configuration & model install
   ================================================================
   ⚙️  Creating configuration file at ~/.config/mavor/config.toml...
   ✅ All required system runtime tools (parec, wtype, wl-copy) are available
   📥 Downloading model "whisper-base.en" into ~/.cache/mavor/models...
   ✅ Downloaded and verified model "whisper-base.en"
   📥 Downloading model "zipformer-streaming-20m" into ~/.cache/mavor/models...
   ✅ Downloaded and verified model "zipformer-streaming-20m"

   ⚙️  Setting up systemd user service...
   ✅ Created symlink ~/.config/systemd/user/graphical-session.target.wants/mavor.service → ~/.config/systemd/user/mavor.service.

   ================================================================
   🎉 Setup complete! mavor is configured and ready.
   ```

4. Maya verifies system health with `mavor doctor`. Five of these lines are
   *derived* facts — what this machine will do with her config, not what her
   config says:
   ```console
   $ mavor doctor
   mavor doctor — system and environment verification
   ==================================================
   ✅ Wayland session:             WAYLAND_DISPLAY=wayland-1
   ✅ Audio capture (parec/Pulse): parec available, PipeWire/PulseAudio server connected
   ✅ Virtual typing (wtype):      wtype installed at /bin/wtype
   ✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
   ✅ Runtime and placement:       whisper.cpp, local-server — whisper models default to a supervised warm whisper-server
   ✅ Inference threads:           6 (this machine's physical core count; 12 logical)
   ✅ GPU acceleration:            CPU only (whisper-cli loaded no GPU backend — the stock build ships CPU backends only; install a whisper.cpp built with -DGGML_VULKAN=ON for acceleration)
   ✅ Configuration file:          valid config (model=whisper-base.en, preview=auto)
   ✅ Voice model availability:    whisper-base.en found at ~/.cache/mavor/models/ggml-base.en.bin
   ✅ Live preview source:         companion (zipformer-streaming-20m) — "whisper-base.en" does not decode incrementally, so the streaming companion "zipformer-streaming-20m" runs alongside it
   ✅ Vocabulary biasing:          no [vocabulary] configured — nothing is biased
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
   Words appear in the overlay as she says them — the `zipformer-streaming-20m`
   companion decoding the same audio alongside `whisper-base.en`. **None of that
   text is typed.**

8. Maya releases `$mod+grave`. The HUD overlay crossfades to amber:
   ```
   [ ⟳ TRANSCRIBING... ]
   ```

9. Exactly 1.1 seconds later, `wtype` types the sentence — `whisper-base.en`'s transcript, produced once, not the preview's — directly into her Neovim cursor position, and `wl-copy` updates the Wayland clipboard. The HUD smoothly disappears.

    **Closed:** an earlier draft of this story listed "no words until you stop
    speaking" as a gap that could only be fixed by making a streaming model the
    model you keep. The preview companion closed it without that trade: a small
    streaming recognizer paints the overlay while the accurate batch model still
    produces the text. See
    [§7.2 of the user guide](../user-guide.md#72-preview--text-in-the-overlay-while-you-speak).

10. Maya pauses for 3 seconds holding `$mod+grave` without speaking, then releases.
    The energy-threshold voice-activity check evaluates the captured WAV, finds less than 150 ms of frames above the RMS threshold, and returns to `idle` without typing hallucinated filler text like `"you"` or `"[BLANK_AUDIO]"`.

    **Gap:** The gate is amplitude, not speech — it cannot distinguish quiet dictation from a noisy room, so a soft speaker near a fan gets both failure modes: real speech dropped, and hallucinations let through.

**Teaching it her vocabulary:**

11. While dictating technical Go comments, Maya notices that `whisper-base.en` occasionally transcribes domain terms inaccurately (e.g., typing `"g r p c"` instead of `"gRPC"` or dropping underscore casing).

12. She inspects the built-in model catalog, looking for something more accurate:
    ```console
    $ mavor models list
    Model cache: ~/.cache/mavor/models

    NAME                     ENGINE       SIZE  LANGUAGES            STREAM  STATUS
    whisper-tiny.en          whisper   74.1 MB  en                   no      –
    whisper-base.en          whisper  141.1 MB  en                   no      ✓ 141.1 MB  ★
    whisper-small.en         whisper  465.0 MB  en                   no      –
    whisper-large-v3-turbo   whisper   1.51 GB  multi (99)           no      –
    fastconformer-streaming  sherpa   429.4 MB  en                   yes     –
    zipformer-streaming-20m  sherpa   122.0 MB  en                   yes     ✓ 130.1 MB

    ★ active   ✓ downloaded   – not downloaded
    SIZE is the download; sherpa archives expand to roughly twice that on disk.
    Download one with `mavor models pull <name>`.
    ```
    (Abridged — the catalog is 26 models.)

13. Her first instinct is the biggest model, and that instinct is wrong:

    > [!WARNING]
    > `whisper-large-v3-turbo` returns **unpunctuated lowercase text**. Measured
    > on the same clips, it emits `lux is in the pit he cannot sit still` where
    > `whisper-base.en` emits `Lux is in the pit. He cannot sit still.` — for
    > 1.51 GB of download and 21.01 s per 20 s of speech on CPU against
    > `whisper-base.en`'s 1.63 s. Word error rate is the same; the output is
    > not. [`../choosing-a-model.md`](../choosing-a-model.md#do-not-reach-for-the-biggest-model)
    > has the numbers.

14. The right fix is not a bigger model, it is telling the model her words. Maya adds a `[vocabulary]` table to `~/.config/mavor/config.toml`:
    ```toml
    model = "whisper-base.en"

    [vocabulary]
    words = ["gRPC", "ClientConn", "keepalive", "mTLS", "goroutine"]
    file = "~/.config/mavor/vocabulary.txt"
    ```

    **Closed:** this used to be a gap — jargon could only be corrected by
    upgrading the model, and the one biasing mechanism mavor exposed
    (`sherpa_hotwords_file`) did nothing at all on a whisper model. One
    runtime-neutral `[vocabulary]` table now reaches every model that can be
    biased: whisper takes it as an initial prompt, a transducer takes it as a
    hotwords file, and `doctor` says which.

15. Maya restarts the background service and checks what the table actually became:
    ```console
    $ mavor service restart
    ✅ Restarted mavor.service
    $ mavor doctor | grep Vocabulary
    ✅ Vocabulary biasing:          5 phrase(s) → whisper initial prompt (--prompt)
    ```

16. She returns to Neovim and dictates:
    *"Configure gRPC ClientConn with keepalive enforcement and TLS credentials."*
    `gRPC` and `ClientConn` land with their casing intact.

    **Gap:** `mavor models pull` still does not offer to activate what it just
    downloaded or to write the name into `config.toml`. The flow is inverted
    instead — edit `model`, then run `mavor setup`, which pulls whatever the
    file now names and skips what is present — but nothing tells a user that
    from inside `models pull`.

**What would trip them up:**
- Writing a pre-rewrite model name. `base.en`, `tiny` and `parakeet` do not
  resolve, there are no aliases, and the daemon **refuses to start** rather than
  guessing — naming the closest catalog entries in the error.
- Carrying an old `config.toml` forward. It parses, contributes nothing, and
  every default applies. `mavor doctor` reports a file whose every key is
  unknown and points at `mavor config init --force`.
- Forgetting to restart the service after editing `config.toml`. Configuration
  is read once, at daemon start; there is no hot reload.

**What makes this work:**
- The `wlr-layer-shell` HUD surface requests no keyboard input focus (`keyboard_interactivity = 0`), so Neovim never loses cursor state.
- The dual dispatch via `wtype` (synthetic keystrokes) and `wl-copy` (clipboard) ensures that if an esoteric terminal drops synthetic keystrokes, the full transcript is already in the paste buffer.
- Nothing in step 14 named a runtime, a decoding method or a file format. The
  model decides the runtime; mavor decides how the vocabulary reaches it.

**The aha moment:**
- Seeing the HUD pill float unobtrusively below Waybar, watching her own words appear in it as she speaks, and having the *final* sentence land inside the Neovim buffer without any window focus flickering.

---

## 2. Derek — Minimalist Sway User, The Single-Binary "Zero-Bloat" Workflow

**Context:** Derek runs an ultra-minimal Arch Linux installation on a ThinkPad X280 with 8 GB of RAM. He maintains pristine dotfiles, avoids systemd user services when a simple Sway `exec` statement suffices, and distrusts heavy runtimes (Python virtualenvs, Node, Electron). He wants a tiny dictation daemon that uses almost no RAM while idle, starts from his Sway config, and stays out of the way.

**First 10 minutes:**

1. Derek finds `mavor` in a curated list of minimalist Wayland utilities. Seeing one daemon with no Python, Node or Electron anywhere near it, he downloads the pre-built release tarball. A release is a directory, not a file: the binary plus the two sherpa-onnx shared objects it links against, which have to travel together. `linux/amd64` only for now — mavor is a cgo program, so a release for another architecture needs a cross toolchain rather than a `GOARCH` flag:
   ```console
   $ tar -xzf mavor_v0.1.0_linux_amd64.tar.gz
   $ install -m 0755 mavor ~/.local/bin/mavor
   $ install -m 0644 -D -t ~/.local/lib lib*.so
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
   ✅ Runtime and placement:       whisper.cpp, local-server — whisper models default to a supervised warm whisper-server
   ✅ Inference threads:           2 (this machine's physical core count; 4 logical)
   ✅ GPU acceleration:            CPU only (whisper-cli loaded no GPU backend)
   ✅ Configuration file:          valid config (model=whisper-base.en, preview=auto)
   ❌ Voice model availability:    speech: model "whisper-base.en" is in the catalog but not installed — run `mavor models pull whisper-base.en`, or `mavor setup` to install everything this config names
   ✅ Live preview source:         phrases — no companion model is installed; pull "zipformer-streaming-20m" for a live preview
   ✅ Vocabulary biasing:          no [vocabulary] configured — nothing is biased
   ❌ Daemon socket status:        daemon is not running at /run/user/1000/mavor.sock (run 'mavor daemon' or 'mavor service start')
   ✅ Systemd user service:        systemd unit not installed (optional; run 'mavor service install' to enable)
   ==================================================
   ❌ 2 check(s) failed. Fix the issues above before running mavor.
   ```

   With no config file at all, every default applies — which is why the report
   above is a working configuration with two things missing rather than an
   error. A missing config file is not a failure.

3. Derek initializes the configuration file anyway, so his dotfiles have something to track:
   ```console
   $ mavor config init
   ✅ Initialized configuration at ~/.config/mavor/config.toml
   ```

4. He edits it before downloading anything, because `mavor setup` will pull whatever it names. Three changes: the smallest model, no preview companion, and no warm server:
   ```toml
   model = "whisper-tiny.en"

   [preview]
   enabled = false          # no second model resident, no second download

   [advanced]
   placement = "subprocess" # one whisper-cli per utterance; nothing held between
   ```

5. Then one command downloads exactly what that file names, and nothing else:
   ```console
   $ mavor setup
   📥 Downloading model "whisper-tiny.en" into ~/.cache/mavor/models...
   ✅ Downloaded and verified model "whisper-tiny.en"
   ```
   Total on disk: 74.1 MB.

**The minimal workflow:**

6. Derek configures toggle mode in `~/.config/sway/config`:
   ```
   exec mavor daemon
   bindsym $mod+grave exec mavor toggle
   ```

7. Derek reloads Sway. `mavor daemon` launches in the background.
   He checks process memory with `ps -o rss,comm -p $(pgrep mavor)`:
   the daemon holds only its own working set. Because `advanced.placement = "subprocess"` spawns a fresh `whisper-cli` per utterance, no model weights are resident between dictations.

   > [!NOTE]
   > `subprocess` is a deliberate choice here, not the default. Whisper models
   > default to `local-server`: mavor supervises a child `whisper-server` that
   > holds the model warm, which saves 207–259 ms per utterance on the small
   > models and costs the model's memory for as long as the daemon runs. Derek
   > is buying idle memory with latency, and the config says so in one line.
   >
   > The same downgrade happens on its own if no `whisper-server` is on
   > `$PATH` — mavor falls back to `subprocess`, warns once in the log and once
   > in `doctor`, and keeps dictating. A placement you asked for by name is
   > never rewritten, which is the other reason to write it down.

8. Derek hits `$mod+grave` once. The HUD pill turns blue:
   ```
   [ ● RECORDING   ▂▃▅▆ ]
   ```
   He speaks: *"Fix memory leak in network socket poll loop."*
   He hits `$mod+grave` again. The HUD flashes amber, and the text is typed directly into his terminal.

**Changing the model:**

9. While testing dictation on battery power, Derek finds `whisper-tiny.en` transcribes short commands very fast — a measured real-time factor of 0.061, about 16x faster than speech — but occasionally omits plurals or punctuation.

10. Derek decides to test `whisper-base.en` (141.1 MB) to see if the accuracy gain is worth the extra 67 MB of disk space. One line changes, and `setup` fetches what the line now names:
    ```toml
    model = "whisper-base.en"
    ```
    ```console
    $ mavor setup
    📥 Downloading model "whisper-base.en" into ~/.cache/mavor/models...
    ✅ Downloaded and verified model "whisper-base.en"
    ```
    Nothing else in the file moves: the runtime and its placement follow from
    the name, and `subprocess` still applies because `whisper-base.en` is still
    a whisper model.

    **Gap:** Because Derek runs `mavor daemon` via Sway `exec` instead of systemd, there is no `mavor daemon reload` or IPC reload signal (`SIGHUP`). Derek has to find the process ID or run `pkill -f 'mavor daemon'` and restart it manually. Configuration is read once, at daemon start.

11. Derek reloads the daemon:
    ```console
    $ pkill -f 'mavor daemon' && mavor daemon &
    $ mavor status
    idle
    ```

12. He dictates the same sentence again. Accuracy is noticeably improved: measured, `whisper-base.en` takes 1.63 s for 20 s of speech against `whisper-tiny.en`'s 1.05 s, for the best accuracy of any model in the catalog.

**What would trip them up:**
- Starting `mavor daemon` from `.xprofile` or a tty login script before `WAYLAND_DISPLAY` or `XDG_RUNTIME_DIR` is exported into the environment. The daemon exits immediately when it cannot find a Wayland display socket.
- Wondering where daemon logs go when launched via Sway `exec`. Unless `--log-file` or `paths.log` is set, logs go to `~/.local/state/mavor/daemon.log`.
- Installing the binary without its two shared objects. There is one build and it is cgo; the binary is linked to look in `~/.local/lib` and will not start without them.

**What makes this work:**
- The binary needs no GPU libraries and no runtime beyond glibc and the two shared objects that ship with it.
- `advanced.placement = "subprocess"` frees 100% of model memory the moment transcription finishes, and `preview.enabled = false` means there is no second model to hold either.

---

## 3. Lisa — Technical Writer, Hands-Free Long-Form Dictation on Fedora

**Context:** Lisa writes end-user documentation, tutorials, and release notes in Markdown using Obsidian and ghostty on a Fedora laptop running Sway. She does not write code, does not use compilers, and prefers one-shot tooling that handles configuration automatically. She needs reliable speech transcription that handles long-form paragraphs, punctuation, and multilingual terminology without requiring complex terminal setup.

**First 10 minutes:**

1. Lisa discovers `mavor` from an accessibility blog post titled *"Native Voice Dictation on Linux Desktops"*.

2. She downloads the `linux/amd64` release directory and places the binary in `~/.local/bin/` and the two shared objects in `~/.local/lib/`.

3. Lisa runs the automated setup wizard:
   ```console
   $ mavor setup
   mavor setup — automated first-run configuration & model install
   ================================================================
   ⚙️  Creating configuration file at ~/.config/mavor/config.toml...
   ✅ All required system runtime tools (parec, wtype, wl-copy) are available
   📥 Downloading model "whisper-base.en" into ~/.cache/mavor/models...
   ✅ Downloaded and verified model "whisper-base.en"
   📥 Downloading model "zipformer-streaming-20m" into ~/.cache/mavor/models...
   ✅ Downloaded and verified model "zipformer-streaming-20m"
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
   ✅ Runtime and placement:       whisper.cpp, local-server — whisper models default to a supervised warm whisper-server
   ✅ Inference threads:           4 (this machine's physical core count; 8 logical)
   ✅ GPU acceleration:            CPU only (whisper-cli loaded no GPU backend)
   ✅ Configuration file:          valid config (model=whisper-base.en, preview=auto)
   ✅ Voice model availability:    whisper-base.en found at ~/.cache/mavor/models/ggml-base.en.bin
   ✅ Live preview source:         companion (zipformer-streaming-20m) — "whisper-base.en" does not decode incrementally, so the streaming companion "zipformer-streaming-20m" runs alongside it
   ✅ Vocabulary biasing:          no [vocabulary] configured — nothing is biased
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
   The overlay fills with provisional text as she talks, which is what tells her the microphone is live — she never has to stop and check.

8. Lisa presses `$mod+d` again to stop.
   The HUD flips to `[ ⟳ TRANSCRIBING... ]`. In 1.8 seconds, `whisper-base.en` completes the real transcription and types it into Obsidian with accurate punctuation, capitalization, and sentence spacing. The provisional text is discarded; nothing is typed twice.

9. While dictating later in the day, Lisa accidentally unplugs her USB microphone headset:
   The daemon detects the broken PipeWire stream, catches the error, and briefly shows a red error state on the HUD:
   ```
   [ ✕ AUDIO CAPTURE FAILED ]
   ```
   After 1.5 seconds, the HUD smoothly disappears and the daemon resets to `idle` without freezing or crashing.

**Changing the model:**

10. Lisa begins writing international documentation referencing German and French technical standards (*"DIN EN ISO 9001"*, *"Prüfung"*, *"Société"*).

11. She tries dictating with `whisper-base.en`, but the English-only acoustic model mangles foreign loanwords into nonsensical English phonetic approximations.

12. Lisa searches the model catalog for multilingual options:
    ```console
    $ mavor models list
    Model cache: ~/.cache/mavor/models

    NAME                     ENGINE       SIZE  LANGUAGES            STREAM  STATUS
    whisper-base.en          whisper  141.1 MB  en                   no      ✓ 141.1 MB  ★
    whisper-small            whisper  465.0 MB  multi (99)           no      –
    whisper-large-v3-turbo   whisper   1.51 GB  multi (99)           no      –
    canary-180m              sherpa   146.6 MB  en, es, de, fr       no      –
    parakeet-tdt-0.6b        sherpa   464.6 MB  multi (25)           no      –
    sensevoice-small         sherpa   999.3 MB  zh, en, ja, ko, yue  no      –
    zipformer-streaming-20m  sherpa   122.0 MB  en                   yes     ✓ 130.1 MB

    ★ active   ✓ downloaded   – not downloaded
    SIZE is the download; sherpa archives expand to roughly twice that on disk.
    Download one with `mavor models pull <name>`.
    ```
    (Abridged.)

13. `whisper-large-v3-turbo` covers 99 languages, but the measured warning in [`../choosing-a-model.md`](../choosing-a-model.md) rules it out for a writer: it returns unpunctuated lowercase text, and Lisa's whole workflow is punctuation. She picks `canary-180m` instead — English, Spanish, German and French, 146.6 MB to download, 457 MB resident, and the only sherpa model that formats its output as well as `whisper-base.en` does.

14. Lisa opens `~/.config/mavor/config.toml` and changes one line:
    ```toml
    model = "canary-180m"
    ```
    Then she runs `mavor setup` again, which downloads exactly the model that line now names and skips the companion she already has:
    ```console
    $ mavor setup
    📥 Downloading model "canary-180m" into ~/.cache/mavor/models...
    ✅ Downloaded and verified model "canary-180m"
    ✅ Model "zipformer-streaming-20m" is already installed (~/.cache/mavor/models/sherpa/zipformer-streaming-20m)
    ```

15. She restarts the background service and reads what changed underneath her one-line edit:
    ```console
    $ mavor service restart
    ✅ Restarted mavor.service
    $ mavor doctor
    ...
    ✅ Runtime and placement:       sherpa-onnx, in-process — sherpa models are linked into the daemon and stay resident
    ✅ Voice model availability:    canary-180m found at ~/.cache/mavor/models/sherpa/canary-180m
    ✅ Live preview source:         companion (zipformer-streaming-20m) — "canary-180m" does not decode incrementally, so the streaming companion "zipformer-streaming-20m" runs alongside it
    ...
    ```
    She wrote one word. The runtime changed from whisper.cpp to sherpa-onnx, the
    placement from a supervised warm server to in-process, and the preview
    arrangement stayed as it was — all derived, none of it configured.

    **Gap:** `mavor doctor` reads `config.toml` from disk on every invocation,
    so it reports the file as it is *now* — not what the running daemon loaded
    at start. Lisa cannot ask the live process which model it is holding;
    `mavor status` still returns only `idle`. If she edits the file and forgets
    to restart, `doctor` will happily describe a configuration nothing is
    running.

16. Lisa dictates a paragraph containing German and French phrases. `canary-180m` transcribes the diacritics and foreign nouns accurately, and the resident 457 MB is the price she pays for it staying loaded between dictations.

**What would trip them up:**
- On an immutable Linux distribution (like Fedora Silverblue), `mavor setup` may fail to install missing packages via `dnf` because `rpm-ostree` requires layering and rebooting. Clear manual package advice is needed.
- Assuming "multilingual" means "better." The largest multilingual Whisper models are slower *and* format worse than the default; the model that solved Lisa's problem was a third the download of `whisper-large-v3-turbo`.
- Leaving dictation running in a noisy environment: without push-to-talk, ambient coffee shop chatter can append stray words to the document.

**What makes this work:**
- One-shot `mavor setup` is idempotent and config-driven: it makes whatever the file currently says fully runnable, whether that is a first install or a one-word edit.
- Whisper and Canary both handle capitalization, commas, and periods automatically without requiring spoken syntax like *"insert comma"*.

---

## 4. Sam — Audio Power User, Live Streaming Text & PipeWire Ducking

**Context:** Sam is a DevOps engineer and audio enthusiast on Arch Linux running Hyprland with an XLR studio microphone, PipeWire, and continuous background music playback in Spotify. Sam hates waiting for batch transcription after speaking: waiting 2 seconds breaks their train of thought. Sam wants live text as they speak, custom vocabulary boosting for Kubernetes terminology, and automatic media ducking so music doesn't bleed into the microphone.

**First 10 minutes:**

1. Sam finds `mavor` while researching `sherpa-onnx` integrations for Linux. Sam discovers that `mavor` links the in-process sherpa-onnx recognizers in **every** build, including the NVIDIA FastConformer transducer with 80 ms causal chunk streaming.

2. The release directory would do, but Sam builds from source out of habit:
   ```console
   $ git clone https://github.com/mschulkind-oss/mavor && cd mavor
   $ mise install
   $ just build
   $ just deploy
   ```

    **Closed:** this used to be a gap — a pre-built release was pure Go, so a
    downloaded binary could not load any sherpa model and failed at startup if
    you named one. There is one build now and it is always cgo, so a release
    binary reaches every model in the catalog. There is no `sherpa` build tag
    and no second artifact. The cost is that releases are `linux/amd64` only.

3. Sam runs `mavor doctor` to verify the audio stack and see what the default config resolves to:
   ```console
   $ mavor doctor
   mavor doctor — system and environment verification
   ==================================================
   ✅ Wayland session:             WAYLAND_DISPLAY=wayland-1
   ✅ Audio capture (parec/Pulse): parec available, PipeWire/PulseAudio server connected
   ✅ Virtual typing (wtype):      wtype installed at /usr/bin/wtype
   ✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
   ✅ Runtime and placement:       whisper.cpp, local-server — whisper models default to a supervised warm whisper-server
   ✅ Inference threads:           8 (this machine's physical core count; 16 logical)
   ✅ GPU acceleration:            CPU (sherpa models run on the CPU in this build — the ONNX Runtime vendored by the sherpa-onnx Go binding is CPU-only and ships no execution-provider libraries)
   ✅ Configuration file:          valid config (model=whisper-base.en, preview=auto)
   ✅ Voice model availability:    whisper-base.en found at ~/.cache/mavor/models/ggml-base.en.bin
   ✅ Live preview source:         companion (zipformer-streaming-20m) — "whisper-base.en" does not decode incrementally, so the streaming companion "zipformer-streaming-20m" runs alongside it
   ✅ Vocabulary biasing:          no [vocabulary] configured — nothing is biased
   ✅ Daemon socket status:        daemon is active (state: idle)
   ✅ Systemd user service:        systemd unit installed and active (active)
   ==================================================
   ✅ All environment checks passed! mavor is ready.
   ```

**What happens:**

4. Sam configures PipeWire audio ducking in `~/.config/mavor/config.toml`:
   ```toml
   [ducking]
   enabled = true
   volume = "15%"
   apps = ["spotify", "firefox", "chromium"]
   ```

5. Sam starts Spotify playing music at 100% volume.
   Sam presses `$mod+grave` and begins speaking into the microphone:
   - PipeWire ducks the Spotify playback stream to 15% volume via `pactl`.
   - Voice calls in Discord remain untouched because only streams matching `apps` are attenuated; omitting the key would duck everything.
   - The HUD overlay shows the microphone's live vocal energy level.
   - Upon releasing `$mod+grave`, Spotify volume smoothly restores to 100%.

**Getting the words on screen while speaking:**

6. Sam's first assumption is that live text means adopting a streaming model as the model they keep. It does not:

    **Closed:** this used to be a gap — the only way to see words while
    speaking was to make a streaming recognizer the model that produces your
    text, and accept its accuracy for everything you dictate. The **preview
    companion** removed that trade. `preview.source = "auto"` runs
    `zipformer-streaming-20m` alongside `whisper-base.en`, paints its partial
    output in the overlay, and throws it away: the typed text is still the
    batch model's, produced once, on release. Sam had this working before
    reading step 7, without editing a single key.

7. Sam wants more than a preview, though: the *final* text streamed, so the transcript is finished the instant the key comes up. That means a streaming model as `model`. Sam inspects the verbose specification:
   ```console
   $ mavor models list --verbose
   ...
   fastconformer-streaming
     NeMo FastConformer transducer, 80ms chunk — decodes while you speak
     engine      sherpa (in-process sherpa-onnx, CGO)
     download    429.4 MB
     languages   en
     streaming   yes — decodes incrementally while you speak
     speed       fast (relative tier, not measured)
     vocabulary  hotwords supported (transducer)
     gpu         none in practice — the bundled ONNX Runtime is a CPU-only build
     status      – not downloaded
     source      https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-streaming-fast-conformer-transducer-en-80ms.tar.bz2
   ```

8. Sam writes the whole change as four lines. There is no engine key, no mode key, no streaming strategy, and no separate hotwords path — the model decides the runtime, and the vocabulary table is runtime-neutral:
    ```toml
    model = "fastconformer-streaming"

    [vocabulary]
    file = "~/.config/mavor/vocabulary.txt"
    boost = 2.0
    ```

    **Closed:** this used to take six keys — `engine`, `sherpa_model`, `mode`,
    `streaming_strategy`, `sherpa_hotwords_file` and `sherpa_decoding_method` —
    four of which restated the same fact and one of which (`decoding_method`)
    silently disabled the hotwords if you left it at its default. sherpa-onnx
    ignores hotwords under greedy decoding without complaint, so mavor now
    switches to modified beam search itself whenever a vocabulary is present.
    There is no decoding-method key to get wrong.

9. Sam writes the vocabulary file at `~/.config/mavor/vocabulary.txt`, one phrase per line:
   ```
   Kubernetes
   kubectl
   ConfigMap
   StatefulSet
   WireGuard
   Prometheus
   ```

10. Sam pulls the model and restarts:
    ```console
    $ mavor setup
    📥 Downloading model "fastconformer-streaming" into ~/.cache/mavor/models...
    ✅ Downloaded and verified model "fastconformer-streaming"
    $ mavor service restart
    ✅ Restarted mavor.service
    ```

11. `mavor doctor` confirms all three derived consequences of those four lines:
    ```console
    ✅ Runtime and placement:       sherpa-onnx, in-process — sherpa models are linked into the daemon and stay resident
    ✅ Live preview source:         main-model — "fastconformer-streaming" decodes incrementally, so the preview reads its partials and no second model is loaded
    ✅ Vocabulary biasing:          6 phrase(s) → a hotwords file at /run/user/1000/mavor-hotwords.txt, with boost 2.0, decoded by modified beam search
    ```
    The companion is gone: the main model streams, so mavor reads its partials
    directly rather than loading a second model to paint the same words.

    **Gap:** `fastconformer-streaming` pins its cache directory to the old
    `parakeet` name so the rename would not orphan an existing 450 MB download,
    but only `mavor models pull` and `mavor models list` honour that pin — the
    loader searches for a directory named after the catalog entry. The model
    downloads, `models list` marks it installed, and the daemon then refuses to
    start saying it is not. Every other renamed model is unaffected; this is the
    one entry with a pinned directory.

12. Sam holds `$mod+grave` and dictates:
    *"Drain node worker-three and apply the Prometheus StatefulSet ConfigMap."*
    - Words decode incrementally as Sam speaks, appearing in the HUD overlay subtitle. Measured on the comparable `zipformer-streaming`, the first token arrives 107 ms in.
    - Specialized jargon (`StatefulSet`, `ConfigMap`) is boosted during transducer beam search and transcribed correctly.
    - The instant Sam releases the hotkey, the transcript is essentially finished — the streaming recognizer moved the decode into the speaking window rather than after it.

**What would trip them up:**
- Expecting `[vocabulary]` to reach any model. It reaches whisper (as a prompt, capped at 224 tokens) and transducers (as a hotwords file). CTC, paraformer, moonshine and sensevoice models get **nothing** — sherpa-onnx implements biasing only inside transducer beam search — and `mavor doctor` says so plainly rather than failing.
- Setting `boost` too high. 1.5 to 3.0 is the useful range; above that, listed words start appearing where they were not said, and `doctor` reports the value as likely to do so.
- Memory. A sherpa model runs in-process and stays resident for the life of the daemon — measured, `zipformer-streaming` holds 154 MB and `parakeet-tdt-0.6b` holds 1.56 GB — where a whisper model on `subprocess` holds nothing between dictations.
- Expecting the GPU to help. Sherpa models run on the CPU regardless of `advanced.gpu`, because the ONNX Runtime vendored by the Go binding is a CPU-only build. `gpu` is a whisper-only setting.

**What makes this work:**
- The in-process placement eliminates process spawning latency, and it is derived from the model rather than asked for.
- FastConformer decodes 80 ms audio chunks in real time, shifting the compute burden into the speaking window rather than after it.
- Sam's two goals — live words, and accurate final text — turn out to be separable. The preview companion serves the first without touching the second, which is why step 6 is a shorter route than steps 7 through 12 for most people.

---

## 5. Aria — Automated CI / Ephemeral Dev Container Agent

**Context:** Aria is an autonomous coding and testing agent operating inside a headless Docker / NixOS jail container. Aria has no physical monitor, no interactive terminal user, and no physical microphone. Aria needs to install `mavor`, scaffold configuration, run diagnostic checks, pull a lightweight model for end-to-end testing, simulate audio capture via PipeWire virtual sources, and verify transcription output programmatically.

**First 10 minutes:**

1. Aria inspects the repository layout and development instructions in `AGENTS.md`.

2. Aria builds the binary. It is a cgo build — `CGO_ENABLED=0` does not compile, and cross-compiling needs a real toolchain — so the container must carry a C compiler:
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
   📥 Downloading model "whisper-base.en" into ~/.cache/mavor/models...
   ✅ Downloaded and verified model "whisper-base.en"
   📥 Downloading model "zipformer-streaming-20m" into ~/.cache/mavor/models...
   ✅ Downloaded and verified model "zipformer-streaming-20m"
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
   ✅ Runtime and placement:       whisper.cpp, local-server — whisper models default to a supervised warm whisper-server
   ✅ Inference threads:           6 (this machine's physical core count; 12 logical)
   ✅ GPU acceleration:            CPU only (whisper-cli loaded no GPU backend)
   ✅ Configuration file:          valid config (model=whisper-base.en, preview=auto)
   ✅ Voice model availability:    whisper-base.en found at ~/.cache/mavor/models/ggml-base.en.bin
   ✅ Live preview source:         companion (zipformer-streaming-20m) — "whisper-base.en" does not decode incrementally, so the streaming companion "zipformer-streaming-20m" runs alongside it
   ✅ Vocabulary biasing:          no [vocabulary] configured — nothing is biased
   ❌ Daemon socket status:        daemon is not running at /run/user/1000/mavor.sock (run 'mavor daemon' or 'mavor service start')
   ✅ Systemd user service:        systemd unit not installed (optional; run 'mavor service install' to enable)
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
   {"at":"2026-09-05T15:48:12.443Z","text":"Continuous integration test passing."}
   ```

   **Gap:** a history entry records only the timestamp and the text. It does not
   record which model produced it, how long the decode took, or which preview
   mode was active — so a CI matrix that runs the same clip through several
   models cannot attribute a transcript to a model from the log alone. It has to
   correlate against the config it wrote.

**Changing the model for CI optimization:**

9. In ephemeral CI environments, network bandwidth and test execution time are strictly capped. Downloading 141.1 MB of `whisper-base.en` plus 122.0 MB of preview companion on every test matrix run consumes unnecessary time — and a headless run has no overlay to preview into at all.

10. Aria switches the matrix to `whisper-tiny.en` (74.1 MB) and turns the preview off, which removes the second download entirely.

11. Aria edits the configuration file with `sed`, then reads back the resolved result:
    ```console
    $ sed -i 's/^model = .*/model = "whisper-tiny.en"/' ~/.config/mavor/config.toml
    $ sed -i 's/^enabled = true/enabled = false/' ~/.config/mavor/config.toml
    $ ./bin/mavor config show
    # Config file: /root/.config/mavor/config.toml

    model = 'whisper-tiny.en'

    [preview]
    enabled = false
    source = 'auto'
    pause_ms = 450
    min_phrase_ms = 600

    [ducking]
    enabled = false
    volume = '0%'
    apps = []
    sink = ''

    [vocabulary]
    words = []
    file = ''
    boost = 1.5

    [overlay]
    top_margin = 8

    [advanced]
    placement = 'auto'
    server = ''
    threads = 6
    gpu = 'auto'

    [paths]
    models = '/root/.cache/mavor/models'
    log = '/root/.local/state/mavor/daemon.log'
    socket = '/run/user/0/mavor.sock'
    ```

    **Gap:** Updating configuration in scripts still requires `sed` or a file
    overwrite, and the nested schema makes that *worse* rather than better —
    `enabled` is not a unique string in the file, so the second `sed` above is
    only correct because `[preview]` happens to be the first table. A
    `mavor config set preview.enabled false` would eliminate the fragility. That
    the daemon warns about every unknown key at start, and that `doctor` reports
    a wholly stale file, catches a typo *eventually* — but only after a run.

12. Aria pulls the smaller model and restarts the daemon:
    ```console
    $ ./bin/mavor setup
    📥 Downloading model "whisper-tiny.en" into /root/.cache/mavor/models...
    ✅ Downloaded and verified model "whisper-tiny.en"
    $ kill %1
    $ ./bin/mavor daemon --log-file /tmp/mavor.log &
    $ ./bin/mavor status
    idle
    ```

13. Aria reruns the synthetic audio dictation test. Measured on the benchmark harness, `whisper-tiny.en` decodes 20 s of speech in 1.05 s against `whisper-base.en`'s 1.63 s — a real-time factor of 0.061 versus 0.136 — and the removed companion download is 122 MB the matrix no longer fetches.

**What would trip them up:**
- Running in headless containers without a virtual Wayland socket: if `WAYLAND_DISPLAY` is missing and cannot be globbed in `XDG_RUNTIME_DIR`, the daemon fails during startup.
- Assuming `CGO_ENABLED=0` still works. There is one build and it is cgo; a container image without a C compiler cannot build `mavor` at all.
- Parsing human-readable tables. `mavor models list --json` exists — the benchmark harness drives the catalog through it — but `mavor doctor` and `mavor config show` do not have a `--json`, so an agent asserting on either is regex-matching prose.

**What makes this work:**
- The JSON-over-Unix-socket IPC protocol returns clean single-token state responses (`idle`, `recording`, `transcribing`) that can be asserted directly in scripts.
- The `history --json` recovery log provides an authoritative, structured record of completed dictations independent of GUI keystroke capture.
- `mavor setup` is idempotent, so a CI step can run it unconditionally: it downloads whatever the config names and nothing else, and exits zero when everything is already present.

---

## Technical Architecture Reference

### Core CLI Subcommand Matrix

| Subcommand | Invocation | Primary Purpose | Key Flags |
|---|---|---|---|
| `setup` | `mavor setup` | Make the current config runnable: scaffold it, check tools, pull every model it names | `--force` (overwrite config, re-fetch models) |
| `doctor` | `mavor doctor` | Environment health check, plus every derived fact — runtime, placement, threads, GPU, preview source, vocabulary mechanism | `--fix` (trigger setup workflow) |
| `daemon` | `mavor daemon` | Long-lived daemon handling audio, overlay, IPC, and inference | `--verbose`, `--log-file PATH` |
| `start` | `mavor start` | Signal push-to-talk press (enter `recording`) | None |
| `stop` | `mavor stop` | Signal push-to-talk release (enter `transcribing` then `idle`) | None |
| `toggle` | `mavor toggle` | Toggle between `idle` and `recording` | None |
| `status` | `mavor status` | Output current daemon state (`idle`, `recording`, `transcribing`) | None |
| `logs` | `mavor logs` | View or stream daemon logs, from journald when available | `-f`/`--follow`, `-n <lines>` |
| `config` | `mavor config <action>` | Scaffold or inspect configuration | `init [--force]`, `show`, `path` |
| `models` | `mavor models <action>` | Download and inspect the model catalog | `list [--installed] [--verbose] [--json]`, `pull <name>` |
| `service` | `mavor service <action>` | Manage systemd user unit | `install [--start]`, `status`, `restart`, `stop`, `uninstall` |
| `history` | `mavor history` | Inspect or recover recent transcripts | `-n N`, `--json`, `--copy`, `--index N`, `--no-timestamps` |

### The one-line config, by persona

Every persona above changed behaviour by writing model names and table keys — never a runtime, a placement, an engine or a decoding method:

| Persona | The line that mattered | What was derived from it |
|---|---|---|
| Maya | `[vocabulary] words = [...]` | whisper initial prompt, capped at 224 tokens |
| Derek | `advanced.placement = "subprocess"` | A fresh `whisper-cli` per utterance; nothing resident |
| Lisa | `model = "canary-180m"` | sherpa-onnx runtime, in-process placement, companion preview retained |
| Sam | `model = "fastconformer-streaming"` | sherpa-onnx in-process, preview reads the main model's own partials, hotwords file plus modified beam search |
| Aria | `preview.enabled = false` | No companion model named, so `mavor setup` fetches one artifact instead of two |

---

## Open Questions

1. 💬 **OQ-1: Dynamic model reloading via IPC.**

   <!-- vantage: oq id=OQ-1 leaning="Add an IPC reload action that reloads config and rebuilds the transcriber while idle; queue or reject it during recording or transcribing." -->

   Changing `model`, `preview.source` or any other key in `config.toml` requires restarting the daemon process (`mavor service restart` or `pkill -f 'mavor daemon'`). Configuration is read once, at start. Could the daemon support a `reload` IPC action that rebuilds the active model without interrupting the socket connection? Lisa's gap in story 3 and Derek's in story 2 are both this question wearing different clothes.

   _Leaning:_ Add an IPC `reload` action that triggers `config.LoadFile("")` and rebuilds the `speech.Transcriber` if the daemon is currently `idle`. If the daemon is `recording` or `transcribing`, queue or reject the reload until the active phrase finishes. A reload must re-run the preview resolution too, since `preview.source` can change what is loaded.

   **Answer:**
   > _(empty — fill in when decided)_

2. 💬 **OQ-2: Machine-readable output for the remaining commands.**

   <!-- vantage: oq id=OQ-2 leaning="Add --json to doctor and config show; skip status, which is already a single token." -->

   `mavor models list --json` shipped with the catalog rewrite and is how the benchmark harness reads the catalog, and `mavor history --json` predates it. `mavor doctor` and `mavor config show` still emit only human-readable text — and `doctor` is now the command that carries every derived fact an agent would want to assert on (runtime, placement, thread count, preview mode, vocabulary mechanism).

   _Leaning:_ Add `--json` to `mavor doctor` and `mavor config show`. `doctor`'s schema should be one record per check with the check name, pass/fail, and the message. `mavor status` already returns a single token and needs nothing.

   **Answer:**
   > _(empty — fill in when decided)_

3. 💬 🤷 **OQ-3: Download confirmation for large artifacts.**

   <!-- vantage: oq id=OQ-3 leaning="Prompt above 500 MB when stdin is a TTY, with -y to skip; mavor setup is the non-interactive path and must never prompt." -->

   `whisper-large-v3` is 2.88 GB and `whisper-large-v3-turbo` is 1.51 GB, against 141.1 MB for `whisper-base.en` and 74.1 MB for `whisper-tiny.en`. Should `mavor models pull` prompt for confirmation above some size when attached to an interactive TTY?

   _Leaning:_ An interactive `Download 1.51 GB? [y/N]` when `os.Stdin` is a TTY, skippable with `-y` / `--yes`. `mavor setup` must remain non-interactive whatever this rules, since it is the documented CI path (story 5) — and it now pulls two models rather than one.

   **Answer:**
   > _(empty — fill in when decided)_

### Decision Ledger

| ID | Ruling / Decision | Date | Settled in |
|---|---|---|---|
| OQ-4 | **Superseded.** In-process versus sidecar was originally answered as "in-tree pluggable `Transcriber` engines selected by an `engine` key." That key welded together two independent questions and is deleted. The durable answer is the **runtime**/**placement** split: the runtime follows from the model via the catalog, the placement is derived per runtime, and `[advanced]` exposes only the two overrides that cannot be computed. | 2026-08-16, superseded 2026-09-05 | [`../design/configuration-surface.md`](../design/configuration-surface.md#3-two-axes-welded-into-one-enum), [§2 of the user guide](../user-guide.md#2-how-a-model-runs-runtime-and-placement) |
| OQ-5 | **Zero audio retention.** Capture files in `/tmp/mavor-recordings/` are unlinked as soon as transcription completes, so no dictation audio outlives the utterance. | 2026-08-16 | [`../reference/how-mavor-works.md`](../reference/how-mavor-works.md) |
| OQ-6 | **The live preview never types.** Provisional text is painted in the overlay and discarded; the transcript always comes from `model`, produced once, on release. A companion model may produce the preview, and is pulled by `mavor setup`. | 2026-09-05 | [`../design/configuration-surface.md`](../design/configuration-surface.md#6-the-preview) |
| OQ-7 | **Vocabulary is one runtime-neutral table.** `[vocabulary]` becomes a whisper prompt, a transducer hotwords file (forcing modified beam search), or nothing — decided by the model, reported by `doctor`. There is no decoding-method key. | 2026-09-05 | [`../design/configuration-surface.md`](../design/configuration-surface.md#7-vocabulary-and-decoding) |
