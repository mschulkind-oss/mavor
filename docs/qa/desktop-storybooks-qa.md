---
status: accepted
stage: CURRENT
---

# Desktop storybook verification

Verified 2026-10-01 on detached `ea440c8` plus uncommitted storybook changes.
The [plan](../design/desktop-storybooks-plan.md) and
[research](../research/desktop-storybooks.md) explain scope and staging decisions.

## Commands and results

Commands ran from this isolated worktree, without branches, delegation, host
service changes, or host installation. Implementation-phase diagnostics were retained here:

```text
/workspace/.yolo/durable/storybooks/
```

| Check | Observed result |
| :--- | :--- |
| `just storybook-nix` | Pass; nine 1920 × 1080 full Sway frames plus nine 1920 × 180 crops |
| `just storybook-gnome-nix` | Pass; seven real 1280 × 720 Shell frames; report regressions and missing-Shell cleanup test pass |
| Pinned `just test-gnome-x11 -v` | Pass; production focus-safe copy, native Paste, overview, deadlines, crash/loss cleanup and supervisor regressions |
| Selected pinned Sway regression command below | Pass; 13 tests, including black-baseline, ordinary overlay/preview, frame rejection, report labels, prerequisite and cleanup checks |
| `just check` | Pass; format, static analysis, tagged type checks, all unit packages |
| `just done` | Pass; “Quality gate passed. Ready to commit.” |
| Markdown rendering check | `uvx vantage-check` passes on all five changed Markdown files |
| Local HTML link/image validation | Both reports resolve every local image and link; seven GNOME scene metadata entries preserve copy-only text equality |
| Process audit | All recorded GNOME worker/owner PIDs and Sway staging PIDs absent from `/proc`; supervisors report private groups reaped |

Pinned acceptance and ordinary Sway checks:

```bash
export MAVOR_GNOME_ARTIFACTS=/workspace/.yolo/durable/storybooks/g
env -u LD_LIBRARY_PATH \
  NIX_BUILD_SHELL="$(readlink -f "$(command -v bash)")" \
  nix-shell test/gnome/environment.nix --run 'just test-gnome-x11 -v'
env -u LD_LIBRARY_PATH \
  NIX_BUILD_SHELL="$(readlink -f "$(command -v bash)")" \
  nix-shell test/gnome/environment.nix --run \
  'go test -tags=integration ./test/integration/... -count=1 -v -run "Test(StagedFrame|Staging|StorybookReport|HarnessStarts|OverlayDoesNot|OverlayWithout|WaveformReaches|RecordingPill|TranscribingReaches|PreviewText|PreviewStays|WaylandClientPaints)"'
```

## Visual evidence

Read actual compositor PNGs and Chromium-rendered HTML screenshots, not offline
composites. Representative generated outputs, relative to the worktree:

```text
test/reports/ui-storybook.html
test/reports/screenshots/05_recording-55_full.png
test/reports/screenshots/05_recording-55_crop.png
test/reports/gnome-storybook.html
test/reports/gnome-storybook.json
test/reports/gnome-screenshots/02-copy-only.png
test/reports/gnome-screenshots/03-manual-paste.png
test/reports/gnome-screenshots/06-overview.png
test/reports/gnome-screenshots/07-return-paste.png
```

- **Sway:** real wallpaper surrounds a 900 × 500 floating editor; waveform pill
  remains unobstructed above it. Crop excludes staging and still shows the overlay.
- **GNOME:** Shell top bar and mature overview are visible. The native editor
  displays exact fixtures before/after explicit Paste. Copy-only scenes retain
  ready/prior text. The test clears the editor before each Paste; production
  copy never edits it. No mavor HUD or notifications are fabricated.
- **Automated frame evidence:** every full frame requires thousands of teal,
  blue, and bright pixels, plus mapped native-window geometry. The GNOME
  decoder checks dimensions and real PNG pixel data. Wallpaper without an
  editor fails a permanent regression test.
- **HTML:** Chromium rendered both local files at 1440 × 1200 with linked
  images visible, readable captions, and reciprocal report links. Browser
  render PNGs are retained as `gnome-html.png` and `sway-html.png` in diagnostics.

## Repairs and limitations

The first Python regression run failed because the report module did not yet
exist. Generation exposed an off-screen Sway initial-size race: staging now
waits for final geometry. Native Shell startup banners are dismissed before
capture, with a null-banner guard; no captured pixels are edited.

The first ordinary Sway regression run hit Unix socket-length limits under
nix-shell's temporary prefix. The pinned environment now uses short disposable
runtime paths, without changing the ordinary harness. The failed log remains
as `sway-regressions-long-socket-failed.log` in diagnostics. The C++ runtime and
resolved bash path repair shell/library and Go-toolchain mismatches observed
while entering the pinned environment.

No full audio/model end-to-end suite or original known-failing wl-copy GNOME
focus diagnostics ran: these captures demonstrate fixture presentation and
production X11 clipboard dispatch, not recording, recognition, physical keys,
login/lock lifecycle, or GPU-free CI. Shell clocks and editor caret animations
make byte-identical screenshots an unsupported claim. Browser stderr reports
unavailable optional D-Bus services, but both HTML render commands exit zero.
Generated reports remain ignored and are not force-added. Implementation was
left uncommitted for review; finalization results follow.


## Finalization verification

Finalization reran `just format`, `just check`, both storybooks, and the complete
Sway integration suite in the pinned environment: **29 passed, one skipped**.
The skipped canned-WAV clipboard test had no reachable PulseAudio/PipeWire
server. The real Whisper test requires its additional `e2e` build tag and was
not enabled. Focus-safe GNOME acceptance and supervisor regressions passed;
report regressions and missing-prerequisite cleanup passed again. Existing
known-broken wl-copy diagnostic tests were unchanged and not run.

The measured versions are **GNOME Shell 50.4** and **Sway 1.12**. The pinned Nix
closure supplies GTK4, a C compiler, pkg-config, GLib, Python, xclip,
wl-clipboard, D-Bus, swaybg, Waybar, Grim, Shell/Mutter and Mesa driver paths.
Go and the existing speech build libraries remain jail dependencies. No jail
configuration or host services were changed. A standalone `sway --version`
initially triggered its wrapper's unavailable default bus configuration;
setting an explicit nonexistent private bus address suppressed that wrapper
startup and returned the version. Actual desktop generation already had its
own valid private bus and passed.

Fresh full recording, Paste and overview PNGs were inspected directly. Both
show the same visible teal/blue wallpaper and readable native editor. Sway's
red waveform remains clear above the editor; GNOME's actual overview includes
workspace thumbnails and search, without any fabricated mavor overlay. Earlier
Chromium HTML render evidence remains available; no new browser render was
performed in finalization. Fresh report references and scene metadata were
validated again, and recorded finalization staging processes were absent.

Final logs have the `final-` prefix in the diagnostics directory above.
`just done` and Markdown checks passed before the single source commit.
Ignored HTML, metadata and PNG outputs remain in this worktree for the parent.
Physical-keyboard dictation, real inference, full login/lock lifecycle and
GPU-free CI remain unverified.

## Main checkout integration

The parent integrated the storybook source above CPU recovery commit
`649aeb6`, then regenerated both reports in the main checkout with
`just storybook-nix` and `just storybook-gnome-nix`. The pinned full Sway suite
passed in 61.588 seconds; focus-safe GNOME acceptance and cleanup passed in
42.932 seconds. `just check`, `just done`, and Markdown validation of all seven
affected documentation files passed on the combined changes.

Direct reads of fresh recording, manual-Paste, and overview PNGs confirmed
visible wallpaper and the native editor on both desktops. Chromium rendered
both main-checkout HTML reports at 1440 × 1200; images, captions, and reciprocal
links were visible. The local artifact audit counted 18 Sway images and seven
GNOME images, resolved local links, and checked copy-only text equality against
capture metadata.
Its initial false alarm treated the existing hidden lightbox's empty image
source as a missing file; the private audit now recognizes that placeholder,
which receives its image when opened. No report or source test was weakened.

All nine recorded desktop, staging, dispatcher, and clipboard-owner processes
from the parent generation were absent from `/proc`. Fresh reports remain
ignored under the main checkout's generated-report directory. Parent gate,
artifact-audit, and browser evidence have a `parent-` prefix in the diagnostics
directory above. No host installation, daemon restart, or release occurred.
