---
status: accepted
---

# GNOME copy-only output: implementation findings

Source review on 2026-10-01 against `a2ba579` and this worktree:

- [Output selection](../../cmd/mavor/main.go) already separates paste from typing.
  Clipboard must be selected before the native constructor, which requires
  layer-shell through its Wayland connection.
- [Paste output](../../internal/output/paste.go) owns CLIPBOARD and PRIMARY only
  briefly, injects a chord, and restores selections. Reuse is rejected for
  delayed manual paste; use a separate dispatcher.
- [DefaultRunner](../../internal/output/output.go) avoids inherited-pipe waits
  for daemonized wl-copy. Preserve this behavior; ordinary backgrounding keeps
  ownership after launch. Do not capture child stderr/stdout through pipes.
- [wl-clipboard's manual](https://man.archlinux.org/man/wl-clipboard.1.en)
  describes focus-dependent fallback without data-control. A launch deadline
  cannot establish GNOME readiness or kill every forked descendant.

No external GNOME/version claims were independently reverified. No live GNOME
session is available. Sway tests establish only Sway behavior.
