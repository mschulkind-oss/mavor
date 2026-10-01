---
status: accepted
---

# GNOME clipboard implementation tasks

The [plan](../design/gnome-clipboard-design-plan.md) owns behavior.

- [x] Test normalization, literal text, helper count, deadlines/errors/cancellation.
- [x] Add dispatcher and select it before keyboard construction.
- [x] Test clipboard config round-trip and reject unknown driver names.
- [x] Make setup requirements and doctor checks driver-aware without test writes.
- [x] Explain config/scaffold/manual shortcuts and reconcile overlapping references.
- [x] Add real Sway persistence/repeated-read/PRIMARY/successive-transcript test.
- [x] Independent review and final local verification; GNOME focus blockers
  remain explicit in the [report](../reports/gnome-headless-qa.md).
- [x] Correct stale user-guide output diagram.
- [ ] Parent integration of the single final delivery commit and shared files.
- [x] Launch isolated real GNOME Shell 50.4 and prove native clipboard transfer;
  [research](../research/gnome-clipboard-research.md) records focus theft and
  overview/no-focus waiting, not a background-mode acceptance pass.
- [x] Build the [GNOME harness](../design/gnome-clipboard-design-plan.md#real-gnome-harness-handoff):
  poll compositor socket and Shell bus readiness under bounded deadlines;
  retain startup/focus and consumer protocol logs; bounded process cleanup.
  Source-helper protocol logs remain unavailable through DefaultRunner.
- [ ] Add a native Wayland consumer with deterministic read requests and continuous
  focus event reporting; exact UTF-8, delayed/repeated reads, successive copy,
  destination reactivation, PRIMARY preservation, and compositor-loss coverage.
  Native reads/focus/PRIMARY and destination reactivation are implemented;
  compositor-loss recovery beyond the frozen-compositor deadline remains open.
- [x] Test no-focus/overview separately from focused destination transfer; report
  transient source focus as a limitation, never as focus-safe delivery success.
- [x] Exercise the real mavor dispatcher against GNOME, including launch timeout
  and cancellation while the helper is pending; do not change silence filtering.
- [ ] Verify a GPU-free renderer path before requiring this on GPU-less CI.
- [ ] Decide whether to bake GNOME/GTK/Mesa dependencies after the permanent
  harness works. Any jail config edit requires configuring-the-jail and yolo check;
  transient Nix packages already enable the probe without a restart.
- [ ] Complete login-session/shortcut/lock acceptance; see
  [QA](../qa/gnome-clipboard-qa.md).

## Real-session implementation handoff

- [x] Add separate opt-in GNOME tag and recipe, with failed prerequisites
  reported as errors and tagged compilation in the ordinary gate.
- [x] Run real dispatcher, wl-copy, and native GTK transfer checks on Shell/Mutter
  50.4; retain focus assertions that fail rather than blessing helper focus.
- [x] Verify delayed/repeated reads, replacement, external PRIMARY consumption,
  frozen-compositor launch deadline, and failure-path process cleanup.
- [x] Run output unit tests, existing real-Sway clipboard persistence, and
  `just check`; preserve [results](../research/gnome-clipboard-research.md#permanent-real-session-regression-suite).
- [x] Establish and verify explicit X11 copy-only output through a supervised
  foreground xclip owner. Original Wayland focus diagnostics remain failing;
  the separate production X11 acceptance passes.
- [ ] Verify full desktop lifecycle and GPU-free CI. Do not infer either from
  the isolated dispatcher suite.

- [x] Repair worker forced-death cleanup with an external supervisor and verify
  real GNOME teardown after native transfer; process-only regressions also pass.

## Experimentally proven correction phase

Work is uncommitted against `10139e1`; parent owns landing and integration.

- [x] Add tests first for X11 owner lifetime/replacement/Close, literal stdin,
  failed startup, cancellation, and preserving a previous owner on failed launch.
- [x] Add explicit backend config/default/validation/scaffold/show round-trips;
  keep Wayland as default and reject X11 with injection drivers.
- [x] Require xclip plus desktop DISPLAY/authorization for X11 setup/doctor;
  preserve Wayland requirements, with no clipboard write during diagnostics.
- [x] Test actual production dispatcher in real GNOME with continuous destination
  focus, mature overview/no-focus, delayed/repeated/replacement native reads,
  external PRIMARY and editable GTK Paste action.
- [x] Verify launch/cancel against frozen XWayland, normal Close, connection loss,
  abrupt parent death, and reaping. Pin the measured Nix environment.
- [x] Rerun unchanged Wayland focus diagnostics and retain their failures.
- [ ] Parent review, final repairs/commit/integration and complete-desktop checks.

No jail configuration, deployment, or silence-filter changes were made.
[QA](../qa/gnome-clipboard-qa.md#production-x11-verification) owns final gate evidence.
