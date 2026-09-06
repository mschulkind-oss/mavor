---
title: "Twenty-nine keys, and the three that lie"
author: "Matthew Schulkind"
date: 2026-09-05
status: accepted
tags: [design, config, cli, models, sherpa, whisper, preview, streaming, cgo, gpu]
summary: "Redesign of mavor's config.toml: separate the model from where it runs, delete the keys that do nothing or actively break transcription, prefix every model name with its family, and make a dedicated streaming model the default source of the live preview."
vantage:
  status-chip: true
---

# Twenty-nine keys, and the three that lie

**Status:** DECIDED (2026-09-05). Nothing built; every open question is settled
and the Decision Ledger carries the five rulings. Evidence in [§2](#2-what-exists-today) verified against
the tree at `62db92c` on 2026-09-05.

**The short version.** `config.toml` exposes 29 keys. Three of them are wrong
rather than merely confusing: `gpu_layers` passes a flag `whisper-cli` does not
accept, so any non-zero value breaks every transcription; `device` is written
into a struct field nothing reads; and the scaffolded template disagrees with
the compiled defaults on two behaviors. The redesign separates two axes the
current `engine` key welds together — **which model** and **where its runtime
runs** — deletes the keys that were only ever aliases for one of those, gives
every model name a family prefix, and makes a small dedicated streaming model
the default source of the live preview instead of re-running the main model at
every pause.

**The most important section is [§3](#3-two-axes-welded-into-one-enum)** — the
runtime/placement split. Everything else in the proposal falls out of it.

**Reads with:**
[`active-window-context-and-vocabulary-prompting.md`](active-window-context-and-vocabulary-prompting.md)
(the vocabulary biasing this doc gives a config surface to, still in review),
[`../reports/model-benchmarks.md`](../reports/model-benchmarks.md) (every
performance number quoted here), [`../roadmap.md`](../roadmap.md) (the AMD GPU
workstream this doc opens).

---

## 1. Verdict and principles

Rewrite the schema in place, with no compatibility aliases. The project has not
shipped publicly, so there is no installed base to migrate and every line spent
on a deprecation path is waste.

Five principles, cited by number later:

**P1. A key exists only if a user could reasonably want a value other than the
one mavor would pick.** Threads, decoding method, and engine all fail this test:
there is a right answer, mavor can compute it, and exposing it only invites a
worse one.

**P2. Name the thing, not its implementation.** `mode = "streaming"` names an
inference strategy to describe whether text appears in an overlay. `base.en`
names a file that upstream happens to ship. Both are the implementation leaking
through.

**P3. One concept, one key.** `model` and `sherpa_model` are the same concept.
So are `preset` and `model`. So are `mode` and `streaming_strategy`.

**P4. Every knob states its default and its unit at the point of use.** A
commented-out line in the scaffolded file is documentation; a key with no stated
default is a hole.

**P5. `mavor doctor` is the second half of the config file.** The file says what
you asked for; `doctor` says what this machine will actually do with it. Any
setting whose effect depends on the hardware or the installed tools must be
reported there, and the config comments point at it.

---

## 2. What exists today

### 2.1 Three keys that are wrong, not merely confusing

**`gpu_layers` breaks transcription.** Both the one-shot path
([`internal/speech/speech.go#L52`](../../internal/speech/speech.go#L52)) and the
supervised server path
([`internal/speech/supervisor.go#L119`](../../internal/speech/supervisor.go#L119))
append `-ngl <n>` when `gpu_layers > 0`. whisper.cpp has no such flag. Verified
against the packaged binary on 2026-09-05:

```console
$ whisper-cli -ngl 99 -m model.bin
error: unknown argument: -ngl
```

whisper.cpp uses the GPU automatically when its build has a GPU backend, and
offers only `-ng` / `--no-gpu` to turn that off. So the key has exactly two
behaviors: `0` does nothing, and anything else makes every transcription fail.
Worse, `doctor` actively recommends the broken setting —
[`cmd/mavor/gpu.go#L165`](../../cmd/mavor/gpu.go#L165) tells a user with a
Vulkan build to "set `gpu_layers` in config.toml to offload".

**`device` is write-only.** `cfg.Device` is copied into `SupervisorConfig.Device`
at [`internal/speech/factory.go#L56`](../../internal/speech/factory.go#L56). That
field is declared at
[`internal/speech/supervisor.go#L41`](../../internal/speech/supervisor.go#L41)
and read nowhere. The documented values include `"rocm"`, which nothing in the
program has ever been able to act on.

**The scaffolded file disagrees with the compiled defaults.** `mavor config init`
writes a template that is a separate literal from `Default()`, and the two have
drifted:

| Key | `Default()` | `config init` template | `docs/user-guide.md` |
|---|---|---|---|
| `mode` | `streaming` | `batch` | `streaming` |
| `duck_audio` | `false` | `true` | `true` |
| `top_margin` | `8` | `32` (as the example) | `8` |

A user who runs `config init` silently gets different behavior from a user who
does not. Nothing enforces agreement, so this will drift again.

### 2.2 Keys that are aliases for other keys

**`preset` picks `model`, and gets it wrong.** `Resolve()` overrides the model
whenever it equals the default string
([`internal/config/config.go#L206`](../../internal/config/config.go#L206)):

```go
if c.Model == "" || c.Model == "base.en" {
```

So `preset = "fast"` together with an explicit `model = "base.en"` yields
`tiny.en`. The user wrote the model name and did not get it. Beyond the bug,
`preset` offers three points on a curve that `mavor models list` already presents
in full, with measured numbers.

**`streaming_strategy` has three values and two behaviors.** `auto` and
`vad_batch` take the same branch
([`internal/daemon/daemon.go#L326`](../../internal/daemon/daemon.go#L326)):

```go
if d.streamingStrategy == "vad_batch" || d.streamingStrategy == "auto" {
```

The third value, `transducer`, forces a path that is taken anyway whenever the
loaded transcriber implements `StreamTranscriber`. The key selects nothing.

**`sherpa_model` falls back to `model`** when empty, and the catalog already
records which runtime each model belongs to (`KnownModel.Engine`). Two keys, one
concept, violating P3.

### 2.3 A flat namespace where the prefixes are doing a table's job

Nine `sherpa_*` keys, four `duck_*` keys, and three path keys sit at the same
level as the two or three settings a first-time user touches. Five of the sherpa
keys — `sherpa_model_type`, `sherpa_tokens`, `sherpa_encoder`, `sherpa_decoder`,
`sherpa_joiner` — exist only to describe a model that is not in the catalog.

---

## 3. Two axes welded into one enum

This is the core diagnosis. `engine = "cli" | "server" | "sherpa"` is a
three-value enum over a two-dimensional space, and the three values are not the
same kind of thing. Two of them name *where inference happens*; the third names
*what library does the inference*.

Two terms, both **coined here**, used throughout the rest of this document:

- **Runtime** — the inference library that executes a model. mavor has exactly
  two: whisper.cpp, and ONNX Runtime reached through sherpa-onnx. A runtime is
  never configured. It is a property of the model, recorded in the catalog.
- **Placement** — where that runtime executes relative to the daemon process.
  Four values, defined in the table below. Placement is *not* the same as
  "engine", and it is *not* per-runtime: it is an independent question that
  happens to have a different default answer for each runtime.

> [!NOTE]
> **Does sherpa run in server or CLI mode?** Neither. It runs **in-process**:
> the model is loaded into the daemon through cgo and stays resident for the
> life of the daemon. That is a third placement, and the fact that it currently
> sits in a list next to `cli` and `server` — as though it were the same kind of
> choice — is exactly the conflation this section names.

### 3.1 The placement values

| Placement | What it means | Model stays warm |
|---|---|---|
| `in-process` | The runtime is linked into the daemon and the model is resident. | Yes, for the life of the daemon |
| `local-server` | mavor starts and supervises a child process holding the model, and posts audio to it over loopback HTTP. | Yes, for the life of the child |
| `subprocess` | A fresh process per utterance; the model is loaded and freed each time. | No |
| `remote` | An HTTP server someone else runs, named by URL. | Not mavor's problem |

### 3.2 What actually exists, per runtime

| Runtime | `in-process` | `local-server` | `subprocess` | `remote` |
|---|---|---|---|---|
| whisper.cpp | not built (no Go binding in this project) | ✅ `whisper-server`, **the default** | ✅ `whisper-cli` | ✅ any HTTP URL |
| ONNX Runtime (sherpa) | ✅ cgo, **the default** | not built (upstream ships websocket servers; mavor does not use them) | not built | not built |

Two cells are the defaults, and they are fully derived from the model name. The
user never picks placement to get the right behavior — only to get a *different*
one: a remote server, or `subprocess` when `whisper-server` is missing or when
they want a command they can rerun by hand.

The measured case for `local-server` as the whisper default, from
[`../reports/model-benchmarks.md`](../reports/model-benchmarks.md):

| Model | Warm server | Cold subprocess | Saved per utterance |
|---|---:|---:|---:|
| `whisper-tiny.en` | 550 ms | 809 ms | 259 ms |
| `whisper-base.en` | 1.30 s | 1.51 s | 207 ms |
| `whisper-small.en` | 3.31 s | 4.77 s | 1.45 s |

Startup cost is 325–551 ms, paid once when the daemon starts.

### 3.3 The config surface this produces

```toml
[advanced]
# placement = "auto"    # "auto", or "subprocess" for whisper models
# server = "http://…"   # send audio to a whisper server you run
```

Setting `server` implies `remote` and makes `placement` irrelevant; `doctor`
reports the conflict if both are set to incompatible values. `placement` accepts
`auto` and `subprocess` only — the other two values are derived and are not
things a user can meaningfully ask for. Naming a placement a runtime cannot
provide (`subprocess` on a sherpa model) is a config error reported by `doctor`
and refused at daemon start, rather than silently ignored.

---

## 4. The build is cgo, always

**Decided 2026-09-05 ([OQ-1](#decision-ledger)): mavor is a cgo program.** The pure-Go build is not
demoted to a second artifact, it is deleted. The `sherpa` build tag goes with
it, `just build` becomes what `just build-sherpa` was, and `build-sherpa` and
`bench-sherpa` collapse into `build` and `bench`.

This was a live question because `AGENTS.md` states the default build is pure Go
and `CGO_ENABLED=0` works. The sherpa runtime is the one variant needing cgo,
which is why thirteen of the twenty-four catalog models are unreachable in that
default build — and why [§6](#6-the-preview) could not make a sherpa model the default preview
source without settling this first.

### 4.1 What the ruling costs, measured

Both variants built from `62db92c` on 2026-09-05. The left column is what is
being given up:

| | pure Go (deleted) | cgo (the build) |
|---|---|---|
| Binary | 11.8 MB, `statically linked` | 12.0 MB, dynamically linked |
| Ships as | one file | one file **plus 31 MB of shared objects** |
| Shared objects | none | `libonnxruntime.so` (26.4 MB), `libsherpa-onnx-c-api.so` (5.1 MB) |
| Default `RUNPATH` | n/a | an absolute path into **the builder's** `~/go/pkg/mod` |
| Cross-compile to `arm64` | works | needs a cross toolchain |
| libc | none | glibc; the module carries a `!musl` tag |

The `RUNPATH` result looks fatal and is the one thing here that could have
reversed the decision:

```console
$ readelf -d bin/mavor | grep RUNPATH
 (RUNPATH)  Library runpath: [/home/agent/go/pkg/mod/github.com/k2-fsa/sherpa-onnx-go-linux@v1.13.7/lib/x86_64-unknown-linux-gnu:…]
```

A binary built that way runs only on a machine carrying that exact module cache
path. The cross-compile failure is equally concrete:

```console
$ GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go build -tags sherpa ./cmd/mavor
# runtime/cgo
gcc_arm64.S:30: Error: no such instruction: `stp x29,x30,[sp,'
```

> [!IMPORTANT]
> The `RUNPATH` problem is solved, not tolerated. Verified 2026-09-05: building
> with `-ldflags "-r \$ORIGIN"` and copying the two shared objects next to the
> binary produces a relocatable 42 MB directory that runs. Do not re-derive this
> objection — it was investigated and it does not hold, and the release recipe
> must set `$ORIGIN` or it will ship a binary that runs only on the build host.

### 4.2 What follows from it

- **The distribution unit is a directory**, 42 MB, not an 11.8 MB file. Normal
  for a program shipping an ML runtime; whisper.cpp ships shared objects too.
- **Cross-compilation needs a cross toolchain.** The realistic targets are
  `linux/amd64` and `linux/arm64`, and the sherpa module vendors prebuilt objects
  for both, so this is a build-host question rather than a portability wall.
- **musl is unsupported** until someone asks. The upstream module has a musl
  variant behind its own tag; wiring it up is not free and nobody wants it yet.
- **A C toolchain becomes a build requirement**, including in CI.

What it buys is the reason: thirteen catalog models, the streaming preview
companion of [§6](#6-the-preview), sub-second Parakeet, and a config file that never has to
explain a build tag to anyone. That last one is why this section is in a
document about `config.toml` at all.

---

## 5. Model naming

Every catalog name begins with its model family. Whisper's names did not; now
they do. The eleven whisper entries become `whisper-tiny`, `whisper-base.en`,
`whisper-large-v3-turbo`, and so on. The thirteen sherpa entries already satisfy
the rule (`parakeet-tdt-0.6b`, `moonshine-tiny`, `zipformer-streaming`) and do
not change.

**Aliases are deleted outright, not flipped.** `tiny`, `base.en`, `zipformer`,
`parakeet-tdt`, and `parakeet-tdt-1.1b` stop resolving. One name per model. A
name that does not resolve produces an error listing the closest catalog entries
rather than a fallback.

> [!WARNING]
> The prefix names the **model family**, not the runtime. sherpa-onnx also
> supports Whisper models in ONNX form, so a future ONNX Whisper entry could not
> be called `whisper-base.en` — that name is taken by the GGML file that runs on
> whisper.cpp. If such an entry is ever added it needs a distinct name.

Two consequences the implementation must handle:

- **On-disk filenames stay upstream's.** The catalog name `whisper-base.en`
  maps to the file `ggml-base.en.bin`, because that is what the download URL
  serves. Today the path is built by string concatenation on the config value
  ([`internal/speech/factory.go#L26`](../../internal/speech/factory.go#L26)); it
  becomes an explicit field on the catalog entry.
- **`parakeet` is badly named and should be renamed in the same pass.** It is
  the NeMo streaming FastConformer 80 ms model, and it sits next to an unrelated
  `parakeet-tdt-0.6b`. Rename it for what it is.

A name not found in the catalog is looked up as a directory under the sherpa
model directory before failing, which is how a hand-installed model is used. The
five `sherpa_*` file-path keys leave the config entirely; describing a custom
model is a manual topic that needs a walkthrough, not five keys sharing top
billing with `model`.

---

## 6. The preview

**Preview** is the text mavor shows in the overlay while you speak. It is never
typed. The final transcript always comes from `model`, produced once, when you
release the key — partial results are provisional, and typing them would insert
the same words twice.

Today the preview has two mechanisms behind a key that selects neither reliably
([§2.2](#22-keys-that-are-aliases-for-other-keys)). The proposal names both and picks a better default.

- **Companion model** *(coined here)* — a small streaming recognizer loaded
  alongside the main model, fed the same audio, emitting partial text
  continuously. It never contributes to the final transcript.
- **Phrase mode** *(coined here, replacing the undefined word "slice")* — no
  second model. When you pause, the audio since the last pause is transcribed
  with the main model and appended to the preview.

### 6.1 Why the companion model becomes the default

Phrase mode has two documented failure modes, both structural:

1. **Whisper hallucinates on short clips.** The model was trained on long-form
   subtitled audio and fills near-silent or very short inputs with plausible
   text, frequently by repeating the previous phrase. This is upstream Whisper
   behavior, not an implementation bug.
2. **Each phrase is decoded with no context from the last**, so a word that
   depends on what preceded it is re-guessed from nothing every time.

There is precedent for the fix. OpenWhispr shipped phrase-based preview,
documented exactly these problems, and replaced it in v1.7.6 with a dedicated
streaming model. Mistral ships Voxtral as two separate models — one realtime,
one batch — for the same reason. The two-pass ASR literature (a fast first pass
emitting partials, an accurate second pass finalizing) is the same idea with a
shared encoder.

Cost is real but bounded: a streaming zipformer decodes at roughly 0.06–0.08
real-time factor and keeps up on one to two cores, alongside the main model.

### 6.2 The resolution rule

`preview.source = "auto"` resolves in this order, at daemon start:

1. **The main model already decodes incrementally** (catalog `Streaming: true` —
   currently the streaming FastConformer and `zipformer-streaming`). Read its
   partial output directly. No second model is loaded.
2. **A companion model is installed.** Load it and run it alongside.
3. **Otherwise**, fall back to phrase mode, and have `doctor` say which model to
   pull for a better preview.

**The designated companion is the 20M-parameter streaming zipformer**, added to
the catalog for this purpose ([OQ-2](#decision-ledger)). The catalogued `zipformer-streaming` is a
310 MB artifact for a resident int8 encoder of roughly 40 MB, which is a poor
trade for something that only paints an overlay; the 20M model upstream
publishes is the right size for the job. It stays selectable by name for anyone
who wants the larger one.

**`mavor setup` always pulls it** ([OQ-3](#decision-ledger)), which makes step 3 a safety net rather
than the normal path — it catches a model deleted after setup, or a config
edited by hand. That follows from a broader rule worth stating on its own:

> [!IMPORTANT]
> **`mavor setup` makes the current config fully runnable, and is idempotent.**
> It pulls every model the config names — the main model and the preview
> companion — skips what is already present, and can be re-run at any time
> after an edit. "Fully runnable" is the contract: after `setup` exits zero,
> `mavor daemon` starts with that config and needs no further downloads.

Explicit values override the resolution: a model name forces that companion,
and `"phrases"` forces phrase mode even when a companion is available. **A model
named explicitly and not found is fatal**, never a silent downgrade — see
[§10.2](#102-failure-paths).

---

## 7. Vocabulary and decoding

`sherpa_decoding_method`, `sherpa_hotwords_file` and `sherpa_hotwords_score`
expose sherpa-onnx's decoding internals to a user who wants one thing: for the
model to stop mishearing their vocabulary. Replace all three with one
runtime-neutral table.

```toml
[vocabulary]
# words = ["mavor", "wlroots", "Schulkind"]
# file  = "~/.config/mavor/vocabulary.txt"
# boost = 1.5
```

What it maps to, per runtime:

| Model kind | Mechanism | Notes |
|---|---|---|
| whisper (any) | initial prompt (`--prompt`) | Capped at 224 tokens upstream; mavor passes none today, so this closes a real gap |
| transducer (parakeet, zipformer) | hotwords file, one phrase per line | **Forces `modified_beam_search`**, because sherpa-onnx ignores hotwords under greedy decoding without complaint |
| CTC, paraformer, moonshine, sensevoice | nothing | sherpa-onnx implements biasing inside transducer beam search only. `doctor` reports this rather than failing |

**The user never chooses a decoding method.** The evidence says there is nothing
to choose. On LibriSpeech the zipformer transducer scores 2.17% word error rate
with greedy search and 2.15% with modified beam search — a difference of 0.02
absolute — for roughly `max_active_paths` times the decoder work. Beam search is
also unavailable on every non-transducer model: CTC, paraformer, whisper,
moonshine and sensevoice all abort with `Only greedy_search is supported at
present`. And there are open upstream reports of beam search on NeMo TDT
returning empty or hallucinated text where greedy is clean.

So greedy is the default, and the single thing beam search buys — hotwords —
turns it on as a consequence of configuring vocabulary, on the models that
support it. This is what the code already does at
[`internal/speech/sherpa.go#L682`](../../internal/speech/sherpa.go#L682); the
change is to stop pretending it was the user's decision.

`boost` is a per-token score added during beam search whenever a hypothesis
extends a listed phrase. Upstream's default is 1.5; the vocabulary design doc
puts the useful range at 1.5–3.0. Too high inserts listed words where they were
not said, which is why the comment in the file states the range rather than just
the default (P4).

This table lands now rather than waiting for
[`active-window-context-and-vocabulary-prompting.md`](active-window-context-and-vocabulary-prompting.md)
to leave review ([OQ-4](#decision-ledger)). It is the smaller half of that design rather than a
competing one: the window-context work derives the word list at runtime and
still needs somewhere to put a static list, so it adopts this key shape rather
than replacing it. It also closes a gap the roadmap already flags — whisper
receives no prompt at all today.

**Language model fusion is deliberately not exposed.** sherpa-onnx supports
RNN-LM shallow fusion, but only for zipformer and conformer transducers — not
Parakeet, not CTC, not whisper — it requires `modified_beam_search`, and no
pretrained ONNX language model is published for any catalog model. Using it
would mean training one in icefall. Not a config key.

---

## 8. The proposed file

```toml
# ~/.config/mavor/config.toml
#
# Every value has a working default; delete a line to get it back.
# Run `mavor doctor` after editing — it checks each setting against this
# machine and reports what the daemon will actually do with it.

# The model that produces your text. `mavor models list` shows every choice
# with its size, speed and accuracy, and marks what is installed.
model = "whisper-base.en"

[preview]
# Text in the overlay while you speak. The final text always comes from
# `model`, typed once when you release the key. The preview types nothing.
enabled = true

# Where the preview text comes from.
#   "auto"       — read `model` directly if it decodes as you speak;
#                  otherwise run a small streaming model alongside it;
#                  otherwise fall back to "phrases"
#   "phrases"    — no second model: re-transcribe with `model` at each
#                  pause. Cheaper, slower, and prone to filling silence
#                  with words you did not say
#   <model name> — run that model alongside as the preview source
source = "auto"

# "phrases" only: how long a pause ends a phrase, and how much speech a
# phrase needs before a pause can end it.
# pause_ms = 450
# min_phrase_ms = 600

[ducking]
# Lower other audio while recording.
enabled = false
volume = "0%"                     # "0%" mutes; "25%" merely lowers
# apps = ["spotify", "firefox"]   # only these; the default is every stream
# sink = ""                       # a specific output, not the default one

[vocabulary]
# Words the model gets wrong: names, jargon, commands. whisper models take
# these as a prompt; transducer models (parakeet, zipformer) boost them
# while decoding. Other models cannot use them and `mavor doctor` says so.
# words = ["mavor", "wlroots", "Schulkind"]
# file  = "~/.config/mavor/vocabulary.txt"   # one phrase per line
# boost = 1.5   # transducers only. 1.5 to 3.0 is the useful range; higher
#               # makes these words appear where they were not said.

[overlay]
top_margin = 8   # px below the top of the usable area, under your bar

# Chosen for you. Override only if `mavor doctor` gives you a reason to.
[advanced]
# placement = "auto"     # "auto", or "subprocess" for whisper models
# server = "http://…"    # send audio to a whisper server you run instead
# threads = 6            # default: this machine's physical core count
# gpu = "auto"           # "auto" or "off". whisper only — sherpa models
#                        # run on the CPU whatever this says.

[paths]
# models = "~/.cache/mavor/models"
# log    = "~/.local/state/mavor/daemon.log"
# socket = "$XDG_RUNTIME_DIR/mavor.sock"
```

Twenty keys, down from twenty-nine, and a first-time user reads the first line.

### 8.1 What this deletes

| Old key | Fate |
|---|---|
| `mode` | → `preview.enabled` (P2: a boolean that says what it does) |
| `preset` | deleted; `models list` shows the tradeoff with real numbers |
| `streaming_strategy` | deleted; it selected nothing ([§2.2](#22-keys-that-are-aliases-for-other-keys)) |
| `sherpa_model` | folded into `model` (P3) |
| `device` | deleted; never read ([§2.1](#21-three-keys-that-are-wrong-not-merely-confusing)) |
| `gpu_layers` | → `advanced.gpu`; the old key broke transcription ([§2.1](#21-three-keys-that-are-wrong-not-merely-confusing)) |
| `sherpa_provider` | deleted; see [§9](#9-chosen-for-you-threads-gpu-and-the-sherpa-provider) |
| `sherpa_decoding_method` | deleted; follows from vocabulary ([§7](#7-vocabulary-and-decoding)) |
| `sherpa_hotwords_file`, `sherpa_hotwords_score` | → `vocabulary.file`, `vocabulary.boost` |
| `sherpa_model_type`, `sherpa_tokens`, `sherpa_encoder`, `sherpa_decoder`, `sherpa_joiner` | leave the config; custom models become a manual topic ([§5](#5-model-naming)) |
| `server_socket` | → `advanced.server`, a URL only. The path form never created a path |
| `silence_threshold_ms` | → `preview.pause_ms` |
| `min_phrase_ms` | → `preview.min_phrase_ms` |
| `threads` | → `advanced.threads`, defaulting to physical cores ([§9](#9-chosen-for-you-threads-gpu-and-the-sherpa-provider)) |

---

## 9. Chosen for you: threads, GPU, and the sherpa provider

**Threads default to the physical core count**, read from
`/sys/devices/system/cpu/cpu*/topology/core_id`, falling back to
`runtime.NumCPU()` when that is unreadable, and clamped to at least 1. The
thread-scaling table in
[`../reports/model-benchmarks.md`](../reports/model-benchmarks.md) shows the
speedup curve flattening at physical cores on a 6-core/12-thread machine: 6
threads was best or within noise for every model, and 8 bought nothing. A value
above the logical core count is honored and warned about in `doctor`.

**`gpu` is `auto` or `off`, and applies to whisper only.** whisper.cpp uses a GPU
when its build has one; `off` maps to `-ng` for a broken driver. `doctor` reports
which backend actually loaded, which is the only reliable answer.

**`sherpa_provider` is deleted rather than fixed.** Verified 2026-09-05: the
`sherpa-onnx-go-linux` module vendors a CPU-only `libonnxruntime.so` with no
provider shared objects, and sherpa-onnx responds to a provider it cannot honor
by logging `Fallback to cpu!` and continuing. A `provider` key on this build can
only ever mislead. See [`../roadmap.md`](../roadmap.md) for the workstream on
changing that.

---

## 10. Behavior the implementation must get right

Altitude ends here; this section is the specification, because each of these has
a plausible wrong answer that would compile.

### 10.1 Degenerate inputs

| Input | Behavior |
|---|---|
| `vocabulary.words` empty and `file` unset | No prompt, no hotwords, greedy decoding. Not an error |
| Both `words` and `file` set | Union, `words` first, duplicates dropped keeping first occurrence |
| Vocabulary longer than whisper's 224-token prompt | Truncate at a phrase boundary, warn once at load naming the count dropped |
| `boost` above 5.0 | Honored; `doctor` reports it as likely to insert words |
| `threads` ≤ 0 | Autodetect, as if unset |
| `top_margin` < 0 | Clamp to 0 |
| `pause_ms` or `min_phrase_ms` ≤ 0 | Use the default (450 ms / 600 ms) |
| `model` not in the catalog | Look up as a directory under the sherpa model dir; if absent, **fatal**, naming the closest catalog entries |
| `preview.source` names a model that is not installed | **Fatal.** A named model is a request, not a hint ([§10.2](#102-failure-paths)) |
| `preview.source = "auto"` and the companion is not installed | Warn, fall back to phrase mode, name the model to pull. The only case that downgrades |
| `preview.source` equal to `model` | Treated as case 1 of [§6.2](#62-the-resolution-rule). Never load the same model twice |
| Unknown key in the file | Warn at load, listing the key; `doctor` reports it as an error. Never fatal at load |
| Config file absent | Every default applies. Not an error, as today |

### 10.2 Failure paths

- **A model named in the config cannot be found** → **fatal at daemon start**,
  naming the model and the directory searched. This holds for the main model and
  for an explicitly named `preview.source` alike. A user who writes a model name
  gets that model or an error, never a quiet substitution — and since `mavor
  setup` makes the current config fully runnable ([§6.2](#62-the-resolution-rule)), reaching this state
  means the config changed after setup, which the message should say.
- **The companion model fails to load for any other reason** (corrupt files, an
  unreadable directory) → log a warning, fall back to phrase mode, daemon starts.
  The preview is a convenience; a broken one must not cost the user dictation.
- **`whisper-server` child dies** → the supervisor restarts it
  ([`internal/speech/supervisor.go#L351`](../../internal/speech/supervisor.go#L351)).
  If it dies again within 30 s of a restart, stop restarting, log, and fail the
  next transcription with a message naming the child's stderr.
- **`whisper-server` binary missing** with `placement = "auto"` → fall back to
  `subprocess`, warn once at start, and report it in `doctor`.
- **`advanced.server` URL unreachable** → the daemon still starts. `doctor`
  reports it; the first transcription fails with the URL in the message.
- **A phrase-mode transcription fails** → drop that phrase from the preview, log
  at debug, keep recording. It must never abort the recording or the final
  transcript.
- **Vocabulary file unreadable** → warn, proceed with `words` alone.

### 10.3 Ordering, one-writer rules, and forbidden behavior

- **The overlay text has one writer**: the preview driver, and only between
  start and stop. The final transcript never reaches the overlay as text.
- **The output emitter has one writer**: the final transcription. **The preview
  must never emit text**, and the companion model must never influence the final
  transcript.
- **On stop, all preview work is cancelled and its results discarded**, including
  phrase transcriptions still in flight. A phrase result arriving after stop is
  dropped, not appended.
- **Phrase mode inherits the placement of the main model.** Under `subprocess`
  that means one process spawn per pause, which is precisely why it is the
  fallback and not the default.
- **Config is read once**, at daemon start. There is no hot reload; a change
  needs `mavor stop` and a restart. `doctor` and `config show` read from disk on
  each invocation.
- **The companion model loads at daemon start**, not lazily at first recording,
  so the first dictation is not the slow one. This trades resident memory for
  first-use latency, deliberately.

### 10.4 Triggers, with units

| Event | Trigger |
|---|---|
| Phrase boundary in phrase mode | RMS below the speech threshold for `pause_ms` (default 450 ms) with at least `min_phrase_ms` (default 600 ms) of speech accumulated |
| Companion model load | Once, at daemon start |
| Config load | Once, at daemon start; once per CLI invocation that needs it |
| Ducking | On entering Recording; restored on leaving it, including on error |

### 10.5 Pre-existing state on the day this ships

Existing `config.toml` files stop parsing meaningfully: their keys are unknown
and every default applies. Because there is no public release
([`../roadmap.md`](../roadmap.md)), the affected population is the author's own
machine. `mavor doctor` must detect a file whose keys are all unknown and say
plainly that the schema changed and `mavor config init --force` will scaffold
the new one. Downloaded model files are untouched — the rename in [§5](#5-model-naming) is a
catalog-name change, and on-disk names stay upstream's.

### 10.6 What done looks like

- `mavor config init` on a clean machine, followed by `mavor setup` and then
  `mavor daemon`, dictates correctly with no edits and with a live preview.
- `mavor setup` run twice in a row downloads nothing the second time and exits
  zero both times. Run again after `preview.source` is edited to name a model
  that is not installed, it pulls that model; `mavor daemon` then starts.
- A config naming a model that is not installed fails to start with a message
  naming the model and the directory searched — never a downgrade to a
  different model or to phrase mode.
- A file containing only `model = "whisper-small.en"` runs `whisper-small.en`
  through a warm supervised server, on the physical core count, with a preview.
- `mavor config show` output, saved and reloaded, produces an identical resolved
  config.
- The scaffolded template parses to exactly `Default()` — enforced by a test, so
  the drift in [§2.1](#21-three-keys-that-are-wrong-not-merely-confusing) cannot recur.
- No configuration causes `-ngl` to be passed to whisper.cpp.
- `mavor doctor` reports, for the active config: which runtime and placement were
  chosen and why, the thread count and where it came from, whether a GPU backend
  loaded, whether vocabulary can be applied to this model, and where the preview
  text is coming from.

---

## 11. Alternatives considered

**Keep `engine` and add placement beside it.** Rejected. The two would then
disagree — `engine = "sherpa"` with `placement = "local-server"` names a cell
that does not exist — and the user would have to keep them consistent by hand.

**Keep compatibility aliases for one release.** Rejected: nothing has shipped
publicly, so the aliases would serve one machine while permanently complicating
the loader.

**Keep `preset` as a friendlier front for `model`.** Rejected. `models list`
already presents speed and accuracy with measured numbers, and `preset` cannot
name the twenty-one models outside its three-value enum.

**Make phrase mode the preview default and treat the companion as opt-in.**
Rejected as the default, kept as the fallback. [§6.1](#61-why-the-companion-model-becomes-the-default) gives the reasons; the
resolution rule in [§6.2](#62-the-resolution-rule) means nobody gets a surprise download either way.

**Expose `decoding_method` for users who want beam search without hotwords.**
Rejected: 0.02% word error rate for several times the decoder work, on the only
model family that supports it, with open reports of it producing empty output.

**Fix `gpu_layers` to emit `-ng` correctly rather than replacing it.** Rejected.
The key's name promises layer-granular offload that whisper.cpp does not
implement, so keeping the name would preserve the lie.

---

## 12. Risks

| Risk | Mitigation |
|---|---|
| cgo-only means no cross-compilation without a cross toolchain | The only realistic targets are `linux/amd64` and `linux/arm64`; the sherpa module vendors prebuilt objects for both, so the release builds on each host or in a container |
| A whisper-only user now ships 42 MB and an ONNX Runtime they never load | Accepted deliberately. Two build variants cost more in CI and support than the 30 MB saves |
| A release built without `$ORIGIN` runs only on the build host | [§4.1](#41-what-the-ruling-costs-measured). The release recipe sets it, and a smoke test runs the artifact from a directory the build did not create |
| The companion model starves the main model on a small CPU | It costs roughly one core at 0.06–0.08 real-time factor; `doctor` warns below 4 physical cores, and `preview.source = "phrases"` is one line |
| Deleting aliases breaks the author's own config and any scripts | [§10.5](#105-pre-existing-state-on-the-day-this-ships): `doctor` detects an all-unknown-keys file and names the fix |
| The catalog rename invalidates the benchmark report's model column | `just bench` regenerates it; the report is generated, never edited |
| `[advanced]` becomes a dumping ground | P1 gates additions: no key unless mavor cannot pick the value |

---

## 13. Non-goals

- **Not** a hot-reloading config. A change needs a daemon restart.
- **Not** a change to any transcription algorithm. The preview work in [§6](#6-the-preview) adds a
  second recognizer; it does not alter how the final transcript is produced.
- **Not** the window-context half of
  [`active-window-context-and-vocabulary-prompting.md`](active-window-context-and-vocabulary-prompting.md).
  This doc gives vocabulary a static config surface; deriving vocabulary from the
  focused window stays in that doc.
- **Not** a profile or per-application config system. One file, one set of values.
- **Not** GPU acceleration for sherpa. [§9](#9-chosen-for-you-threads-gpu-and-the-sherpa-provider) explains why it cannot work on this
  build; [`../roadmap.md`](../roadmap.md) carries the workstream.
- **Not** a config surface for custom sherpa model files ([§5](#5-model-naming)).

---

## 14. What I would build, in order

**First, fix `gpu_layers` on its own.** It breaks transcription today for anyone
who sets it, and `doctor` recommends setting it. Replace it with the `auto`/`off`
flag, correct the `doctor` message, and land it independently of everything else
here.

**Second, prefix the whisper catalog names and delete every alias**, adding the
explicit on-disk filename field the rename requires. This is self-contained and
touches the catalog, `models pull`, and the docs.

**Third, collapse the build to cgo** ([§4](#4-the-build-is-cgo-always)). Delete the `sherpa` build tag and the
`CGO_ENABLED=0` recipe, fold `build-sherpa` into `build` and `bench-sherpa` into
`bench`, set `$ORIGIN` in the release recipe, and correct the Build Tags section
of `AGENTS.md`, which currently states the opposite. This has to land before the
preview, which assumes a sherpa model is always available.

**Fourth, the schema itself.** The struct with its tables, `Default()`, thread
autodetection, runtime and placement derivation, and a test asserting that the
scaffolded template parses to exactly `Default()`.

**Fifth, the preview.** The companion model, the resolution rule, and phrase
mode as the named fallback. This is also where `mavor setup` becomes idempotent
and config-driven ([§6.2](#62-the-resolution-rule)), and where a named-but-missing model becomes fatal
([§10.2](#102-failure-paths)). The largest piece.

**Sixth, vocabulary.** The `[vocabulary]` table mapped to a whisper prompt and to
sherpa hotwords, with the decoding method following from it.

**Seventh, the docs**: the user guide's configuration reference, the quickstart,
and the `doctor` output described in [§10.6](#106-what-done-looks-like).

Steps one and two are worth landing even if the rest of this proposal is
rejected.

---

## Follow-up: `[output]`, added 2026-09-06

The schema [§8](#8-the-proposed-file) settled had no say over the clipboard, because I never noticed
mavor was writing to it. It always has: `Emit` ran `wtype` and `wl-copy`
unconditionally, and the code's own comment gives the reason — a copy is still
useful when the keystrokes miss the intended window.

That is a real benefit and it was not the user's to decline, which is the test
P1 sets. So a seventh table:

```toml
[output]
clipboard = false
```

**Off by default**, which is the part worth arguing. The recovery it buys is
worth less than the clipboard it destroys: someone dictating into an editor
while holding a URL to paste loses the URL, on every utterance, having asked
for none of it. `mavor history --copy` already recovers a transcript on demand,
so the capability is not lost — only the surprise is.

Typing itself is not configurable. It is the product, not a policy.

This makes 21 keys rather than the 20 [§8](#8-the-proposed-file) counted.

---

## Decision Ledger

| ID | Ruling / Decision | Date | Settled in |
| :--- | :--- | :--- | :--- |
| OQ-1 | cgo only. The pure-Go build and the `sherpa` tag are deleted, not demoted — one build, one artifact | 2026-09-05 | [§4](#4-the-build-is-cgo-always) The build is cgo, always |
| OQ-2 | The 20M-parameter streaming zipformer is the designated companion; `zipformer-streaming` stays selectable by name | 2026-09-05 | [§6.2](#62-the-resolution-rule) The resolution rule |
| OQ-3 | `mavor setup` always pulls the companion, is idempotent, and makes the current config fully runnable. A named model that is missing is fatal, never a downgrade | 2026-09-05 | [§6.2](#62-the-resolution-rule), [§10.2](#102-failure-paths) Failure paths |
| OQ-4 | The `[vocabulary]` table lands now; the window-context design adopts its key shape rather than replacing it | 2026-09-05 | [§7](#7-vocabulary-and-decoding) Vocabulary and decoding |
| OQ-5 | `[preview]` stays a table — `pause_ms` and `min_phrase_ms` belong with the thing they tune | 2026-09-05 | [§8](#8-the-proposed-file) The proposed file |
