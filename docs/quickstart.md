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
account, no API key, and the only outbound request is the model download in
step 2.

This is the short path. [`user-guide.md`](./user-guide.md) is the same
territory in depth — engines, streaming, ducking, every config key.

> [!NOTE]
> mavor needs a **wlroots Wayland compositor** — sway, Hyprland, river,
> Wayfire, niri or labwc. The overlay is a `wlr-layer-shell` surface and the
> typing goes through `virtual-keyboard-v1`; neither exists on GNOME or X11.

---

## Step 1: Install the binary

```bash
go install github.com/mschulkind-oss/mavor/cmd/mavor@latest
```

The default build is pure Go, so this needs no system headers. Tagged
releases also publish a `linux/amd64` tarball — see
[Install in the README](../README.md#install) for that route and for
building from source with `just install`.

---

## Step 2: Run `mavor setup`

One command scaffolds the config, installs whatever runtime tools are
missing, downloads the default model, and installs the systemd user service:

```console
$ mavor setup
mavor setup — automated first-run configuration & model install
================================================================
⚙️  Creating configuration file at /home/you/.config/mavor/config.toml...
✅ All required system runtime tools (parec, wtype, wl-copy) are available
📥 Downloading default voice model "base.en" into /home/you/.cache/mavor/models...
✅ Downloaded and verified voice model "base.en"

⚙️  Setting up systemd user service...
✅ Installed systemd user unit at /home/you/.config/systemd/user/mavor.service (ExecStart=/home/you/go/bin/mavor daemon)
✅ Enabled mavor.service for graphical session startup

================================================================
🎉 Setup complete! mavor is configured and ready.
```

`base.en` is a 141 MB Whisper model and the right default — it scores 0.0%
word error on the project's fixture, and the larger Whisper models score
*worse* on formatted text. [`choosing-a-model.md`](./choosing-a-model.md)
explains why, and which model to pick if English-only does not fit.

If a runtime tool is missing, setup names it, detects your distribution, and
asks for the one privileged install:

```console
📦 Missing system tools detected: parec, wtype
🔍 Detected Linux distribution: arch
🔐 Privileged setup required to install missing system packages.
```

Prefer to install them yourself? They are `parec` (pulseaudio-utils or
pipewire-pulse), `wtype`, `wl-clipboard`, and `whisper-cpp` for the default
`cli` engine.

---

## Step 3: Check the environment

```console
$ mavor doctor
mavor doctor — system and environment verification
==================================================
✅ Wayland session:             WAYLAND_DISPLAY=wayland-1
✅ Audio capture (parec/Pulse): parec available (audio server check skipped/idle)
✅ Virtual typing (wtype):      wtype installed at /usr/bin/wtype
✅ Clipboard (wl-clipboard):    wl-copy and wl-paste installed
✅ Speech engine:               whisper-cli installed at /usr/bin/whisper-cli
✅ GPU acceleration:            CPU only (whisper-cli loaded no GPU backend — the stock build ships CPU backends only)
✅ Configuration file:          valid config (mode=batch, preset=balanced, model=base.en)
✅ Voice model availability:    whisper model found at /home/you/.cache/mavor/models/ggml-base.en.bin
❌ Daemon socket status:        daemon is not running at /run/user/1000/mavor.sock (run 'mavor daemon' or 'mavor service start')
✅ Systemd user service:        systemd unit installed (inactive)
==================================================
❌ 1 check(s) failed. Fix the issues above before running mavor.
```

A failing daemon check at this point is expected — nothing has started it
yet. Every other line should be green before you go on; each failure carries
its own fix in parentheses, and `mavor doctor --fix` re-runs setup.

"CPU only" is a statement, not a fault: the packaged `whisper-cpp` is a
CPU-only build, and `base.en` transcribes 20 seconds of speech in 1.63 s on
a 2017-era desktop CPU. GPU is an optimisation, not a requirement — see
[`model-benchmarks.md`](./reports/model-benchmarks.md) for what it buys.

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
   waveform, and any music ducks if `duck_audio` is on.
3. Say *"Hello world, dictation on Wayland works."*
4. Release. The HUD switches to transcribing, and the words type themselves
   into the focused window and land on the clipboard.

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
| Stream tokens as you speak, switch to the in-process sherpa engines, tune ducking and VAD | [`user-guide.md`](./user-guide.md) |
| See the speed, memory and accuracy numbers behind the defaults | [`reports/model-benchmarks.md`](./reports/model-benchmarks.md) |
| Understand how the daemon is put together | [`reference/how-mavor-works.md`](./reference/how-mavor-works.md) |
