---
title: "Paced incremental final prototypes"
status: accepted
date: 2026-10-02
summary: "Production-session observations under shared CPU load; lower release waits, with offline segmentation quality drift."
---

# Paced incremental final prototypes

**MEASURED:** Eight observations on the uncommitted prototype based on `779917b`.
This is a bounded feasibility experiment, not a default-model recommendation,
quiet-host benchmark, distribution of latencies, or end-to-end typing timing.
[Usage and capabilities](../choosing-a-model.md#opt-in-final-recognition-prototypes)
and [verification](../qa/incremental-final-transcription-qa.md) are separate.
The generated historical benchmark reports were not edited.

## Method and provenance

The permanent opt-in test in [the benchmark harness](../../cmd/mavor-bench/paced_test.go)
uses the production final-session mechanism and model factory. The producer
supplies exact 16 kHz mono signed 16-bit PCM at 30 ms absolute recording deadlines,
including the short final frame, into the same asynchronous queue used by the
daemon. It does not run inference in the producer. Release is the actual timestamp
after the final admission; completion is the timestamp after authoritative
`Finish` returns. The retained original WAV is passed to that production finish
path. No preview recognizer, microphone, clipboard, output dispatcher, OSC device,
or host service participates in these measurements.

Both models were loaded from read-only mounted files, with four ONNX threads,
Go 1.26.3, sherpa-onnx binding 1.13.7, CPU-only inference, on an Intel i7-8700K
(12 logical CPUs, `GOMAXPROCS=12`). The cgroup exposed no CPU or memory quota.
Model creation/loading was timed independently: Nemotron 5.877 s, Parakeet
9.659 s. One loaded recognizer per fresh model test process was reused across
fixtures/modes. Each warm after-stop baseline preceded its paired live mode;
there was no separate untimed inference warmup or randomized order.

Runs occurred 2026-10-02 04:23–04:30 UTC. The host was shared with the separate
GNOME workflow and other work. Observed one-minute load ranged from 6.11 to
12.65; three load averages and runnable/task counts were recorded before/after
each row. This is **contended evidence**, not a quiet baseline. Maximum producer
lateness was 1.04–3.37 ms. Load average is not CPU utilization, and endpoint
snapshots do not reveal every transient stall. One observation per cell cannot
establish a worst-case wait or promise subsecond completion on another machine.

The fixtures contain exactly 320,000 samples (20 s) and 1,085,835 samples
(67.8646875 s). Raw durable evidence retains fixture/reference SHA-256 hashes,
actual transcripts, timestamps, session-start time, load context, CPU information,
thread settings, process peak resident memory, mode/replay reason and sample counts:

```text
/workspace/.yolo/durable/incremental-final/
  nemotron-paced.jsonl, nemotron-paced.log
  parakeet-paced.jsonl, parakeet-paced.log
  model-files.sha256, provenance.log
  distribution-origin.log, cli-*-{plain,verbose,json}.log
```

Process peak resident memory reached about 992 MiB for Nemotron and 2,060 MiB
for Parakeet. These are process high-water marks including prior rows/allocator
retention, not per-recording deltas or memory limits. Model files and fixture
references are explicitly named; no models were downloaded.

## Release-to-authoritative-final observations

Wait includes remaining decode, retained-WAV reconciliation and finalization,
but excludes model load, session startup, cleaning, history and output dispatch.
WER is [the existing normalized word error rate](./model-benchmarks.md#accuracy).
Literal token error additionally scores case/punctuation without normalization;
it is edit distance on whitespace-separated tokens divided by reference length,
not a segmentation repair. Actual transcripts remain available for inspection.

| Model / fixture | Mode | Release wait | WER | Literal token error |
| :--- | :--- | ---: | ---: | ---: |
| Nemotron 560ms / 20 s | after-stop | 7.192 s | 1.82% | 7.27% |
| Nemotron 560ms / 20 s | streaming | 0.145 s | 1.82% | 7.27% |
| Nemotron 560ms / 67.865 s | after-stop | 16.522 s | 18.05% | 32.06% |
| Nemotron 560ms / 67.865 s | streaming | 0.206 s | 18.05% | 32.06% |
| Parakeet v2 / 20 s | after-stop | 1.449 s | 1.82% | 1.82% |
| Parakeet v2 / 20 s | segments | 0.195 s | 3.64% | 7.27% |
| Parakeet v2 / 67.865 s | after-stop | 7.149 s | 11.28% | 22.14% |
| Parakeet v2 / 67.865 s | segments | 0.187 s | 18.80% | 32.06% |

All four live rows completed in the requested mode, without replay or overload,
and processed every captured sample. Native finalization, not real audio loss,
accounted for most of Nemotron's release wait. The export name “560ms” is not
that measured wait, and does not impose a bound on it.

| Live row | Pending at release | Peak unfinished audio | Drain | Finalize | Completed segments |
| :--- | ---: | ---: | ---: | ---: | ---: |
| Nemotron / 20 s | 0.020 s | 0.150 s | 2.58 ms | 142.02 ms | — |
| Nemotron / 67.865 s | 0.0047 s | 0.450 s | 5.37 ms | 200.54 ms | — |
| Parakeet / 20 s | 1.970 s | 5.070 s | 194.65 ms | 0.002 ms | 6 |
| Parakeet / 67.865 s | 1.895 s | 7.170 s | 187.01 ms | 0.004 ms | 16 |

Unfinished audio includes the in-flight/queued work and, for segments, the
not-yet-split portion; it is not simply a model-call duration. There was no unread
WAV suffix in this producer experiment. Recorder-stop suffix reconciliation and
failure replay have permanent lifecycle tests, not additional speed proof here.

## Quality findings, without boundary repair

Nemotron's live transcript was byte-for-byte identical to its same-model baseline
on each fixture. Both preserved all three “Lux” occurrences and both “hops”
occurrences on the short fixture, and both “records” occurrences and “repetition
loops” on the technical fixture. All eight rows ended with the expected final
phrase (“path” instead of reference “patch” on the short clip; “what broke” on
the long clip). There was no final-word omission on these samples.

That is not technical accuracy parity with Whisper. Both Nemotron modes rendered
`internal/speech` and `internal/output` as spoken “internal slash speech” and
“internal slash output”, and `ripgrep` as “rip grep”. Counts and normalized WER
must not be mistaken for exact command/path correctness.

Parakeet segmentation changed context and formatting. Short output inserted
“and” after “dust” and changed sentence capitalization. On the technical clip,
the full baseline included “Wayland compositor”; segment output instead included:

```text
... interacts with the way. I think that's a Actually, wait, before you do that ...
```

It also lost “sample” from “new benchmark sample”, changed “but make sure” to
“Make sure”, and broke sentences around “unhandled. XKB key codes”. Repeated
“records” survived, but with a period across the boundary. No overlap matching,
deduplication, punctuation repair or fuzzy text joining hid those differences.
The higher segment WER and the invented fragment are a real quality cost, not
merely different display formatting. Exact path/command spellings failed in
both offline modes too.

## Decision and remaining risks

Native Nemotron final recognition is feasible on these paced fixtures on this
CPU under observed load: it kept up and moved nearly all decoding before release,
without changing its same-model transcript. Offline-v2 segment processing also
moved work before release but **is not quality-equivalent** on the technical
sample. Keep both explicit opt-in prototypes and keep after-stop as the default;
there is no automatic model/quality substitution.

Quiet/short speech, accents, noise, uninterrupted speech beyond the conservative
split limit, long tails, heavier contention, repeated runs and user-specific
technical vocabulary remain real-model coverage gaps. Unit/fake tests cover
sample preservation, silence, cancellation, repeat retention and fallback
mechanics; they cannot prove recognizer quality or model-speed behavior there.
A CPU large-v3 recognizer taking roughly twice audio duration cannot be made
real-time by starting early. The user's GPU readiness timeout and sticky CPU
recovery are outside this experiment; no VRAM-out-of-memory conclusion or GPU
retry-policy change is implied.

## Post-review verification pair

A fresh process per model reran the same production harness after preview-only
repairs (independent companion configuration and diagnostics, truthful shared-main
preview planning). These repairs do not change the measured final-session decode
mechanism. Shared load averages ranged from 4.32 to 8.54; fixed baseline-first
ordering and all original measurement exclusions still apply.

| Model / fixture | After-stop wait | Live wait | WER baseline → live |
| :--- | ---: | ---: | :--- |
| Nemotron / 20 s | 5.154 s | 0.139 s | 1.82% → 1.82% |
| Nemotron / 67.865 s | 17.839 s | 0.251 s | 18.05% → 18.05% |
| Parakeet v2 / 20 s | 1.540 s | 0.209 s | 1.82% → 3.64% |
| Parakeet v2 / 67.865 s | 6.388 s | 0.192 s | 11.28% → 18.80% |

All live rows again processed 320,000 or 1,085,835 real samples without replay
or overload. Nemotron's paired transcripts again matched exactly. Parakeet again
lost “Wayland compositor” and invented “I think that's a”; the final phrase and
natural repeated “records” survived. This does not close broader acoustic quality
or dispatch gaps. Durable raw evidence:

```text
/workspace/.yolo/durable/incremental-final/
  landing-nemotron-streaming-en-560ms.jsonl
  landing-nemotron-streaming-en-560ms-paced.log
  landing-parakeet-tdt-0.6b-v2.jsonl
  landing-parakeet-tdt-0.6b-v2-paced.log
```
