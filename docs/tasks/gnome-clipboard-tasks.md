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
- [x] Independent review and final local verification; no scoped blocking findings.
- [x] Correct stale user-guide output diagram.
- [ ] Parent integration of the single final delivery commit and shared files.
- [ ] Live GNOME acceptance; see [QA](../qa/gnome-clipboard-qa.md).
