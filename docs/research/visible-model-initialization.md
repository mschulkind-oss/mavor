---
status: accepted
stage: CURRENT
---

# Startup and companion source findings

Inspected 2026-10-02 at `dbb44ff`. This is source evidence, not a claim of runtime
verification. The approved behavior is in the [design](../design/visible-model-initialization.md).

## Blocking operations and ownership

| Source | Finding |
| :--- | :--- |
| [`main.go`](../../cmd/mavor/main.go#L169) | Resolve and FactoryFor precede signal context and overlay; factory failure returns invisibly |
| [`factory.go`](../../internal/speech/factory.go#L174) | FactoryFor reaches synchronous sherpa construction; whisper server/CLI are cheap constructors |
| [`companion.go`](../../internal/speech/companion.go#L349) | LoadPreview resolves and constructs companion before overlay; only named missing models are fatal, other load errors degrade |
| [`modelstart.go`](../../cmd/mavor/modelstart.go#L24) | beginStart starts one goroutine; consuming wait is mandatory before Close and is not repeatable |
| [`daemon.go`](../../internal/daemon/daemon.go#L214) | Run registers transition listener then opens sole IPC; cmd waits for engine before Run |
| [`supervisor.go`](../../internal/speech/supervisor.go#L323) | Readiness is TCP dial, ten-second GPU default; stderr bounded to 64 KiB, Stop reaps child |
| [`ipc.go`](../../internal/ipc/ipc.go#L52) | Serve owns socket; accepted requests have five-second deadlines; cancellation closes listener and waits handlers |

Both cmd defers and daemon Run currently close main/companion; new startup needs
one ownership transfer rather than additional close wrappers. Early IPC setup must
confirm binding before launching loads; merely starting Serve in a goroutine races.

## Backup limitations

- [`daemon.go`](../../internal/daemon/daemon.go#L761): stop cancels feeding, increments
  HUD generation, finalizes with Background, discards final text, retains only
  recognition evidence. Final WAV can contain audio never read before stop.
- [`final_cycle.go`](../../internal/daemon/final_cycle.go#L59): incremental companion
  queue has 32 slots and drops when full; cancellation exits without draining.
  Both paths currently treat companion as disposable, not output authority.
- [`audio.go`](../../internal/audio/audio.go#L166): ReadChunk advances a shared file
  offset; Stop clears path/offset before returning finalized WAV. Reconcile from
  WAV after stop, not by adding another live reader.
- [`final_session.go`](../../internal/speech/final_session.go#L317): main Finish
  already verifies live prefix digest and feeds final-WAV tail. Reuse the algorithm,
  not its replay fallback: backup cannot restart recognition on missing coverage.
- [`sherpa_cgo.go`](../../internal/speech/sherpa_cgo.go#L237): StopStream adds encoder
  padding, signals InputFinished, decodes, then deletes active stream. Context is
  checked between native decode calls, not while one executes. Abort/Close serialize.

## Error and persistence surfaces

[`server.go`](../../internal/speech/server.go#L112) has private inferenceServerError
for transport/read/5xx/JSON error; local file errors and 4xx are ordinary errors.
It does not itself prove GPU or distinguish readiness inference. Supervisor's
private serverReadinessError is startup-only. Export a narrowly qualified request
error wrapping the original cause; preserve errors.Is/As across chunking.

[`history.go`](../../internal/history/history.go#L29) stores only time/text today.
Optional source/model/warning fields are backward-compatible JSON, not a migration.
Daemon already cleans text and records once before Emit; retain that sole dispatch.

## Fixture comparison and desktop gap

| Candidate | Decision |
| :--- | :--- |
| Existing benchmark/real-speech WAV | Not selected: attribution/license not established in inspected fixture tree |
| Silence or health-only probe | Not selected: silent shortcuts do not exercise representative voiced decode |
| Bundled public speech download | Not selected: adds provenance/distribution review and unnecessary network acquisition |
| Locally generated voiced vowels | Selected: nonzero short voiced decode, owned generation recipe, no blob/license or private audio |

[`capture.go`](../../test/desktop/capture.go#L24) feeds constant RMS for every scene;
that proves calibration, not motion. Its BuildReport says twelve captures despite
already comparing catalog length. [`report.go`](../../test/desktop/report.go#L82)
is the shared catalog; [`oracle.go`](../../test/desktop/oracle.go#L42) independently
locates the shared painter and permits animation differences only in indicators.
Keep it strict. Both backend Show methods reject enum values above Error, and
painter subtitle sizing/drawing is gated to Recording: added enums alone do not
render initialization diagnostics. GNOME harness and test child also have explicit
visual name maps to extend. The reported near-full bars follow logarithmic calibration and are
not evidence that raw microphone levels are binary.

Retained host evidence supplied by the parent identifies ten-second Vulkan load
deadlines, not proven OOM or shader warm-up. This scout performed no host mutation
or independent GPU test.
