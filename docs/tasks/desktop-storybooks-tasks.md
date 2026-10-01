---
status: accepted
stage: BUILT
next: "Parent integration and report regeneration"
---

# Desktop storybook execution

The [scoped plan](../design/desktop-storybooks-plan.md) owns the implementation
map; [QA](../qa/desktop-storybooks-qa.md) owns commands and results.

- [x] Test report escaping and capture errors before implementation.
- [x] Add shared local wallpaper and opt-in native editor presentation.
- [x] Add Sway-only storybook staging and genuine frame checks.
- [x] Add GNOME production-copy/manual-Paste scenes and Shell capture.
- [x] Add missing-prerequisite and process cleanup regressions.
- [x] Generate both ignored reports without cached test results.
- [x] Inspect compositor PNGs and preserve useful overlay closeups.
- [x] Run existing X11 acceptance and supervisor regressions.
- [x] Complete browser and final local gates; record their results in QA.

- [x] Finalize with full applicable Sway integration and focus-safe GNOME checks.
- [x] Preserve ignored generated reports and commit source changes once.

Finalization remains on detached HEAD; the parent owns integration and final report regeneration.
