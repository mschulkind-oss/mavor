---
status: accepted
---

# Independent desktop storybook review

Reviewed 2026-10-01 on detached `ea440c8` plus the uncommitted implementation.
**Blocking findings: none.** No production/test edits or commits made in this
review. Implementation verification is recorded separately in the
[earlier QA evidence](../qa/desktop-storybooks-qa.md).

## Actual visual evidence

Read full compositor PNGs directly, not reconstructed desktop images:

- [Sway recording frame](../../test/reports/screenshots/05_recording-55_full.png):
  teal/blue geometric wallpaper surrounds the centered native editor. Its text
  is readable; the real red Go overlay and waveform sit above, without overlap.
  The [180-pixel crop](../../test/reports/screenshots/05_recording-55_crop.png)
  preserves Waybar and overlay while excluding the editor.
- [GNOME copy-only](../../test/reports/gnome-screenshots/02-copy-only.png):
  Shell top bar, visible wallpaper and native editor; editor still says ready.
- [GNOME Paste](../../test/reports/gnome-screenshots/03-manual-paste.png):
  editor visibly contains “Plan the afternoon walk. Bring a notebook.”
- [GNOME overview](../../test/reports/gnome-screenshots/06-overview.png):
  actual Shell search field, workspace thumbnails and window presentation;
  replacement text remains visible in the editor.
- [Return and Paste](../../test/reports/gnome-screenshots/07-return-paste.png):
  editor shows the overview fixture after returning. No fabricated mavor HUD.

Inspected both HTML sources and retained Chromium render images. Both pages
show their desktop images, readable captions and reciprocal report links.
Fresh artifact validation resolves all relative references: Sway has nine
PNG download links and 18 embedded PNGs; GNOME has seven linked images.
The shared wallpaper is deliberately simple, but visibly distinct from the
ordinary black test backdrop.

## Independent execution and code review

Re-ran both `just storybook-nix` and `just storybook-gnome-nix`: pass; nine Sway
states and seven GNOME scenes regenerated here. GNOME report regressions and
missing-Shell failure/cleanup checks also passed. Re-ran 13 selected Sway
regressions, including ordinary black-baseline, overlay layout, preview,
missing prerequisite, frame rejection and child cleanup: all passed.

Reviewed the actual tracked diff and new staging/report files. Sway staging
is invoked only by the storybook, not ordinary harness startup. Captures use
Grim; crops derive from captured pixels. GNOME launches private headless
Shell/Mutter, uses its screenshot service, and never composes a fake overlay.

GNOME requests the production X11 dispatcher's `Emit`, then invokes the native
editable GTK widget's `clipboard.paste` action. It does not directly set fixture
text as a substitute for clipboard transfer. The test clears the buffer first,
explicitly disclosed in both report and README. Copy-only scenes assert exact
unchanged editor text and no focus-loss/edit events. Replacement owners exit;
overview remains active while copying. No preview is presented as emitted text.

Private GNOME home/runtime/config directories and bus/display credentials
isolate the run. The supervisor kills and reaps the private worker group,
including adopted holders. All recorded PIDs from this review's GNOME run and
both Sway staging children were absent afterward. Generated reports remain
ignored; HEAD unchanged.

Durable review diagnostics:

```text
/workspace/.yolo/durable/storybooks/independent-gnome.log
/workspace/.yolo/durable/storybooks/independent-generation-sway.log
/workspace/.yolo/durable/storybooks/independent-sway.log
/workspace/.yolo/durable/storybooks/independent-artifact-audit.txt
```

## Limits and nonblocking observations

- Frame color/brightness checks are coarse evidence, not semantic recognition
  of an editor. Native-window geometry, exact text checks and visual inspection
  supply the additional evidence in this review.
- Native clocks/carets prevent byte-identical storybook images. Ordinary
  baseline/layout assertions remain separate and passed; byte determinism was
  not certified.
- The initial review-only link audit incorrectly treated the empty, dynamically
  populated lightbox image source as a missing file. Correcting that audit
  resolved it; no report change was needed.
- Inspected existing Chromium renders, rather than launching another browser.
  Full `just check`/`just done` were reported by implementation and not rerun
  here. This review does not certify physical-keyboard dictation, audio/model
  inference, a full login/lock session, or GPU-free CI.
