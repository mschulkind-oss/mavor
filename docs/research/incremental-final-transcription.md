---
status: accepted
stage: CURRENT
next: "Use the paced prototype report for current implementation evidence"
---

# Incremental final recognition: what the current tree actually does

Source inspection, 2026-10-01, against `779917b`. The base sequence below is
historical, not the opt-in implementation; subsequent recognition measurements
are in the [prototype report](../reports/incremental-final-prototype.md). No recognition or latency
measurements were performed in this contract phase. See the
[design](../design/incremental-final-transcription.md) for settled behavior.

## The inspected base sequence

- [The daemon](../../internal/daemon/daemon.go#L276-L310) starts capture before
  preview. Preview-disabled recordings do not start streaming at all.
- [Preview feeding](../../internal/daemon/daemon.go#L423-L500) consumes newly
  captured PCM through `ReadChunk`, then synchronously decodes on a 30 ms tick.
  A slow decode makes the next read larger, not faster.
- [Release](../../internal/daemon/daemon.go#L636-L687) cancels preview, clears its
  generation, asynchronously calls `StopStream(context.Background())`, and
  discards the text. There is no explicit join of the feeding goroutine before
  that finalization. Main-model batch decoding can contend with its own preview
  drain; a new preview start waits for the previous drain.
- [Final transcription](../../internal/daemon/daemon.go#L734-L809) stops capture,
  retains the resulting WAV until pipeline completion, optionally checks energy,
  calls `Transcribe(WAV)` regardless of native streaming, cleans annotations and
  whitespace, writes history, then emits once.
- [The consuming reader](../../internal/audio/audio.go#L166-L263) advances one
  offset. `Stop` clears the recorder's path and offset before waiting for parec
  to flush. Reading again after stop cannot recover the last unread bytes.
  Reconcile the final WAV using the session's own sample count instead.

## Recognition and wrapper facts

- [Sherpa](../../internal/speech/sherpa.go) satisfies `StreamTranscriber` even
  for offline models; method availability is not eligibility. Offline feed
  returns an error, while offline start and stop can appear successful.
- [The online C wrapper](../../internal/speech/sherpa_cgo.go) owns one active
  stream; feeding drains all ready decode steps. Stop adds 0.66 seconds of
  synthetic silence, marks input finished, drains, gets the result and destroys
  the stream. Preserve this padding for the last word. Native decode calls
  themselves cannot be interrupted; context is checked between calls.
- [Factory selection](../../internal/speech/factory.go) wraps Whisper in
  after-stop chunking but returns Sherpa directly. [Chunking](../../internal/speech/chunking.go)
  preserves streaming, startup and close interfaces; its token overlap heuristic
  is not proof that natural repetitions are safe to deduplicate.
- [Drain tests](../../internal/daemon/preview_latency_test.go) require a discarded
  companion decode not to delay the IPC reply or normal main transcription.
  [Energy tests](../../internal/daemon/silence_preview_test.go) protect quiet
  speech and the default-off energy rejection. Retain these default-mode tests.

## Fast-moving — verify before building

Both requested candidates are present and readable under the read-only mount:

```text
/ctx/mavor-models/sherpa/nemotron-streaming-en-560ms
  encoder.int8.onnx 652916849 bytes; decoder 7257753; joiner 1735862
  tokens.txt; upstream streaming artifact marker directory
/ctx/mavor-models/sherpa/parakeet-tdt-0.6b-v2
  encoder.int8.onnx 652184296 bytes; decoder 7257753; joiner 1739080
  tokens.txt; upstream offline v2 artifact marker directory
```

Directory listing establishes availability, not successful loading or quality.
Use catalog names with `paths.models = "/ctx/mavor-models"`; custom paths must not
be reverse-guessed into catalog identities. The [library locator](../../scripts/sherpa-libs.sh)
resolved both required libraries from the installed `v1.13.7` module:

```bash
libdir="$(dirname "$(scripts/sherpa-libs.sh | head -1)")"
export LD_LIBRARY_PATH="$libdir${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
export CGO_ENABLED=1
```

The vendored recognizers use CPU. No model download is needed. If loading fails,
record the exact path/error and repair within scope; never substitute a model
and call the requested candidate measured.

## Measurement gaps and verdicts

| Option | Disposition | Reason |
| :--- | :--- | :--- |
| Main native stream finalized after release | Adopt as opt-in | Existing online recognizer supports an authoritative end result; daemon currently throws it away. |
| Offline v2 completed segments | Adopt conservatively as opt-in | Sequential, silence-bounded segments avoid overlap text guessing; complete replay covers unsafe cuts or overload. |
| Promote preview text | Reject | Companion and phrase output are not authoritative main recognition. |
| Unbounded queue or forced overlapping cuts | Reject | Hides overload or risks lost/repeated boundary words. |
| Change GPU readiness/retry | Exclude | User reports ten-second readiness timeout and sticky CPU recovery, not proven memory exhaustion. |

[The existing harness](../../cmd/mavor-bench/sherpa.go) feeds streaming audio as
fast as decoding permits, not at wall-clock arrival. Its first-token and total
figures cannot prove release-to-final latency. The historical
[68-second report](../reports/model-benchmarks-llm-prompt-68s.md) is generated,
marked BUSY and includes loading; preserve it unchanged. The
[LLM selection draft](../design/llm-oriented-model-selection.md) asserts loop
immunity and a settled preview default without adequate evidence; do not inherit
those claims. The real fixture reference deliberately contains “records records”:
text matching must not erase that repetition.

Existing broad [model research](open-weight-models-and-runtimes.md) and
[energy-filter research](silence-filter-opt-in.md) inform scope, but current source
and paced observations outrank historical architectural speed claims.
