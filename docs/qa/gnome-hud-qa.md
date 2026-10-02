---
status: accepted
stage: CURRENT
verified: 2026-10-02
covers:
  - internal/overlay/
  - test/desktop/
  - test/gnome/
  - test/integration/
---

# GNOME HUD acceptance

**Measured:** isolated GNOME Shell/Mutter 50.4 and pinned headless Sway, against
work based on `f8c9f02`, repeated by the final owner before the single feature
commit. This is not full-login, physical hotplug or input-injection certification.

## Real presentation and visual parity

Both desktops display the actual production Overlay using one Go painter, one
shared twelve-state catalog and one Sway-derived HTML template. Genuine captures
come from Grim or Shell Screenshot. Full frames and top crops are retained in
ignored report directories. No comparison image is exported as a screenshot.

The shared image validator independently locates the screenshot canvas. It checks
receipt-driven label, width, color, waveform and preview pixels and searches the
full frame for duplicate changed HUD accents. Frame receipts acknowledge
[submission only](../design/gnome-hud.md#frame-acknowledgment-contract).
Wayland Screen is empty; GNOME Screen is X protocol space, not PNG space.
Native 1× captures measured `(480,40)-(1440,141)` on both desktops.

Permanent backend regressions reproduced the former acceptance of Recording/Error
swaps in both directions and duplicate HUDs outside the expected canvas. All four
assertions failed with the old color/preview-only checks and pass after wiring the
shared validator. Shared tests additionally reject missing/wrong waveform,
ordered distinct RMS substitutions, absent/wrong preview, queued receipts and a
contaminated Hidden baseline. Negative fixtures are in memory only.

Fresh backend captures cover recording, short/long/cleared preview, transcribing,
error, Hidden, showing again, fullscreen and workspace/overview exit. Receipt
refresh before capture avoids confusing an earlier animation phase with current
presentation. Fullscreen uses its own actual Hidden background. Separate tests
prove pointer delivery underneath the pill and preview, authorization rejection,
transport stall/retry, compositor loss and bounded construction/Close.

Personal inspection covered all twelve top crops on both desktops, representative
full desktop frames, browser dark/light, split/crop/full/lightbox views, and combined
HUD/clipboard captures. Recording has a wider red pill and waveform; Error is a
compact crimson label. Transcribing is amber without waveform. Long preview keeps
its tail with an ellipsis; cleared preview and Hidden restore wallpaper. Native
panel/decorations and pulse/clock timing differ; these are not falsely treated as
HUD-state distinctness. Chromium verified twelve loaded cards and working controls.

## Production clipboard and HUD coexistence

A dedicated supervised private session runs the real HUD child and production
X11 clipboard dispatcher together with a native Wayland GTK editor. Before either
component starts, the fixture selects its sentinel text. Across HUD mapping,
updates and clipboard replacement, continuous Shell focus events and native GTK
active events remain empty; editor text and selection stay unchanged; native
CLIPBOARD reads contain only final transcript fixtures; PRIMARY keeps the selected
sentinel. No native edit event is allowed before explicit fixture Paste.

Only an explicit fixture command invokes the editable widget's GTK Paste action.
It replaces the selection with the final transcript, never the distinct preview-only
sentinel. Closing/recreating the HUD does not disturb clipboard contents or focus.
Replacing and closing the production clipboard owner reaps its process; the
supervisor also reaps direct and adopted session children. Eight genuine combined
captures pass the same visual oracle, including final Hidden and a fresh recreated
HUD. This is not physical Ctrl+V or production key injection.

The separate supported X11 acceptance passed native delayed/repeated reads, GTK
Paste, PRIMARY preservation, focus/overview, cancellation, owner replacement/death
and compositor-loss cleanup. Native wl-copy diagnostics remain known unsupported
failures and were not used as a supported gate.

## Reproduction and observed gates

Use the [hash-pinned environment](../../test/gnome/environment.nix) and short
runtime paths. Run sequentially; recipes replace ignored generated reports.

```bash
export MAVOR_STORYBOOK_TMPDIR=/tmp/ghud-t
export MAVOR_GNOME_ARTIFACTS=/workspace/.yolo/durable/ghud/runs
# /tmp/ghud-t points to the durable ghud/t directory in this jail.
go test ./internal/overlay ./internal/config ./cmd/mavor ./test/desktop -count=1
go test -race ./internal/overlay -count=1
just test-gnome-overlay-nix
just storybook-nix
just storybook-gnome-nix
(cd test/gnome && python3 -m unittest discover -p 'test_*.py')
go test -tags=gnome ./test/gnome -run '^$'
env -u LD_LIBRARY_PATH NIX_BUILD_SHELL="$(readlink -f "$(command -v bash)")" \
  nix-shell test/gnome/environment.nix --run \
  'TMPDIR=/tmp/ghud-t just test-int -count=1 -v'
env -u LD_LIBRARY_PATH NIX_BUILD_SHELL="$(readlink -f "$(command -v bash)")" \
  nix-shell test/gnome/environment.nix --run \
  'TMPDIR=/tmp/ghud-t just test-gnome-x11 -v'
just build
just format
just check
just done
```

Durable logs use the `integration-` prefix under ghud/logs. Backend final,
X11, unit, backend-race, Python, tagged compile, both storybooks and the complete
Sway rerun passed. Python ran eleven tests. The complete Sway suite skipped
`TestCannedWAVReachesClipboard` because no PulseAudio/PipeWire server was reachable;
real microphone/inference acceptance is not claimed. This task does not restart
or install host services.

The first complete Sway run and one focused reproduction failed prior-selection
restoration in the existing Paste test. Temporary diagnostic logging showed both
snapshots correct and sequential wl-copy restoration taking hundreds of
milliseconds against the existing shared cleanup deadline. The diagnostic run
and complete rerun passed without changing output production or weakening the
test. Retained failure logs make the intermittent timing limitation explicit.
An earlier shared-package race run exceeded the shell timeout. Final acceptance
completed both backend and shared-package race tests (227.927s for the shared
package), with no race reports. Shared non-race regressions also passed.

Build copies the existing sherpa/ONNX libraries beside the executable. ELF dynamic
requirements and resolved dependencies contain no libX11 or GTK: XGB and D-Bus
remain pure Go. A relocated directory runs in the pinned environment. Without its
C++ runtime environment, the jail's relocated binary cannot find libstdc++; this
is the existing sherpa runtime prerequisite, not a newly self-contained binary
claim or a reason to add GTK/libX11. Cross-distribution execution was not tested.

## Geometry and lifecycle limits

Six real configurations passed: two virtual outputs, primary-only 2×, primary-only
fractional, mixed fractional/1× with primary change, rotated primary and restored
outputs. Geometry acceptance converts X protocol space using actual stage/capture
scale; it does not pretend those coordinates are already screenshot pixels.
Monitor/work-area changes are polled, not subscribed, and may hide the HUD for
roughly four seconds plus bounded failed retries. Fractional output is resampled,
not certified per-output-native crispness.

Virtual output removal/addition is not physical cable hotplug. Negative origins
have unit coverage only. Mirroring and physical layout mode 2 remain unverified.
Shell overview/secure UI is authoritative; no HUD-over-lock promise is made.
Recovery across a changed login with stale DISPLAY/cookie/bus is unsupported.
Full desktop lock/unlock, editors/terminals, clipboard-manager variations and
service restart still require live-user validation. The overlay and clipboard
checks do not certify recognition accuracy or preview emission.

The original wallpaper SHA-256 remains
`9419801e0c39a0db869c21a7ac4dec828ede0c4a18699d7f8530092a78074610`;
CPU recovery ancestor `649aeb6` and silence behavior were preserved.

## Final-owner landing evidence

All fresh final commands exited zero. Logs under durable ghud/logs use the
`final-` prefix; earlier failure/timeout logs remain retained, not rewritten.

- Unit: overlay/config/CLI/daemon/shared tests, uncached; shared package 22.496s.
- Race: `go test -race ./internal/overlay ./test/desktop -count=1 -timeout=14m`;
  overlay 3.337s, shared 227.927s, both passed.
- Real GNOME HUD/geometry/coexistence recipe: 149.599s; frozen Close 0.001s,
  frozen constructor failure 5.017s, six geometry cases, focus/input and reaping.
- Complete pinned Sway integration: 99.930s, passed with the same named audio
  skip. The existing Paste timing failure did not recur in this final run.
- Supported X11 clipboard: 43.394s, passed; unsupported wl-copy not gated.
- Both actual HUD reports regenerated: Sway 49.210s, GNOME 61.007s. Separate
  clipboard QA regenerated and passed (40.572s); eleven Python tests passed.
- Build and relocated directory execution passed in the pinned C++ environment;
  both sherpa libraries resolved beside the relocated executable. No new
  libX11/GTK dynamic dependency; no cross-distribution portability claim.
- Chromium loaded twelve cards on each report and exercised theme, filter,
  split/crop/full/lightbox controls. Final owner read matched actual frames/crops
  across all twelve states, dark/lightbox browser renders and combined preview/
  Hidden-after-Paste frames. Red recording with growing decibel-mapped waveform,
  amber Transcribing, compact crimson Error, fitted preview tail and restored
  Hidden background are visibly present on both native desktops.

The independent review's only nonblocking mismatch was documentation promising
a screenshot retry loop. The contract now states the implemented strategy:
fifty acknowledged steady-RMS frames, a 100ms settling wait, one actual capture
and fail-closed validation. This does not certify presentation from submission.
A permanent report regression supplies valid submitted receipts but no visible
HUD and verifies that neither HTML nor manifest is published. No oracle tolerance
was weakened and no production API or coordinates changed.

Final retained sessions under durable ghud/final/runs:

```text
HUD/lifecycle: hud-39zt08ri
Geometry: hud-f5yhkxns
HUD/X11 coexistence: hud-abcjn0ni
GNOME report: hud-sa12cbe6
Supported X11: x11-kwrlsb8x
Clipboard-only report: clipboard-qa-0mlljo5s
```

The final audit verified no recorded session/clipboard PIDs remain, supervisor
reaping, twelve-state manifest parity, strictly increasing submissions, exact
lossless 180px crops and empty production Wayland Screen. Non-Hidden report
canvas bounds remain independently measured `(480,40)-(1440,141)`. Reports and
build outputs remain ignored; regenerate with the commands above. The original
wallpaper hash and CPU-recovery ancestry were rechecked.

Final `just format`, `just check` and `just done` passed; the latter printed
“Quality gate passed. Ready to commit.” Markdown rendering checks passed on all
eleven changed documents. The published checker lacks planning/index rules;
stage/dependency consistency was reviewed manually, not claimed as an automated
index pass. `git diff --check` passed. No generated reports or build artifacts
were staged, and no host/main-checkout operations, branch changes or publishing
were performed.

## CI desktop staging repair (2026-10-02)

The CI storybook failure reproduced on Sway 1.9: the configured black
background covered a separately launched wallpaper client even after ten
seconds of real screenshot polling. Sway 1.12 passed the unchanged setup.
Staging now replaces Sway's own background through IPC rather than competing
with it. Ordinary tests still start black; the original subdued wallpaper,
teal/blue/editor thresholds, and shared HUD assertions remain unchanged.

Permanent tests failed before each repair: wallpaper ownership, delayed black
presentation, startup child cleanup, and bounded diagnostics. Both background
readiness checks now poll real frames with ten-second deadlines. Editor
children are supervised and reaped; startup cleanup registers before fallible
operations. Failure diagnostics share a separate one-second command budget,
record command errors without replacing the original cause, and are retained
separately from accepted report evidence by CI.

Fresh complete integration suites passed on Sway 1.9 (99.408s) and 1.12
(100.516s), each with the existing unavailable-audio-server skip. The unchanged
strict GNOME storybook passed on Shell/Mutter 50.4 (55.178s). Process, deadline,
and cleanup regressions passed ten race-detector repetitions. Actual full and
180px cropped captures were inspected on all three compositors: subdued
wallpaper, native light editor, panel clearance, recording bars, and preview
text remained visible. Quality, tagged-suite, report artifact/template, and
workflow syntax checks passed. Durable evidence uses the `landing-` prefix in
the CI desktop readiness scratch directory; generated reports stay ignored.

This is local evidence using older and current Nix compositors, not the exact
Ubuntu runner package/runtime combination. No GitHub rerun or green remote
result is claimed. Production model, preview, HUD, and output code were not
changed.
