---
title: "mavor — 5-Minute Quickstart"
author: "Matthew Schulkind"
date: 2026-09-05
status: accepted
tags: [quickstart, guide, sway, wayland, tutorial, setup, doctor]
summary: "Install mavor, run `mavor setup`, bind a key, and dictate your first sentence — the shortest path from nothing to typed words, with what to check when a step does not land."
---

# `mavor` — 5-Minute Quickstart

Nothing to typed words, in five steps. Everything here runs locally: no
account, no API key, and the only outbound requests are the model downloads in
step 2.

This is the short path. [`user-guide.md`](./user-guide.md) is the same
territory in depth — the live preview, ducking, vocabulary, every config key.

> [!NOTE]
> mavor needs a **wlroots Wayland compositor** — sway, Hyprland, river,
> Wayfire, niri or labwc. The overlay is a `wlr-layer-shell` surface and the
> typing goes through `virtual-keyboard-v1`; neither exists on GNOME or X11.

---

## Step 1: Install the binary

```bash
go install github.com/mschulkind-oss/mavor/cmd/mavor@latest
```

mavor links sherpa-onnx through cgo, so this needs a C compiler on the
machine — but nothing else: the shared objects come out of the Go module
cache, and `go install` leaves the binary pointing at them there. Tagged
releases also publish a `linux/amd64` tarball, which carries those shared
objects alongside the binary — see
[Install in the README](../README.md#install) for that route and for
building from source with `just install`.

---

## Step 2: Run `mavor setup`

One command scaffolds the config, installs whatever runtime tools are
missing, downloads **every model the config names**, and installs the systemd
user service:

```console
$ mavor setup
mavor setup — automated first-run configuration & model install
================================================================
⚙️  Creating configuration file at /home/you/.config/mavor/config.toml...
✅ Initialized configuration at /home/you/.config/mavor/config.toml
✅ All required system runtime tools (parec, wtype, wl-copy) are available
📥 Downloading model "whisper-base.en" into /home/you/.cache/mavor/models...
downloading whisper-base.en (Whisper Base, 74M parameters, English-only — the default)
URL: https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin
✅ Downloaded and verified model "whisper-base.en"
📥 Downloading model "zipformer-streaming-20m" into /home/you/.cache/mavor/models...
downloading zipformer-streaming-20m (Streaming Zipformer transducer, 20M parameters — small enough to run alongside another model as the live-preview source)
URL: https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-en-20M-2023-02-17.tar.bz2
✅ Successfully extracted 13 model files to /home/you/.cache/mavor/models/sherpa/zipformer-streaming-20m
✅ Downloaded and verified model "zipformer-streaming-20m"

================================================================
🎉 Setup complete! mavor is configured and ready.
```

Two models, because the scaffolded config names two. `whisper-base.en` is the
141 MB Whisper model that produces your text — it scores 0.0% word error on
the project's fixture, and the larger Whisper models score *worse* on
formatted text. `zipformer-streaming-20m` is the 122 MB streaming companion
that paints the live preview in the overlay while you speak; it never
contributes a word to what gets typed.
[`choosing-a-model.md`](./choosing-a-model.md) explains both choices, and
which model to pick if English-only does not fit.

> [!NOTE]
> **`mavor setup` is idempotent, and it is how you apply a config edit.** It
> pulls what the current `config.toml` names, skips what is already in the
> cache, and can be re-run at any time. Change `model` or `preview.source` to
> something you do not have yet, run `mavor setup` again, and it fetches just
> that. Run it twice with nothing changed and the second run downloads
> nothing:
>
> ```console
> $ mavor setup
> ✅ Configuration file found at /home/you/.config/mavor/config.toml
> ✅ All required system runtime tools (parec, wtype, wl-copy) are available
> ✅ Model "whisper-base.en" is already installed (/home/you/.cache/mavor/models/ggml-base.en.bin)
> ✅ Model "zipformer-streaming-20m" is already installed (/home/you/.cache/mavor/models/sherpa/zipformer-streaming-20m)
> ```
>
> After `setup` exits zero, `mavor daemon` starts on that config and needs no
> further downloads. That is the contract.

Where `systemctl` is on `PATH`, setup ends by installing and enabling the
`mavor.service` user unit as well; the runs above are from a machine without
it.

If a runtime tool is missing, setup names it, detects your distribution, and
asks for the one privileged install:

```console
📦 Missing system tools detected: parec, wtype
🔍 Detected Linux distribution: arch
🔐 Privileged setup required to install missing system packages.
```

Prefer to install them yourself? They are `parec` (pulseaudio-utils or
pipewire-pulse), `wtype`, `wl-clipboard`, and — whenever `model` is a whisper
model — `whisper-cpp`, which supplies `whisper-server` and `whisper-cli`.

---

## Step 3: Check the environment

```console
$ mavor doctor
mavor doctor — system and environment verification
==================================================
✅ Wayland session:             WAYLAND_DISPLAY=wayland-1
✅ Audio capture (parec/Pulse): parec available (audio server check skipped/idle)
✅ Virtual typing (wtype):      wtype installed at /bin/wtype
✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
✅ Runtime and placement:       whisper.cpp, local-server — whisper models default to a supervised warm whisper-server
✅ Inference threads:           6 (this machine's physical core count; 12 logical)
✅ GPU acceleration:            CPU only (whisper-cli loaded no GPU backend — the stock build ships CPU backends only; install a whisper.cpp built with -DGGML_VULKAN=ON for acceleration)
✅ Configuration file:          valid config (model=whisper-base.en, preview=auto)
✅ Voice model availability:    whisper-base.en found at /home/you/.cache/mavor/models/ggml-base.en.bin
✅ Live preview source:         companion (zipformer-streaming-20m) — "whisper-base.en" does not decode incrementally, so the streaming companion "zipformer-streaming-20m" runs alongside it
✅ Vocabulary biasing:          no [vocabulary] configured — nothing is biased
❌ Daemon socket status:        daemon is not running at /run/user/1000/mavor.sock (run 'mavor daemon' or 'mavor service start')
✅ Systemd user service:        systemd unit not installed (optional; run 'mavor service install' to enable)
==================================================
❌ 1 check(s) failed. Fix the issues above before running mavor.
```

A failing daemon check at this point is expected — nothing has started it
yet. Every other line should be green before you go on; each failure carries
its own fix in parentheses, and `mavor doctor --fix` re-runs setup.

`doctor` is the second half of the config file: `config.toml` says what you
asked for, and these lines say what *this* machine will do with it — which
runtime and where it runs, how many threads, whether a GPU backend loaded,
and where the preview text is coming from. Read it after every config edit.

"CPU only" is a statement, not a fault: the packaged `whisper-cpp` is a
CPU-only build, and `whisper-base.en` transcribes 20 seconds of speech in
1.63 s on a 2017-era desktop CPU. GPU is an optimisation, not a requirement —
see [`model-benchmarks.md`](./reports/model-benchmarks.md) for what it buys.

Start the daemon:

```bash
systemctl --user start mavor     # installed by `mavor setup`
# or, without systemd:
mavor daemon
```

---

## Step 4: Bind a key

Push-to-talk — hold to speak, release to transcribe:

```
# ~/.config/sway/config
bindsym $mod+grave exec mavor start
bindsym --release $mod+grave exec mavor stop
```

Or toggle, if holding a key is the problem you came here to solve:

```
bindsym $mod+grave exec mavor toggle
```

Reload the compositor (`swaymsg reload` on sway) to pick it up. The same two
lines work on Hyprland, river, Wayfire, niri and labwc in that compositor's
own bind syntax — mavor only cares that something runs `mavor start` and
`mavor stop`.

---

## Step 5: Dictate

1. Focus any text field — editor, terminal, browser.
2. Hold `$mod+grave`. The HUD appears at the top of the screen with a live
   waveform, and any music ducks if `ducking.enabled` is on.
3. Say *"Hello world, dictation on Wayland works."* Words appear in the HUD as
   you speak — that is the preview, and it is only a preview.
4. Release. The HUD switches to transcribing, `whisper-base.en` transcribes
   the whole utterance once, and those words type themselves into the focused
   window and land on the clipboard.

**Nothing typed?** The transcript is not lost — every completed transcript is
appended to a history log:

```bash
mavor history            # newest first
mavor history --copy     # put the most recent one on the clipboard
mavor logs -f            # watch the daemon while you try again
```

The usual cause is a compositor that refuses `virtual-keyboard-v1` to an
unfocused client; the clipboard copy still works, so `mavor history --copy`
followed by a paste gets the words in.

---

## Next steps

| Want to | Read |
|---|---|
| Pick a different model, or a language other than English | [`choosing-a-model.md`](./choosing-a-model.md) |
| Tune the live preview, bias the model toward your vocabulary, configure ducking | [`user-guide.md`](./user-guide.md) |
| See the speed, memory and accuracy numbers behind the defaults | [`reports/model-benchmarks.md`](./reports/model-benchmarks.md) |
| Understand how the daemon is put together | [`reference/how-mavor-works.md`](./reference/how-mavor-works.md) |
