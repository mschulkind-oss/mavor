---
title: "Plan: the config surface rewrite"
author: "Matthew Schulkind"
date: 2026-09-05
status: ready
tags: [plan, config, models, cgo, preview, release]
summary: "Build hand-off for the config schema rewrite: file map, what to reuse, the traps that compile, build order, and everything that ships besides code."
---

# Plan: the config surface rewrite

**Design:** [`configuration-surface.md`](configuration-surface.md) (DECIDED, all five
rulings in its Decision Ledger) · **Status:** ready · Written against `f62c3e9`,
2026-09-05.

**Precedence.** The design wins on behavior — if this file implies different
behavior, this file is wrong. The tree wins on fact; if a symbol moved, follow the
tree and say so in the commit. This plan is advice and is the first thing to be
wrong. Never twist the code to match it.

## Map

| Path | Change |
| :--- | :--- |
| `internal/config/config.go` | the schema: nested structs per table, `Default()`, thread autodetect. `Resolve()` mostly deletes |
| `cmd/mavor/config_cmd.go` | template generated from `Default()`, not a second literal (`:13`) |
| `cmd/mavor/models_cmd.go` | whisper names prefixed, `Aliases` field deleted, new `Filename` field, one path resolver |
| `cmd/mavor/doctor.go` | `runSetup` (`:65`) becomes config-driven; new checks; GPU message (`gpu.go:165`) |
| `cmd/mavor/gpu.go` | `gpu_layers` → `auto`/`off`; report what loaded |
| `internal/speech/factory.go` | derive runtime + placement from the model; drop `GPULayers`/`Device` |
| `internal/speech/speech.go`, `supervisor.go` | delete the `-ngl` append (`speech.go:52`, `supervisor.go:119`) and `SupervisorConfig.Device` (`:41`) |
| `internal/speech/sherpa.go` | `resolveDecoding` (`:682`) reads the vocabulary table |
| `internal/daemon/daemon.go` | companion-model preview; `Config` (`:57`) gains a second transcriber; phrase mode keeps `:326` |
| `cmd/mavor/main.go` | wiring (`:174`, `:201`); drop the inline model path (`:225`) |
| `cmd/mavor/build_tags.go`, `internal/speech/sherpa_stub.go` | **deleted** — no `!sherpa` variant exists any more |
| `cmd/mavor/build_tags_sherpa.go`, `internal/speech/sherpa_cgo.go` | build tag removed; these become unconditional |
| `Justfile` | `build`/`bench` absorb the `-sherpa` recipes; `default_model`/`test_model` are catalog names (`:4`, `:6`) |
| `.goreleaser.yaml` | `CGO_ENABLED=0` (`:22`) goes; `$ORIGIN` rpath; archive carries the shared objects |
| `dist/pypi/src/mavor/cli.py` | extract the shared objects, not just the binary (`:76-84`) |
| `test/integration/harness.go` | `RunDaemon` writes the new config shape (`:406`, config at `~:423`) |

## Reuse

- **`speech.Factory(cfg, logger)`** (`factory.go:15`) stays the single construction
  point. Give it a second entry for the companion rather than a parallel path.
- **`StreamTranscriber`** (`streaming.go:13-25`) already exists and is detected by
  type assertion (`daemon.go:276`, again at `:422`). The companion is just a second value satisfying
  it — no new interface.
- **`buildKnownModels()`** (`models_cmd.go:373`) is the catalog index. Deleting
  `Aliases` (`:25`) is a field removal there, not a rewrite.
- **`Check{Name, Fn}`** (`doctor.go:16`) plus the literal slice at `doctor.go:31`
  is the whole doctor mechanism. New checks append there.
- **`ResolveSherpaModelDir`** (`sherpa.go:450`) already does sherpa path lookup with
  fallbacks. The new resolver wraps it rather than replacing it.
- `runSetup` (`doctor.go:65`) already skips a present config (`:74`) and a present
  model (`:113`). Idempotency is a generalization of what is there, not new work.
- Test state: `t.TempDir()` + `t.Setenv` inline per test. There is no fixture
  builder and no `testdata/`; do not introduce one for this.

## House style

- Errors: `fmt.Errorf("<pkg>: ...: %w", err)` with the package name as prefix at
  package boundaries (`config.go:251`, `factory.go:29`). No wrap helper exists.
- Logging: `log/slog`, a `*slog.Logger` field on the struct, nil-defaulted to
  `slog.Default()` at point of use (`speech.go:78`). Structured k/v args.
- Tests: stdlib only, no testify. Many small named `TestXxx`, not table-driven.

## Traps

- **`"ggml-"+name+".bin"` is duplicated in nine non-test places** across
  `factory.go:27,38`, `doctor.go:111,439`, `main.go:225`, `models_cmd.go:506,517`,
  `bench.go:22`, `sweep.go:120,172`. **Constraint:** §5 makes the catalog name and
  the filename different, so route every one through a single resolver. Change some
  and not others and you get "model not found" from only some code paths, with
  everything compiling.
- **Three release surfaces break on the cgo switch, two of them silently.**
  `.goreleaser.yaml:22` pins `CGO_ENABLED=0` and cross-builds `arm64` (loud, at
  build time). The Homebrew formula does `bin.install "mavor"` — one file (silent
  until a user runs it). `dist/pypi/src/mavor/cli.py:76-84` extracts only the
  `mavor` member from the tarball (same). `just check` passes through all three.
- **`Resolve()` clobbers an explicit model** when it equals the default string
  (`config.go:206`). Deleting `preset` must delete this branch too, or the new
  default model gets overwritten by whatever replaces it.
- **`runSetup` lives in `doctor.go:65`**, not a `setup.go`. Symptom of missing it:
  you write a new setup path and the `setup` subcommand still runs the old one.
- **`models_cmd_test.go:271-304, 574, 650`** assert every canonical name and alias
  resolves and that catalog length matches. These fail *by design* under §5 —
  rewrite to the new names, do not repair until green.
- **The daemon holds exactly one `Transcriber`** (`main.go:174` → `daemon.go` field).
  Nothing is a singleton, so a second instance is structurally fine, but every
  assumption of "the transcriber" needs reading before you add one.
- `Justfile:4,6` carry `base.en`/`tiny.en` as bare catalog names and feed
  `_ensure-model`; the rename reaches into the build recipes.

## Build order

Each step ends green and committable.

1. **`gpu_layers` → `gpu = auto|off`.** Delete both `-ngl` appends and
   `SupervisorConfig.Device`; fix the `doctor` advice at `gpu.go:165`. Independent
   of everything below. → `just check`
2. **Catalog rename.** Prefix the eleven whisper names, delete `Aliases`, add the
   explicit `Filename`, introduce the one path resolver and route all nine call
   sites through it. → `just check`
3. **Collapse the build to cgo** (§4). Delete `build_tags.go` and `sherpa_stub.go`,
   drop the tags from their siblings, fold `build-sherpa`/`bench-sherpa` into
   `build`/`bench`. Must precede step 5. → `just check && just build`
4. **The schema.** Nested structs, `Default()`, thread autodetect, runtime and
   placement derivation, template generated from `Default()`. → `just check`
5. **The preview.** Companion model, resolution rule (§6.2), phrase-mode fallback,
   config-driven idempotent `setup`, fatal-on-named-model-missing (§10.2). Largest
   step. → `just check && just test-int`
6. **Vocabulary.** The table → whisper prompt and sherpa hotwords; decoding method
   follows from it. → `just check`
7. **Docs.** → `just done`

## Ships with

- **Unit, by case** — the §10.1 table is the case list: both `words` and `file` set;
  vocabulary over whisper's 224-token cap; `threads ≤ 0`; `top_margin < 0`;
  `preview.source` equal to `model`; unknown key warns but does not fail; absent
  config file. Plus §10.2: named-model-missing is fatal, corrupt companion degrades.
- **The test that enforces the §2.1 drift never recurs:** scaffolded template parses
  to exactly `Default()`. This one is the reason the bug class dies.
- **Integration** — `test/integration/transcribe_test.go` (`TestCannedWAVReachesClipboard`)
  drives a real config through `RunDaemon` to the clipboard. Extend it for the new
  config shape; it is the test that would catch config→daemon wiring breaking.
  Add one asserting a config naming an absent model exits non-zero with the model in
  the message.
- **Rewrite, do not repair:** `internal/config/config_test.go:9+`
  (`TestDefaultsAreReasonable` asserts `Model == "base.en"`, `Engine == "cli"`),
  `cmd/mavor/models_cmd_test.go` (see Traps), `internal/speech/factory_test.go`
  (engine dispatch, and `:120` asserts the stub error that no longer exists).
- **Docs by path** — `docs/user-guide.md` §7 is the annotated reference and has 38
  hits on old keys; `README.md` (8), `docs/quickstart.md` (2),
  `docs/reference/how-mavor-works.md` (4), `docs/choosing-a-model.md` (3).
  `AGENTS.md` "Build Tags" and "Justfile Recipes" both describe the pre-cgo world.
- **Non-code surfaces** — `.goreleaser.yaml`, the Homebrew formula block in it, the
  PyPI wrapper, `Justfile:4,6`, the `mavor config init` template, `doctor` output
  (§10.6 lists what it must report), and error text for a missing named model.
- **Norms** — `just check` before each commit; the pre-commit hook runs `just
  check-ci` and will reject otherwise. `docs/reports/model-benchmarks.md` is
  generated: rerun `just bench`, never edit.

## Cheap, and yours

Struct field order and whether the tables are named types or anonymous structs;
which of `preview`/`ducking` gets defined first; the wording of warnings. Read once
at startup, tested through `Default()`, invisible downstream.

## Stop and ask

- **The arm64 and packaging strategy** (blocks step 3's release half, not the code).
  cgo cannot cross-compile from the amd64 runner, so `arm64` needs native runners, a
  cross toolchain, or dropping the arch. It is externally visible, it changes the
  Homebrew formula from a one-file install to a library install with rpath, and the
  PyPI wrapper's archive contract changes with it. Steps 1–7 can all land before
  this is answered; a release cannot.

## Don't

- Don't add a compatibility shim for the old keys — §11 rejected it; unknown keys
  warn and `doctor` reports them (§10.1).
- Don't expose `decoding_method`, `sherpa_provider`, or a language-model key. §7 and
  §9 give the reasons; each is a decided no, not an omission.
- Don't make the companion model's failure fatal in general — only a model *named*
  in the config is fatal (§10.2). The distinction is the whole ruling.
- Don't touch `docs/design/active-window-context-and-vocabulary-prompting.md`. It
  adopts this key shape later; this change does not edit it.
