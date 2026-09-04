---
title: "Wayland Dictation Stack: Desktop Plumbing & OSS Ecosystem Survey"
author: "Matthew Schulkind"
date: 2026-08-15
status: accepted
tags: [research, wayland, sway, audio, input, pipewire, wtype]
summary: "Research on Wayland input protocols (wtype, uinput, libei), PipeWire audio capture, hotkey models, and OSS dictation tooling."
---

# Wayland dictation stack — desktop plumbing and the OSS dictation ecosystem

Evergreen domain doc for `mavor`. Scope: everything between the microphone jack and
the characters landing in the focused window — **text injection, audio capture,
hotkey/interaction model, status UI** — plus a survey of the other open-source
dictation tools worth stealing from.

Out of scope (owned by sibling docs): ASR model selection, inference runtimes,
hosted transcription APIs.

**Round 1 — 2026-08-15.** No prior `docs/research/` content existed; this doc is
the starting point for future rounds.

Provenance markers used below:
- **[SRC]** — read from the project's actual source code on the date given.
- **[LOCAL]** — reproduced locally in a containerized Linux environment against the real tooling on the date given.
- **[DOC]** — read in the project's own docs/README/issue tracker.
- **[INF]** — inference from the above; plausible but not directly observed.

---

## 0. Where `mavor` stands today

Verified **[SRC]** against the working tree, 2026-08-15 (~3.2k lines Go):

| Concern | Current implementation | File |
|---|---|---|
| Text injection | `wtype -- <text>` **and** `wl-copy` on every emission, errors joined | `internal/output/output.go` |
| Audio capture | `parec --format=s16le --rate=16000 --channels=1 --file-format=wav <path>`, SIGINT to stop so the WAV header flushes | `internal/audio/audio.go` |
| Transcription | `whisper-cli -m <model> -f <wav> -otxt -nt -np`, reads the `<wav>.txt` sidecar | `internal/speech/speech.go` |
| Hotkey | none of its own — sway `bindsym $mod+grave exec mavor toggle` → `mavor toggle` → JSON over `$XDG_RUNTIME_DIR/mavor.sock` | `README.md`, `internal/ipc/ipc.go` |
| State model | 3-state FSM `Idle → Recording → Transcribing → Idle`; toggle during `Transcribing` is a deliberate no-op | `internal/state/state.go` |
| Status UI | `wlr-layer-shell` bar on the `top` layer, no exclusive zone, no keyboard interactivity, painted in Go | `internal/overlay/`, `internal/wayland/` |
| Config | 4 keys: `top_margin`, `model`, `model_dir`, `socket` | `internal/config/config.go` |

Notable absences relative to the field (see §5): no device selection, no
push-to-talk, no cancel, no history/undo, no word-replacement pass, no
notification or audio cue, no waybar integration, no post-processing hook.

### Three latent bugs found while reading the plumbing

These are the highest-value concrete outputs of this round.

**(a) `wl-copy` will wedge the daemon in `Transcribing`.** `wl-copy` daemonizes
itself after it takes the selection: it `dup2`s `/dev/null` over stdin and
stdout *specifically so the caller's stdout pipe is released* — and then forks,
**leaving the inherited stderr (fd 2) open in the background child**
(`did_set_selection_callback`, wl-clipboard `src/wl-copy.c`) **[SRC 2026-08-15]**.
`output.DefaultRunner` captures with `cmd.CombinedOutput()`, which does not
return until every write end of the child pipe is closed. Reproduced **[LOCAL
2026-08-15]** with a stand-in (`sh -c 'exec 1>/dev/null; (sleep 10) & exit 0'`):

```
CombinedOutput returned after 10.010s      # blocked on the forked child's stderr
Run() (no capture) returned after 13ms
```

Consequence: `Emit` blocks → `runTranscription` never reaches
`machine.Apply(EventTranscribeDone)` → FSM stays in `Transcribing`, the overlay
stays up, and the next `mavor toggle` is swallowed, until something else claims
the clipboard. A clipboard manager (`cliphist`, `wl-clip-persist`) grabs the
selection immediately and masks this, which is likely why it has not been seen
on the dev rig **[INF]**. Fixes, in order of preference: capture stderr into an
explicit `os.Pipe` you close yourself; or `wl-copy --foreground` in a goroutine
you own; or drop output capture for `wl-copy` (`Run()` with `Stderr = nil`).

**(b) Newlines in the transcript become Enter keypresses.** whisper.cpp's
`output_txt` writes `fout << "\n"` after *every* segment
(`examples/cli/cli.cpp`) **[SRC 2026-08-15]**, so any multi-segment dictation
yields an embedded `\n`. `mavor` only `strings.TrimSpace`s the whole body, so
interior newlines survive. wtype then hard-remaps `L'\n' → XKB_KEY_Return` in
`get_key_code_by_wchar` **[SRC 2026-08-15]**. Net effect: dictating two
sentences into Slack/Discord/a chat box sends the message halfway through; into
a shell prompt, it executes the line. Fix: normalise whitespace
(`\n`/`\t` → single space, collapse runs) before `Emit`. This is worth a
regression test.

**(c) A non-UTF-8 locale makes wtype refuse non-ASCII.** wtype calls
`setlocale(LC_CTYPE, "")` then `mbstowcs` on each `argv` text chunk, and
`fail("Failed to deencode input argv")` on error **[SRC 2026-08-15]** — this is
wtype issue #55. A daemon started from a sway `exec` line or a systemd user unit
can easily inherit `LC_CTYPE=POSIX`, at which point every accented character,
curly quote or em-dash kills the whole emission. `mavor` should force a UTF-8
`LC_CTYPE` in the child environment it hands to wtype, or feed text on stdin via
wtype's `-` placeholder rather than through `argv`.

---

## 1. Text injection

### 1.1 The protocol landscape

There are exactly four mechanisms on Wayland, and they differ in *where* they
enter the stack:

| Mechanism | Enters at | Needs compositor buy-in | Reaches XWayland | Reaches Electron/Chromium |
|---|---|---|---|---|
| `virtual-keyboard-unstable-v1` (wtype) | compositor seat, as synthetic key events | yes | in principle; reported broken | flaky |
| `uinput` (ydotool, dotool) | kernel evdev, below the compositor | no | yes | yes |
| libei / EIS (eitype) | compositor's emulated-input server | yes, plus a portal session | yes | yes |
| `input-method-unstable-v2` (IME) | compositor's text-input routing | yes | **no** | **no** (on wlroots) |
| clipboard + synthetic paste | `wl_data_device` + one of the above for the keystroke | partial | yes | yes |

**`virtual-keyboard-unstable-v1`** is implemented by wlroots (so sway, river,
Hyprland, labwc), Weston, and COSMIC. It is **not** implemented by GNOME/mutter
— mutter issue #4124 is the open tracking bug **[DOC 2026-08-15]** — and not
usably by KWin, which is why `wtype` fails with "Compositor does not support the
virtual keyboard protocol" on both (wtype issues #29, #45). Note that
`wayland.app` lists "18 implementations" including Mutter and KWin; that table is
generated by scanning repos for the interface name and is **not** a reliable
support matrix — prefer the compositor's own issue tracker.

**`input-method-unstable-v2`** is a wlroots-originated protocol (still
`unstable`, still not in wayland-protocols proper). sway implements it,
including IM popups (sway 1.6+). The catch is the *other* half: an IME can only
commit text into a surface that has an active `zwp_text_input_v3`. GTK3/GTK4 and
Qt-with-the-Wayland-IM-module do. **Chromium/Electron do not, on wlroots**:
Chromium's Ozone backend has mature `zwp_text_input_v1` (KWin, Weston) and only
experimental v3 behind `--enable-wayland-ime=v3`, so on sway there is
effectively no working IME path for Chromium-family apps **[DOC 2026-08-15]**.
XWayland clients are structurally excluded — they receive input via the X
server, so a Wayland IME never sees them.

### 1.2 `wtype` — status quo

- Upstream `atx/wtype`: **last commit 2022-01-27, last release v0.4 (2022-01-27),
  42 open issues, not archived** — checked via the GitHub API **[2026-08-15]**.
  Four and a half years of no commits with an active issue queue: treat it as
  frozen, not maintained.
- Mechanism **[SRC 2026-08-15]**: wtype builds a *synthetic* xkb keymap
  containing one keycode per unique character in the string (`xkb_keycodes`
  starting at keycode 9, `minimum = 8`), uploads it with
  `zwp_virtual_keyboard_v1.keymap`, then presses/releases raw keycodes with a
  hard-coded 2 ms gap and a default inter-key delay of 0. So it is
  layout-independent *by design* — it never uses your layout.
- …which is also its failure mode. It is only layout-independent if the
  compositor and the client honour the virtual keyboard's own keymap. Voxtype's
  docs report COSMIC typing digits instead of letters — the compositor applying
  the seat keymap to wtype's keycodes **[DOC 2026-08-15]**.
- The long tail of "Chromium/Electron drops or mangles characters" issues (#31,
  #37, #38, #40, #52, #58, #71, #72, #76) is the same shape: the client
  processes the `wl_keyboard.keymap` fd asynchronously, wtype sends key events
  immediately and exits, so events can be interpreted under the wrong keymap.
  Issue #71's title — "Punctuation at 14th unique character position in
  Chromium/Electron" — is consistent with the 14th unique char landing on
  keycode 22, which Chromium hardcodes as BackSpace for its
  `KeyboardEvent.code` / editing-command mapping **[INF]**. The standard
  mitigation is a per-key delay (`wtype -d N`), and note `-d 0` is *rejected*
  by the arg parser (`if (delay_ms <= 0) fail(...)`) **[SRC]**.
- Issue #62 (2024): typing into XWayland windows does nothing at all, open, no
  workaround. Issue #66: fails to release modifiers on exit.
- Issue #5, "Use input-method if available", has been open since 2020.

**Verdict: keep as the default driver on sway, but demote it from
"the" mechanism to "one driver in a chain."** It is the only injection path
that needs no daemon, no `input` group, and no portal, and on wlroots +
native-Wayland apps it works. It is also frozen upstream, broken on
GNOME/KDE, unreliable in Electron and XWayland, and has three sharp edges that
`mavor` currently steps on (§0). Do not build more on top of it than a driver
interface.

### 1.3 `ydotool`

- `ReimuNotMoe/ydotool`: 2.3k stars, **last commit 2025-12-22**, last tagged
  release v1.0.4 (2023-01-30), 95 open issues, **AGPL-3.0** **[API 2026-08-15]**.
  Maintained in the "occasional merge" sense.
- Writes to `/dev/uinput`, creating a virtual keyboard the kernel treats as
  real. Consequence: works on every compositor, every toolkit, XWayland and
  bare TTYs, because nothing above the kernel can tell the difference.
- Costs: a persistent `ydotoold` daemon (packaged as a systemd user service);
  membership in the `input` group or a udev rule; and — the real killer for
  dictation — **it types by keycode against the active layout, so it cannot
  emit characters that are not on your keymap.** Voxtype's driver notes state
  flatly that ydotool "cannot output CJK characters" **[DOC 2026-08-15]**. On a
  US layout this also breaks accented characters, `—`, `“ ”`, and emoji, all of
  which whisper happily produces.
- AGPL is not a problem for `mavor` (we exec it, we don't link it), but it is a
  packaging consideration for any distro bundling.

**Verdict: shortlist as a fallback driver, not a default.** Worth having for
XWayland windows and for users on GNOME/KDE, but the layout-bound character set
makes it a downgrade for general dictation.

### 1.4 `dotool`

- `git.sr.ht/~geb/dotool`, by John Gebbie, **written in Go**, written for
  Numen **[DOC 2026-08-15]**. Same uinput mechanism as ydotool (same `input`
  group / udev requirement, but no separate daemon — it reads actions from
  stdin).
- Crucially it is **layout-aware rather than layout-bound**: it composes
  keystrokes against an xkb layout you declare via `DOTOOL_XKB_LAYOUT` /
  `DOTOOL_XKB_VARIANT`, so non-US layouts type correctly. Voxtype uses exactly
  this positioning — wtype first, `dotool` as "the fallback for non-US
  layouts" **[DOC 2026-08-15]**.

**Verdict: shortlist — the best second driver.** Being Go is incidental (it is
a CLI, we'd exec it) but it means the character-composition logic is readable
if `mavor` ever wants to do uinput natively. Its weakness is the same as
ydotool's: it can only type what the declared layout can express.

### 1.5 libei / `eitype`

- libei is the Emulated Input stack (client `libei`, compositor-side EIS,
  `liboeffis` for the D-Bus handshake with the **XDG RemoteDesktop portal**).
  It is the path GNOME and KDE actually want automation tools to use, and
  `eitype` (`Adam-D-Lewis/eitype`, Rust + Python bindings, pushed 2026-06-13)
  is a "type this text" CLI on top of it **[DOC 2026-08-15]**. Voxtype lists
  `eitype` as its GNOME/KDE driver.
- On sway this is a dead end today: `xdg-desktop-portal-wlr`'s README states it
  implements **only** `org.freedesktop.impl.portal.Screenshot` and
  `org.freedesktop.impl.portal.ScreenCast` **[SRC 2026-08-15]**. No
  RemoteDesktop backend → no EIS session → `eitype` has nothing to connect to.

**Verdict: rejected for the sway target; keep as the known portability escape
hatch.** If `mavor` ever grows a driver chain, `eitype` is the correct entry for
"user is on GNOME or KDE". Not worth a line of code until then.

### 1.6 Clipboard-then-paste

The technique: `wl-copy` the text, then synthesize `Ctrl+V` with whichever
keystroke mechanism works, then optionally restore the previous clipboard.

Real problems, in order of how much they bite:

1. **There is no universal paste chord.** `Ctrl+V` in GUI apps,
   `Ctrl+Shift+V` in most terminals, `Shift+Insert` in some, and Vim's normal
   mode does something else entirely. Per-app knowledge is required, which is
   why tools that do this ship per-app profiles.
2. **You still need a keystroke driver** for the chord, so it does not remove
   the wtype/ydotool dependency — it just reduces it from N keystrokes to two,
   which does dodge the entire "keymap for arbitrary Unicode" problem. That is
   the real win: a clipboard paste can carry emoji and CJK that ydotool cannot.
3. **Clipboard restore is a race.** The old contents must be read, the new
   contents set, the paste delivered *and consumed*, then the old contents
   restored — with no completion signal for step three. OpenWhispr issue #240
   is literally titled "clipboard restore race condition" **[DOC 2026-08-15]**.
   `wl-copy --paste-once` (`-o`, exit after serving one paste) makes this
   tractable.
4. Privacy: dictated text lands in the clipboard history of `cliphist` et al.
   `wl-copy --sensitive` exists as a hint to clipboard managers **[SRC
   2026-08-15]**, and `mavor` should probably set it.

**Verdict: adopt as the always-on fallback, which is what `mavor` already
effectively does** — every `Emit` copies as well as types, so a failed
injection still leaves the text one `Ctrl+V` away. Promote it to a real driver
(copy + synthesized paste) only behind an opt-in config key, because of (1).

### 1.7 What an IME-based approach would actually buy

The appeal is real: `zwp_input_method_v2.set_preedit_string` would let `mavor`
show *live partial transcription underlined at the cursor* and then
`commit_string` the final text — the behaviour every commercial dictation app
has and no Linux OSS tool does. It also side-steps keymaps entirely: you commit
a UTF-8 string, not keycodes, so Unicode is free.

The costs are decisive for `mavor`'s target:

- Only reaches apps with an active `zwp_text_input_v3`. That excludes
  Chromium/Electron on wlroots and all XWayland — i.e. a large share of a
  typical desktop **[DOC 2026-08-15]**.
- An input method is *exclusive per seat*: while `mavor` holds the
  `zwp_input_method_v2`, fcitx5/ibus cannot. A user with a CJK IME could not
  run both. Grabbing the input method only while recording is possible but
  racy **[INF]**.
- **No maintained Go Wayland client library exists.** `rajveermalviya/go-wayland`
  (136 stars, the only serious one) is **archived, last commit 2023-01-30**
  **[API 2026-08-15]**. Remaining candidates are toys (`xogas/wayland`,
  `bnema/wlturbo`, 0 stars each). So this means hand-rolling the wire protocol,
  or cgo against `libwayland` + generated protocol code.
- Prior art is thin: `psynyde/wl-ime-type` (Zig, 2 stars) was **archived
  2026-02-17** and moved to `zw-type` (1 star) **[API 2026-08-15]**. Nobody has
  shipped an IME-based dictation tool.

**Verdict: rejected for now, revisit if partial-results streaming becomes the
headline feature.** The cost is a from-scratch Wayland client in a language
with no library support, and the payoff does not cover Electron or XWayland.
Record it as the *only* mechanism that can do preedit, so the decision is
re-openable.

### 1.8 What the field actually uses in 2026

| Tool | Injection |
|---|---|
| voxtype | driver chain: wtype → eitype → dotool → ydotool → wl-copy → xclip |
| Handy | platform-native (see §5) |
| nerd-dictation | `xdotool` on X11, `ydotool`/`dotool` on Wayland |
| numen | `dotool` (same author) |
| whisper-overlay | wtype-style virtual keyboard |
| Blurt | GNOME Shell extension — injects via the Shell, no external tool |
| OpenWhispr | xdotool/ydotool with clipboard fallback (and the race bug) |

The consensus shape is unambiguous: **a prioritized driver chain with a
clipboard terminal fallback**, not a single tool. That is the single most
copyable architectural idea in this whole document.

---

## 2. Audio capture

### 2.1 The options

Measured in a containerized Linux environment against the host's PipeWire (server reports
`PulseAudio (on PipeWire 1.6.6)`, `pw-record` linked against libpipewire 1.6.8)
**[LOCAL 2026-08-15]**:

| Option | Command / API | 16 kHz mono s16 WAV? | Device pinning | Notes |
|---|---|---|---|---|
| `parec` (status quo) | `parec -d <src> --format=s16le --rate=16000 --channels=1 --file-format=wav out.wav` | verified | `-d/--device`, incl. `@DEFAULT_SOURCE@`, `@DEFAULT_MONITOR@`; also `PULSE_SOURCE` env | needs `pulseaudio-utils`; SIGINT flushes the header |
| `pw-record` | `pw-record --target <node> --rate 16000 --channels 1 --format s16 out.wav` | verified | `--target` takes `object.serial` **or** `node.name`; `-P/--properties` for arbitrary node props; `--latency` | ships with `pipewire` itself; no Pulse shim needed |
| `ffmpeg` | `-f pulse -i <src>` / `-f pipewire` | yes | yes | 60 MB dependency for a WAV writer |
| native Go | see below | — | — | see verdict |

Both `parec` and `pw-record` were run against a real source in that same environment and
both produced correct `RIFF … 16 bit, mono 16000 Hz` files, both flushing the
header on SIGINT **[LOCAL 2026-08-15]**.

**Verdict on `parec` vs `pw-record`: switch the default to `pw-record`, keep
`parec` as a configurable alternative.** Reasons: `pw-record` is part of
`pipewire` (which is already required) rather than the PulseAudio compat
package; `--target` accepts a stable `node.name` while Pulse source names are a
shim-level artifact; and `-P/--properties` gives access to `target.object`,
`node.latency`, and stream metadata that show up usefully in `pw-top`/Helvum.
This is a low-risk change because `internal/audio` already has the
`CommandFunc` seam — it is one function plus a config key.

### 2.2 Native PipeWire from Go — don't

- `bnema/purego-pipewire` — "Go PipeWire bindings via purego, without cgo",
  created 2026-04-11, **3 stars**, 4 open issues **[API 2026-08-15]**. Actively
  developed but nowhere near load-bearing.
- `ik5/gopipewire` — a mirror, not a maintained binding.
- The realistic cgo options are `gen2brain/malgo` (miniaudio, 423 stars, pushed
  2026-05-13) which reaches PipeWire via its PulseAudio/ALSA backends, or
  `jfreymuth/pulse` (pure-Go PulseAudio *protocol* client, 101 stars, pushed
  2026-07-28) which speaks the native Pulse protocol to PipeWire's
  `pulse-server` module — no cgo, no `parec` binary.

**Verdict: rejected for v-next; `jfreymuth/pulse` is the shortlist entry if a
subprocess ever becomes the bottleneck.** The subprocess model costs one
`fork/exec` per dictation (single-digit ms) and buys crash isolation and a
trivially mockable seam — `internal/audio` already exploits that. The one thing
a native capture would unlock is **live input level for the overlay and
client-side VAD**, which a WAV-file subprocess cannot provide. If that feature
lands, `jfreymuth/pulse` (pure Go, no cgo, keeps the static build) beats malgo.

### 2.3 Picking and pinning a device

`mavor` currently passes no device at all and inherits the Pulse default source.
On the dev rig that is visibly the wrong choice **[LOCAL 2026-08-15]** —
`pactl list sources short` shows a filter-chain graph with named virtual nodes:

```
41   desk            float32le 1ch 48000Hz
54   desk_denoised   float32le 1ch 48000Hz     <-- the one you want for dictation
177  alsa_input.pci-0000_00_1f.3.analog-stereo
96117 alsa_input.usb-BEHRINGER_X18_XR18_740ECE96-00.multichannel-input
Default Source: desk
```

The default source is the *raw* `desk`; the denoised node exists and is not
being used. **Adopt: add an `audio_device` config key** passed through to
`-d`/`--target`, with `""` meaning "server default". Empty-string default keeps
today's behaviour, so this is non-breaking.

Monitor/loopback sources come free with the same key: `@DEFAULT_MONITOR@` (Pulse)
or the `…monitor` node name (PipeWire) captures system output, which is how you
would transcribe a meeting or a video. Worth documenting even if not featured.

### 2.4 When the device disappears

Behaviour today: `parec` exits, `ParecRecorder.Stop` finds a zero-byte WAV and
returns `audio: empty WAV at …`, the daemon applies `EventTranscribeFailed` and
returns to `Idle` — with **no user-visible signal at all**, because the overlay
only renders `Recording`/`Transcribing`/`Hidden`. The user talks to a dead
recorder and gets silence.

The unavoidable ecosystem fact: when a USB source is unplugged, PulseAudio/
PipeWire moves the default *away* and does not move it back on re-plug
**[DOC 2026-08-15]**. So a pinned `audio_device` that vanishes will not
self-heal either.

Recommended handling, cheapest first:

1. **An `Error` overlay state** (red, "no audio device", 2 s auto-dismiss). This
   is the actual missing piece — a silent failure is worse than a wrong device.
2. Detect early: if the recorder's stderr mentions connection failure, or the
   WAV is under ~1 KB, report "no audio" rather than "transcription failed".
3. If pinning by name, fall back to the server default and log loudly, rather
   than failing hard.
4. Watching for hotplug (`pactl subscribe`, or a PipeWire registry listener) is
   a real solution but out of proportion for v-next. **Rejected for now.**

---

## 3. Hotkey and interaction model

### 3.1 sway *does* give you key release — the premise needs correcting

From the sway(5) man page **[DOC 2026-08-15]**:

```
bindsym [--whole-window] [--border] [--exclude-titlebar] [--release] [--locked]
        [--to-code] [--input-device=<device>] [--no-warn] [--no-repeat]
        [--inhibited] [Group<1-4>+]<key combo> <command>
```

- `--release` — "the command is executed when the key combo is released".
- `--no-repeat` — suppresses the auto-repeat re-firing of the press binding.
- `--locked` — fires even while the screen is locked.
- `--inhibited` — fires even when a client holds a keyboard-shortcuts inhibitor
  (relevant: some remote-desktop and VM clients grab everything).
- `--to-code` — resolve the keysym to a keycode so the binding survives a layout
  switch.

So the canonical push-to-talk config is two bindings, and this is exactly what
voxtype documents for sway **[DOC 2026-08-15]**:

```
bindsym --no-repeat $mod+grave exec mavor record start
bindsym --release   $mod+grave exec mavor record stop
```

**But `--release` is genuinely unreliable, and it is the specific unreliability
that hurts push-to-talk.** sway issue #6456, "`keysym --release` is unreliable",
open since 2021: *if any other key is pressed before the bound key is released,
the release binding never fires* **[DOC 2026-08-15]** — and the reporter's use
case is literally push-to-talk mic muting. Two PRs (#6616, #7602) reference it;
neither has landed. Issue #8803 (opened 2025-07-13, still open) asks for
`--release-always` because plain `--release` also loses to longer matching
combos.

For dictation this failure is common, not exotic: hold `$mod+grave`, talk, and
if you brush any other key the recorder never stops.

### 3.2 The options, ranked

| Option | Gets key-release | Extra requirements | Verdict |
|---|---|---|---|
| `bindsym` toggle (status quo) | n/a | none | **adopt — keep as default** |
| `bindsym` press + `--release` pair | yes, with sway #6456 caveat | none | **adopt as opt-in PTT**, document the caveat |
| Own evdev reader (`/dev/input/event*`) | yes, reliably | `input` group or udev rule; key leaks into the focused app | **shortlist** |
| `xdg-desktop-portal` GlobalShortcuts | yes | a portal backend that implements it | **rejected on sway** |
| libinput/`libei` InputCapture | yes | RemoteDesktop portal | **rejected on sway** |

**GlobalShortcuts is unavailable on wlroots, full stop.**
`xdg-desktop-portal-wlr` implements only Screenshot and ScreenCast **[SRC
2026-08-15]**, and the tracking issue (`emersion/xdg-desktop-portal-wlr` #240,
"Global shortcut portal support") has been open since 2022-09-30 with a second
request closed in 2023 **[API 2026-08-15]**. The portal is the right long-term
answer for GNOME/KDE portability, and the assertion is cheap to encode — but
today it buys nothing on the target platform.

**Evdev** is what voxtype falls back to, and its docs are candid about the two
costs **[DOC 2026-08-15]**: (i) `sudo usermod -aG input $USER` plus a re-login,
which is a meaningful install-time ask and a security surface (the `input` group
can read every keystroke on the system); (ii) *the hotkey is not consumed* — the
compositor still delivers it to the focused app, so `ScrollLock`/`Pause`/`F13`
are used precisely because they usually do nothing. This is a real regression
against `bindsym`, which swallows the key.

**Recommendation for `mavor`:** stay on the compositor-binding model and grow the
CLI surface instead of the hotkey surface. `mavor toggle` becomes
`mavor record start|stop|toggle|cancel`, and PTT is then a documentation problem, not
a code problem — the user writes two `bindsym` lines. This is voxtype's design
and it is correct: **the compositor already has a good hotkey engine; do not
ship a second one.**

### 3.3 Cancel / abort

There is currently no way to abandon a dictation. Every surveyed tool that has
one exposes it as a separate command (voxtype: `voxtype record cancel`, bound to
Escape) **[DOC 2026-08-15]**.

Two sway-native shapes:

```
# (a) a plain second binding
bindsym --release Escape exec mavor cancel        # fires globally — noisy

# (b) a sway mode, entered only while recording, so Escape is scoped
mode "dictating" {
    bindsym Escape exec mavor cancel; mode "default"
    bindsym $mod+grave exec mavor toggle; mode "default"
}
bindsym $mod+grave mode "dictating"; exec mavor toggle
```

(b) is the better UX — Escape only means "cancel" while the pill is up — but it
requires the compositor config to track daemon state, and the two can desync if
the daemon fails independently **[INF]**. Ship (a); document (b).

Cancel also needs an FSM change: today `EventToggle` in `Transcribing` is a
deliberate no-op, so a wedged transcription cannot be escaped. Add
`EventCancel` valid from both `Recording` (discard the WAV) and `Transcribing`
(kill the whisper process via the existing `exec.CommandContext` ctx).

### 3.4 Interaction affordances the field has and `mavor` lacks

- **Auto-stop on silence.** The installed `whisper-cli` exposes
  `--vad --vad-model … --vad-min-silence-duration-ms N` **[LOCAL 2026-08-15]**,
  but that is post-hoc segmentation of an already-recorded file, not a reason
  to stop recording. Live auto-stop needs client-side VAD on the capture
  stream, which the subprocess-to-WAV model cannot do (§2.2). Note the coupling:
  *native capture is a prerequisite for hands-free stop.*
- **Max recording duration.** voxtype defaults to `max_duration_secs = 60`. A
  toggle-based tool with no cap can silently record for hours if the user forgets
  — cheap and worth adding.
- **Pause media while recording** (voxtype's "auto-pause music") — MPRIS
  `org.mpris.MediaPlayer2.Player.Pause` over D-Bus to whatever is playing, then
  resume. Prevents the mic picking up your own speakers **[DOC 2026-08-15]**.

---

## 4. Status UI

### 4.1 Layer-shell overlay — status quo

> [!NOTE]
> **Resolved, 2026-09-04.** The risk this section identified was acted on: the
> C library and its binding are both gone, replaced by a hand-written Wayland
> client in `internal/wayland`. The analysis below is kept because it is what
> drove that decision, and because its central observation — that the API
> surface is small enough for the dependency to be worth more than it costs —
> is exactly what made the replacement cheap.

`gtk4-layer-shell` (the C library, `wmww/gtk4-layer-shell`) was healthy: 329
stars, v1.3.0 released 2025-10-29, pushed 2026-08-12 **[API 2026-08-15]**.

The **Go binding was the fragile link**: `diamondburned/gotk4-layer-shell`, 1
star, last pushed 2024-01-09, and `go.mod` pinned the pseudo-version
`v0.0.0-20240109211357-6efa9f6dc438` — i.e. the tip of an effectively dormant
repo. The exposure was bounded — layer-shell's API surface is ~8 functions and
hasn't broken — but a GTK4 or layer-shell ABI change would have landed on an
unmaintained shim **[INF]**.

Two costs turned out to matter more than the maintenance risk. The binding is
cgo, which forbade cross-compilation and static linking, and so blocked every
distribution channel except building from source; and `libgtk4-layer-shell-dev`
is absent from Ubuntu LTS, which broke CI outright. Speaking the protocol
directly costs 20 requests and 8 events.

Genuine strengths of the current design worth preserving: `top` layer, no
exclusive zone (windows don't reflow), `KeyboardModeNone` (never steals focus),
and a colour/shape change between states rather than a text change — all correct
choices for a heads-up indicator.

**Verdict: adopt/keep.** It is the only option that is unmissable, sub-frame
fast, and independent of any user configuration. Add an `Error` state (§2.4);
that's the gap.

### 4.2 Waybar module

The pattern the field converged on **[DOC 2026-08-15]**: the daemon writes its
state to a file (voxtype: `$XDG_RUNTIME_DIR/voxtype/state`) and exposes a
follow-mode status command; waybar runs it as a `custom/` module with
`"return-type": "json"`, receiving `{"text":…, "tooltip":…, "class":…}` and
styling `class` with CSS (a pulse animation on `.recording`). Updates arrive
either by streaming or by `SIGRTMIN+N`.

`mavor` already has 90% of this — the IPC server answers `status` — but it is
poll-only, and there's no state file for a shell one-liner.

**Verdict: shortlist — cheap and complementary, not a replacement.** Add
`mavor status --follow --format=json` (long-lived, emits on every FSM transition
via the existing `Machine.Subscribe`) plus a state file. The overlay answers
"is it recording *right now*, while I'm looking at my text"; waybar answers "is
the daemon alive and which model is loaded". Different questions.

### 4.3 libnotify / desktop notifications

**Verdict: shortlist for *errors and results*, rejected for state.** A
notification for "recording started" is latency-jittery (it goes through the
notification daemon), can be missed under Do-Not-Disturb, and stacks up in
history — wrong tool for a 2-second transient. But it is the right tool for two
things `mavor` cannot currently surface at all: (a) an error the user must act on
("no audio device", "model missing"), and (b) optionally the transcript text
itself, which gives a free scrollback of what you dictated via the notification
history. voxtype splits exactly this way — `on_recording_start`/`on_recording_stop`
default off, `on_transcription` default on **[DOC 2026-08-15]**. Implement via
`notify-send` (no new Go dependency) or `godbus` to
`org.freedesktop.Notifications`.

### 4.4 Audible cues

voxtype ships three sound themes (`default`, `subtle`, `mechanical`) with a
volume control, firing on record start/stop **[DOC 2026-08-15]**.

**Verdict: adopt, opt-in, default off.** A short start/stop tick is the only
feedback channel that works when you are looking at the *keyboard* or at another
monitor, and it is what makes push-to-talk feel like a physical button. Costs
one `pw-play`/`canberra-gtk-play` exec. Default off because it is an audio tool
— chirping into a recording is a foot-gun, and the cue plays out of the speakers
the mic can hear.

### 4.5 Accessibility

Honest state of the art: **a layer-shell surface is not announced to a screen
reader.** GTK4 talks to AT-SPI directly, but AT-SPI is X11-era D-Bus plumbing
and the Wayland-native replacement (the "Newton" protocol work across GTK,
mutter and Orca) is still landing and is GNOME-centric **[DOC 2026-08-15]**.
There is no wlroots story for exposing an unfocused overlay's state to Orca.

Practical consequences for `mavor`:
- A blind user gets **nothing** from the current overlay. The audible cue (§4.4)
  is not a nice-to-have for that user, it is the *entire* status channel.
- Do not rely on colour alone to distinguish `Recording` from `Transcribing` —
  the current design already varies text and shape as well, which is correct for
  colour-blind users.
- The notification path (§4.3) does reach Orca, since notifications are
  announced. That is another argument for `on_transcription` notifications.

---

## 5. The OSS dictation field

_(Survey completed 2026-08-15. Star counts and dates are point-in-time — see
"Fast-moving" below.)_

### 5.1 Comparison

| Tool | Stack | Injection | ASR backend | Activation | Status UI | Maint. (checked 2026-08-15) |
|---|---|---|---|---|---|---|
| **mavor** (this project) | Go, cgo/GTK4 | wtype + wl-copy (always both) | whisper.cpp CLI | sway `bindsym` toggle → IPC | layer-shell pill | active (this repo) |
| **voxtype** | Rust | chain: wtype→eitype→dotool→ydotool→clipboard | 7 engines (whisper.cpp, Parakeet, Moonshine, SenseVoice, Paraformer, Dolphin, Omnilingual) | compositor binding (toggle **and** PTT) + evdev fallback | notifications, waybar state file, waveform OSD, sound themes | very active — 1.1k★, created 2025-11-28, v1.0.0-rc1 2026-06-04, commits daily |
| **Handy** | Rust + Tauri | platform-native | whisper / Parakeet, local | PTT + toggle | tray/GUI | very active — 29.6k★, pushed 2026-08-15, MIT |
| **nerd-dictation** | Python | `xdotool` (X11), `ydotool`/`dotool` (Wayland) | VOSK | `begin`/`end` subcommands from a WM binding | none (CLI) | slowing — 1.9k★, pushed 2025-10-10, GPL-3.0 |
| **numen** | Go | `dotool` (same author) | VOSK | always-on voice *commands* | none | see §5.2 |
| **whisper-overlay** | Rust | virtual-keyboard | whisper (realtime streaming server) | global PTT | **live overlay with partial text** | stale — 88★, pushed 2024-07-26 |
| **Blurt** | JS (GNOME Shell ext.) | via GNOME Shell itself | whisper.cpp | Shell hotkey | Shell indicator | maintained — 108★, pushed 2026-05-07, GPL-3.0 |
| **Speech Note / dsnote** | C++/Qt (Flatpak) | app-internal (not system-wide) | many (whisper, VOSK, Coqui, …) | in-app | full GUI | active — 1.6k★, pushed 2026-07-25, MPL-2.0 |
| **OpenWhispr** | Electron | xdotool/ydotool + clipboard | whisper | hotkey | GUI | active; has the clipboard-restore race (#240) |

### 5.2 What to steal, ranked by value-per-line

1. **A prioritized injection driver chain with clipboard terminus** (§1.8).
   Universal in the field; `mavor`'s `output.Dispatcher` interface is already the
   right seam — it needs a list of `Runner`s and a `driver_order` config key
   rather than a hard-coded pair.
2. **`cancel`** (§3.3). One FSM event, one subcommand, one bindsym line.
3. **Word-replacement table.** voxtype's `[text] replacements = { "vox type" =
   "voxtype" }` is a flat map applied post-transcription. This is the
   *injection-side* half of custom vocabulary and is trivial; the ASR-side half
   (whisper's `--prompt`, max `n_text_ctx/2` tokens, plus `--grammar` for GBNF
   command grammars — both present in the installed `whisper-cli` **[LOCAL
   2026-08-15]**) belongs to the sibling ASR doc. Do both; they fail
   differently.
4. **Spoken punctuation** ("comma" → `,`, "new line" → `\n`). Same post-pass.
   Note the interaction with §0(b) — once you *intentionally* emit newlines you
   must decide what wtype should do with them.
5. **Post-processing hook.** voxtype pipes the transcript through an arbitrary
   command (`command = "ollama run llama3.2:1b 'Clean up…'"`, with a timeout).
   One config key, unlocks LLM cleanup, custom formatters, per-user hacks —
   the highest leverage single feature in the survey.
6. **Transcript history + undo-last.** Nothing in the field ships undo; voxtype
   explicitly lists "No history/undo" as a limitation. A ring buffer of the last
   N transcripts (`mavor history`, `mavor again`) is genuinely differentiating, and
   "undo" is implementable as `wtype -k BackSpace` × len — crude, but it is what
   commercial dictation does.
7. **Max recording duration** and **pause-media-while-recording** (§3.4).
8. **Waybar state file + `--follow`** (§4.2), **error notifications** (§4.3),
   **audio cues** (§4.4).
9. **Live partial text.** whisper-overlay is the only OSS tool that shows
   streaming partial results, and it's been stale since 2024-07. This is the
   feature that separates OSS dictation from commercial, and it needs both a
   streaming ASR path (sibling doc) and either an overlay that renders it or an
   IME (§1.7). Treat as a v2 north star, not a v-next task.

### 5.3 voxtype is the project to watch

Created 2025-11-28, 1.1k stars, ~daily commits, MIT, and it is aimed at exactly
`mavor`'s target (Wayland compositors, push-to-talk, local whisper.cpp). It is
strictly ahead on plumbing — driver chain, seven ASR engines, meeting mode with
diarization and SRT/VTT export, TUI configurator, distro packages. `mavor`'s
defensible differences are the layer-shell overlay (voxtype has a waveform OSD
but the field mostly does notifications), the tiny Go codebase, and the
integration-test rig that spins up headless sway. Read its
`docs/USER_MANUAL.md` before designing any feature in §5.2 — the config schema
is a well-considered starting point.

---

## Fast-moving — verify before building

Everything here was true on **2026-08-15** and is expected to move.

- **wtype's maintenance status.** Frozen at v0.4 / 2022-01-27 with 42 open
  issues. If it gains a maintainer (or the open "use input-method if available"
  issue #5 is implemented) the §1.2 verdict changes. Re-check the last-commit
  date before writing any wtype workaround.
- **sway `--release` reliability.** Issues #6456 and #8803 are open with
  unmerged PRs (#6616, #7602). If #6456 lands, push-to-talk via `bindsym`
  becomes trustworthy and the evdev shortlist entry can be closed out.
- **GlobalShortcuts on wlroots.** `xdg-desktop-portal-wlr` #240, open since
  2022-09-30. The day it merges, portal-based hotkeys become the portable
  answer.
- **Go Wayland client libraries.** `rajveermalviya/go-wayland` archived
  2023-01-30; nothing has replaced it. A maintained successor would reopen the
  IME question (§1.7).
- **`bnema/purego-pipewire`** (3★, created 2026-04) and **`jfreymuth/pulse`**
  (101★). If either becomes load-bearing, native capture + live level metering +
  client-side VAD become cheap (§2.2).
- **`diamondburned/gotk4-layer-shell`** — pinned pseudo-version from
  2024-01-09, 1 star. Watch for breakage on the next GTK4 or gtk4-layer-shell
  major.
- **voxtype's feature set.** v1.0.0-rc1 was cut 2026-06-04 and it ships several
  times a month. Anything in §5.2 may already be different. Also re-check its
  stated limitations (no streaming, no VAD, no history/undo) — those are the
  gaps `mavor` could own, and they may close.
- **Chromium Wayland IME.** `--enable-wayland-ime=v3` is experimental and
  text-input v3.2 protocol work is in flight. Chromium shipping stable
  text-input-v3 would materially improve the IME option's coverage.
- **Handy** (29.6k★) is moving fast and cross-platform; re-check what it does on
  Linux specifically before concluding `mavor` has a niche.

---

## Open Questions

**1. Does `mavor` want to be a Wayland dictation tool, or a *sway* dictation tool?**

Nearly every §1 verdict hinges on this. If sway-only, `wtype` + `bindsym` +
layer-shell is a coherent, dependency-light stack and the driver chain is
over-engineering. If cross-compositor, the driver chain, `eitype`, the portal
assertions, and a non-layer-shell status fallback all become necessary, and the
project roughly doubles in surface area. voxtype has already taken the
cross-compositor position and is ahead there.

_Leaning:_ stay sway-first and explicitly say so in the README, but land the
driver-chain *interface* (§1.8) because it also fixes the §0 bugs and costs
little. Add drivers only when someone asks.

> **Answer:**

**2. Push-to-talk: ship it knowing sway `--release` is flaky, or hold out?**

PTT is the single most-requested dictation affordance and the two-`bindsym`
recipe is free to document. But sway #6456 means it will silently fail to stop
whenever the user brushes another key mid-hold — a bad first impression that
looks like `mavor`'s bug. The alternative (own evdev reader) costs an `input`-group
install step, a security surface, and a hotkey that leaks into the focused app.

_Leaning:_ ship `mavor record start|stop|cancel` and document the PTT bindings
with the caveat spelled out, plus a `max_duration_secs` cap so a missed release
can't record forever. Defer evdev.

> **Answer:**

**3. Where should custom vocabulary live — the prompt, a replacement table, or
an LLM post-pass?**

Three mechanisms with different failure modes: whisper's `--prompt` biases
decoding (helps homophones, costs context tokens, unpredictable); a replacement
table is deterministic but only fixes what you enumerate; an LLM post-pass fixes
everything and adds latency, a dependency, and occasional hallucination. They
compose, and the sibling ASR doc owns the first one.

_Leaning:_ replacement table first (10 lines, zero latency, zero deps), plus a
generic `post_process.command` hook so the LLM option is the user's choice
rather than ours.

> **Answer:**

**4. Is the transcript sensitive enough to change the clipboard default?**

`mavor` unconditionally copies every transcript to the clipboard, where `cliphist`
et al. will persist it to disk. For dictation that includes passwords,
medical or personal content, that is a durable leak the user didn't ask for.
`wl-copy --sensitive` exists as a hint, and a `clipboard = always|on-failure|never`
key is easy — but "it's always on the clipboard" is also a genuinely nice
property of the current design.

_Leaning:_ keep copy-always as the default (it is the safety net when injection
fails), add `--sensitive`, and add the config key for people who want it off.

> **Answer:**

---

## Sources / see also

**Text injection**
- [atx/wtype](https://github.com/atx/wtype) — the tool `mavor` uses; source read
  for the keymap-synthesis and `mbstowcs` behaviour. Note last commit 2022-01-27.
- [wtype issue #62](https://github.com/atx/wtype/issues/62) — XWayland windows
  receive nothing; open, no workaround.
- [wtype issue #31](https://github.com/atx/wtype/issues/31) /
  [#71](https://github.com/atx/wtype/issues/71) — the Chromium/Electron
  character-corruption family.
- [wtype issue #45](https://github.com/atx/wtype/issues/45) — the canonical
  "compositor does not support the virtual keyboard protocol" thread (GNOME/KDE).
- [mutter issue #4124](https://gitlab.gnome.org/GNOME/mutter/-/issues/4124) —
  GNOME's open tracking bug for virtual-keyboard-v1; why wtype can't work there.
- [ReimuNotMoe/ydotool](https://github.com/ReimuNotMoe/ydotool) — uinput
  injection, AGPL, needs a daemon.
- [~geb/dotool](https://sr.ht/~geb/dotool) — uinput injection that respects an
  xkb layout via `DOTOOL_XKB_LAYOUT`; written for Numen, written in Go.
- [Adam-D-Lewis/eitype](https://github.com/Adam-D-Lewis/eitype) — libei/EI
  typing CLI; the GNOME/KDE path.
- [libei documentation](https://libinput.pages.freedesktop.org/libei/) — what
  EI/EIS is and the `liboeffis` + RemoteDesktop-portal handshake.
- [input-method-unstable-v2](https://wayland.app/protocols/input-method-unstable-v2)
  and [virtual-keyboard-unstable-v1](https://wayland.app/protocols/virtual-keyboard-unstable-v1)
  — protocol text. **Treat the "implementations" tables as unreliable**; they
  contradict the compositors' own issue trackers.
- [Chromium Ozone/Wayland: the last mile](https://nickdiego.dev/blog/chromium-ozone-wayland-the-last-mile-stretch/)
  — why Chromium has no usable IME on wlroots.
- [wl-clipboard `src/wl-copy.c`](https://github.com/bugaevc/wl-clipboard/blob/master/src/wl-copy.c)
  — the fork-and-keep-stderr behaviour behind bug §0(a); also documents
  `--paste-once`, `--foreground`, `--sensitive`.

**Audio**
- [pw-cat(1) / pw-record](https://docs.pipewire.org/page_man_pw-cat_1.html) —
  `--target` semantics (`object.serial` or `node.name`), `--properties`,
  `--latency`.
- [jfreymuth/pulse](https://github.com/jfreymuth/pulse) — pure-Go PulseAudio
  protocol client; the shortlist entry for native capture without cgo.
- [bnema/purego-pipewire](https://github.com/bnema/purego-pipewire) — Go
  PipeWire bindings via purego; too young to depend on (3★, created 2026-04).
- [gen2brain/malgo](https://github.com/gen2brain/malgo) — miniaudio cgo
  bindings; the mature-but-cgo alternative.

**Hotkeys**
- [sway(5)](https://man.archlinux.org/man/sway.5.en) — authoritative flag list
  for `bindsym`: `--release`, `--no-repeat`, `--locked`, `--inhibited`,
  `--to-code`, `--input-device`.
- [sway issue #6456](https://github.com/swaywm/sway/issues/6456) — `--release`
  doesn't fire if another key was pressed first; open since 2021, PTT is the
  reporter's exact use case.
- [sway issue #8803](https://github.com/swaywm/sway/issues/8803) — request for
  `--release-always`; open since 2025-07-13.
- [xdg-desktop-portal-wlr](https://github.com/emersion/xdg-desktop-portal-wlr) —
  README states Screenshot + ScreenCast only; [issue #240](https://github.com/emersion/xdg-desktop-portal-wlr/issues/240)
  is the GlobalShortcuts request, open since 2022.

**Status UI**
- [wmww/gtk4-layer-shell](https://github.com/wmww/gtk4-layer-shell) — the C
  library (healthy, v1.3.0 2025-10-29), vs
  [diamondburned/gotk4-layer-shell](https://github.com/diamondburned/gotk4-layer-shell)
  — the Go binding `mavor` pins (1★, dormant since 2024-01).
- [Waybar custom module docs](https://github.com/Alexays/Waybar/wiki/Module:-Custom)
  — `return-type: json`, `signal`, and the `class`-driven CSS pattern.
- [Newton: Wayland-native accessibility](https://blogs.gnome.org/a11y/2024/06/18/update-on-newton-the-wayland-native-accessibility-project/)
  — why an unfocused layer-shell surface is invisible to Orca today.

**The field**
- [peteonrails/voxtype](https://github.com/peteonrails/voxtype) and its
  [USER_MANUAL.md](https://github.com/peteonrails/voxtype/blob/main/docs/USER_MANUAL.md)
  — the closest peer project and the best single source for compositor
  keybinding recipes, driver-chain design, and config schema.
- ["Voice Dictation on Linux Wayland: push-to-talk on COSMIC"](https://codeshrew.github.io/ai-lab-notes/posts/2026-02-11_voice-dictation-cosmic-wayland/)
  (2026-02-11) — field report of wtype emitting digits on COSMIC and the
  ydotool workaround.
- [cjpais/Handy](https://github.com/cjpais/Handy) — 29.6k★ cross-platform
  Rust/Tauri dictation; the mainstream competitor.
- [ideasman42/nerd-dictation](https://github.com/ideasman42/nerd-dictation) —
  the long-standing Python/VOSK tool; the `begin`/`end` subcommand pattern
  `mavor` should copy.
- [oddlama/whisper-overlay](https://github.com/oddlama/whisper-overlay) — the
  only OSS tool with live partial-text overlay; stale since 2024-07 but the
  reference design for streaming UI.
- [QuantiusBenignus/blurt](https://github.com/QuantiusBenignus/blurt) — GNOME
  Shell extension route: no injection tool needed because the Shell types for you.
- [mkiol/dsnote (Speech Note)](https://github.com/mkiol/dsnote) — the
  batteries-included Flatpak app; useful for model-management UX ideas.
