---
title: "mavor — 5-Minute Quickstart Guide"
author: "Matthew Schulkind"
date: 2026-08-16
status: accepted
tags: [quickstart, guide, sway, wayland, tutorial, deploy]
summary: "Get mavor voice dictation running on Sway in under 5 minutes with just install/deploy and mavor doctor."
---

# mavor — 5-Minute Quickstart Guide

Get low-latency voice dictation working in your Sway/Wayland desktop environment in five simple steps.

---

## Step 1: Install Dependencies

On Arch Linux / NixOS / Debian / Fedora:

```bash
# Arch Linux
sudo pacman -S sway waybar wtype wl-clipboard pipewire pulseaudio-utils whisper-cpp

# Nix / NixOS (or run inside a dev shell)
nix-shell -p sway waybar wtype wl-clipboard pipewire pulseaudio whisper-cpp
```

---

## Step 2: Build and Install

Clone the repository and install the binary (or deploy as a background systemd user service):

```bash
git clone https://github.com/mschulkind-oss/mavor.git
cd mavor
mise install     # ensures Go toolchain

# Quick install to ~/.local/bin/mavor:
just install

# Or install binary and configure systemd user service in one step:
just deploy
```

---

## Step 3: Verify Environment & Pull Voice Model

Run the self-diagnostic health check:

```bash
mavor doctor
```

Initialize default configuration and download the production voice model (~148 MB):

```bash
mavor config init
mavor models pull base.en
```

---

## Step 4: Configure Sway Hotkeys

Add the following to `~/.config/sway/config`:

```
# Launch daemon on Sway startup (omit if using systemd service from 'just deploy')
exec mavor daemon

# Push-to-Talk (Hold to Speak, Release to Transcribe)
bindsym $mod+grave exec mavor start
bindsym --release $mod+grave exec mavor stop
```

Reload Sway:
```bash
swaymsg reload
```

---

## Step 5: Test Dictation

1. Focus any text editor, terminal, or browser input field.
2. Hold down `$mod+grave` (`Super + \``).
3. Watch the floating HUD overlay appear near the top of the screen with a live waveform meter.
4. Speak a sentence into your microphone:
   *"Hello world, voice dictation on Sway works seamlessly."*
5. Release the hotkey.
6. The transcribed text will instantly type into the focused window and copy to your clipboard!

---

## Next Steps

- Explore in-process **Sherpa-ONNX CGO** and live token streaming in the [`User Guide`](./user-guide.md).
- Enable automatic **PipeWire Audio Ducking** for Spotify and browser playback in `~/.config/mavor/config.toml`.
- Run `just storybook` to view the UI HUD visual regression report.
