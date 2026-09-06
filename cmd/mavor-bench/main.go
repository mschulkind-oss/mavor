// Command mavor-bench measures every model in mavor's catalog: how fast it
// transcribes, how much memory it wants, and how accurate it is, on each
// backend the machine can actually run.
//
// It is built to be rerun. The same command on the same machine after a
// change, or on different hardware, produces a JSON file with the same shape
// and a machine fingerprint beside the numbers, so two runs can be compared
// without anyone having to remember how the first one was produced.
//
// What it does NOT do is invent a row. A model that is not downloaded is
// listed as absent; a backend the build cannot reach is listed as
// unavailable, with the reason. Every number in the output came from a
// process that ran on this machine.
//
// Usage:
//
//	mavor-bench [flags]
//
//	go run ./cmd/mavor-bench                 # every backend this machine has
//	go run ./cmd/mavor-bench -no-sherpa      # whisper only
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func main() {
	// A re-executed copy of this binary measures one sherpa model and exits;
	// it must not parse flags or write a report. See worker.go.
	if runWorkerIfRequested() {
		return
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mavor-bench: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	audio      string
	reference  string
	mavorBin   string
	cpuBin     string
	gpuBin     string
	gpuLibPath string
	only       string
	runs       int
	threads    int
	timeout    time.Duration
	jsonOut    string
	mdOut      string
	skipGPU    bool
	skipSherpa bool
	render     bool

	threadSweep string
	sweepModels string
	skipWarmSrv bool
}

// rerender rewrites the Markdown from a results file. It deliberately does
// not recompute anything: if a number in the report disagrees with the JSON,
// that is a rendering bug, and re-deriving values here would hide it.
func rerender(jsonPath, mdPath string) error {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return fmt.Errorf("reading results to re-render: %w", err)
	}
	var r report
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("parsing %s: %w", jsonPath, err)
	}
	if err := writeMarkdown(mdPath, &r); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "re-rendered %s from %s\n", mdPath, jsonPath)
	return nil
}

func run() error {
	var o options
	flag.StringVar(&o.audio, "audio", "test/fixtures/real_speech.wav", "WAV file to transcribe")
	flag.StringVar(&o.reference, "reference", "", "ground-truth transcript (default: <audio>.txt)")
	flag.StringVar(&o.mavorBin, "mavor", "bin/mavor", "mavor binary, used to read the model catalog")
	flag.StringVar(&o.cpuBin, "whisper-cpu", "whisper-cli", "whisper.cpp binary for the CPU column")
	flag.StringVar(&o.gpuBin, "whisper-gpu", "", "whisper.cpp binary with a GPU backend; empty skips the GPU column")
	flag.StringVar(&o.gpuLibPath, "whisper-gpu-libs", "", "directory of the GPU build's ggml backends (default: alongside the binary)")
	flag.StringVar(&o.only, "models", "", "comma-separated model names to run (default: every installed model)")
	flag.IntVar(&o.runs, "runs", 3, "timed runs per cell; the report takes the median")
	flag.IntVar(&o.threads, "threads", runtime.NumCPU()/2, "CPU threads per transcription")
	flag.DurationVar(&o.timeout, "timeout", 10*time.Minute, "per-transcription timeout")
	flag.StringVar(&o.jsonOut, "json", "docs/reports/benchmarks/latest.json", "where to write the raw results")
	flag.StringVar(&o.mdOut, "markdown", "docs/reports/model-benchmarks.md", "where to write the rendered report")
	flag.BoolVar(&o.skipGPU, "no-gpu", false, "skip the GPU column even if a GPU build was given")
	flag.BoolVar(&o.skipSherpa, "no-sherpa", false, "skip the sherpa engines even if built in")
	flag.StringVar(&o.threadSweep, "thread-sweep", "2,4,6,8", "thread counts for the thread-scaling table; empty skips it")
	flag.StringVar(&o.sweepModels, "sweep-models", "whisper-tiny.en,whisper-base.en,whisper-small.en", "models the thread and warm-server sweeps run over")
	flag.BoolVar(&o.skipWarmSrv, "no-warm-server", false, "skip the warm whisper-server comparison")
	flag.BoolVar(&o.render, "render", false, "re-render the Markdown from an existing results JSON without running anything")
	flag.Parse()

	// Re-rendering exists because the report's prose changes far more often
	// than its numbers do, and a full sweep is an hour of CPU. The JSON is
	// the artifact; the Markdown is a view of it.
	if o.render {
		return rerender(o.jsonOut, o.mdOut)
	}

	if o.reference == "" {
		o.reference = o.audio + ".txt"
	}
	if o.gpuBin != "" && o.gpuLibPath == "" {
		o.gpuLibPath = filepath.Dir(o.gpuBin)
	}

	refBytes, err := os.ReadFile(o.reference)
	if err != nil {
		return fmt.Errorf("reading ground truth: %w", err)
	}
	reference := strings.TrimSpace(string(refBytes))

	audioSec, err := wavDurationSeconds(o.audio)
	if err != nil {
		return fmt.Errorf("reading audio: %w", err)
	}

	cat, err := loadCatalog(o.mavorBin)
	if err != nil {
		return err
	}
	var only []string
	if o.only != "" {
		only = strings.Split(o.only, ",")
	}
	selected, missing := selectModels(cat, only)
	if len(selected) == 0 {
		return fmt.Errorf("no installed models to benchmark (%d in the catalog are not downloaded); run `mavor models pull <name>`", len(missing))
	}

	report := &report{
		Machine:       collectMachineInfo(),
		Audio:         o.audio,
		AudioSeconds:  audioSec,
		Reference:     reference,
		RunsPerCell:   o.runs,
		Threads:       o.threads,
		ModelDir:      cat.ModelDir,
		StreamChunkMS: streamChunkMS,
	}
	for _, m := range missing {
		report.NotInstalled = append(report.NotInstalled, m.Name)
	}

	// --- Decide which whisper columns this machine can honestly produce. ----
	//
	// Up to three, and they answer different questions. "stock" is the build a
	// user gets from their package manager, so it is the baseline that matters
	// for expectation-setting. The Vulkan build appears twice — once with -ng
	// and once without — because that pair, and only that pair, isolates the
	// GPU backend. Comparing the stock CPU build against the Vulkan GPU build
	// would fold in a different compiler, a different ggml, and a different
	// -march, and then credit the whole difference to the GPU.
	var whisperRunners []whisperRunner

	stock := whisperRunner{binary: o.cpuBin, device: "cpu", build: "stock", threads: o.threads}
	if _, err := exec.LookPath(o.cpuBin); err != nil {
		report.Skipped = append(report.Skipped, skipNote{"whisper-cli / cpu (stock)", fmt.Sprintf("%s not found on PATH", o.cpuBin)})
	} else {
		report.WhisperCPUBackends = stock.backends()
		whisperRunners = append(whisperRunners, stock)
	}

	switch {
	case o.skipGPU:
		report.Skipped = append(report.Skipped, skipNote{"whisper-cli / gpu", "disabled with -no-gpu"})
	case o.gpuBin == "":
		report.Skipped = append(report.Skipped, skipNote{"whisper-cli / gpu", "no GPU-capable whisper build given (-whisper-gpu); build one with `just bench-gpu-build`"})
	default:
		gpu := whisperRunner{binary: o.gpuBin, device: "gpu", build: "vulkan", threads: o.threads, libPath: o.gpuLibPath}
		report.WhisperGPUBackends = gpu.backends()
		switch {
		case !hasGPUBackend(report.WhisperGPUBackends):
			// Refuse to publish a GPU column from a build that brings up no
			// GPU backend. It would run, and every number would be a CPU
			// number wearing a GPU label — which is precisely the error in
			// the reports this harness replaces.
			report.Skipped = append(report.Skipped, skipNote{
				"whisper-cli / gpu",
				fmt.Sprintf("`%s` brings up only %v — no GPU backend, so a GPU column would be CPU numbers mislabelled", o.gpuBin, report.WhisperGPUBackends),
			})
		case report.Machine.GPUName == "":
			report.Skipped = append(report.Skipped, skipNote{
				"whisper-cli / gpu",
				"the build has a GPU backend but no Vulkan device enumerated, so it would have fallen back to CPU",
			})
		default:
			// The same binary with -ng, so the GPU row has something to be
			// compared against that differs in exactly one flag.
			cpuSameBuild := gpu
			cpuSameBuild.device = "cpu"
			whisperRunners = append(whisperRunners, cpuSameBuild, gpu)
		}
	}

	// The in-process sherpa engines are always linked in — mavor is a cgo
	// program with no pure-Go variant — so the only way this column goes
	// missing is someone asking for it to.
	sherpa := sherpaRunner{modelDir: cat.ModelDir, threads: o.threads, provider: "cpu"}
	sherpaOK := !o.skipSherpa
	if o.skipSherpa {
		report.Skipped = append(report.Skipped, skipNote{"sherpa", "disabled with -no-sherpa"})
	}
	// sherpa-onnx has no GPU column at all: the ONNX Runtime vendored by the
	// Go binding carries no execution providers, so requesting one falls back
	// to CPU silently. Saying so once is more honest than an empty column.
	report.Skipped = append(report.Skipped, skipNote{
		"sherpa / gpu",
		"the sherpa-onnx-go binding vendors a CPU-only ONNX Runtime with no execution providers; a GPU request falls back to CPU without saying so",
	})

	// Worker processes are re-executions of this binary, so it has to be able
	// to find itself. Under `go run` the executable is a temporary file,
	// which still works: it lives for the duration of the run.
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating this binary to spawn sherpa workers: %w", err)
	}

	ctx := context.Background()
	total := len(selected)
	for i, m := range selected {
		fmt.Fprintf(os.Stderr, "[%d/%d] %s (%s)\n", i+1, total, m.Name, m.Engine)

		switch m.Engine {
		case "whisper":
			for _, w := range whisperRunners {
				report.Results = append(report.Results, benchWhisper(ctx, w, m, cat.ModelDir, o, reference, audioSec))
			}
		case "sherpa":
			if !sherpaOK {
				continue
			}
			report.Results = append(report.Results, benchSherpa(ctx, sherpa, self, m, o, reference, audioSec, false))
			if m.Streaming {
				report.Results = append(report.Results, benchSherpa(ctx, sherpa, self, m, o, reference, audioSec, true))
			}
		}
	}

	runSweeps(ctx, report, stock, selected, cat.ModelDir, o, audioSec)

	if err := writeJSON(o.jsonOut, report); err != nil {
		return err
	}
	if err := writeMarkdown(o.mdOut, report); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nwrote %s and %s\n", o.jsonOut, o.mdOut)
	return nil
}

// runSweeps runs the two setting sweeps — thread count, and warm server
// versus cold CLI — and records why in Skipped whenever one of them does not
// run. Both are whisper-only: they vary a whisper.cpp setting, and sherpa
// exposes neither.
func runSweeps(ctx context.Context, r *report, stock whisperRunner, selected []catalogModel, modelDir string, o options, audioSec float64) {
	counts, err := parseThreadSweep(o.threadSweep)
	if err != nil {
		r.Skipped = append(r.Skipped, skipNote{"thread scaling", err.Error()})
		return
	}
	sweepModels, absent := selectSweepModels(selected, strings.Split(o.sweepModels, ","))
	if len(absent) > 0 {
		r.Skipped = append(r.Skipped, skipNote{
			"thread scaling / warm server",
			fmt.Sprintf("asked for %s, which %s not installed whisper models; pull them for a fuller sweep",
				"`"+strings.Join(absent, "`, `")+"`", plural(len(absent), "is", "are")),
		})
	}

	switch {
	case len(counts) == 0:
		r.Skipped = append(r.Skipped, skipNote{"thread scaling", "disabled with -thread-sweep="})
	case len(sweepModels) == 0:
		r.Skipped = append(r.Skipped, skipNote{"thread scaling", "none of the -sweep-models are installed"})
	case r.WhisperCPUBackends == nil:
		r.Skipped = append(r.Skipped, skipNote{"thread scaling", "no stock whisper-cli to sweep"})
	default:
		fmt.Fprintf(os.Stderr, "\nthread sweep: %d models × %v threads\n", len(sweepModels), counts)
		r.ThreadScaling = benchThreadSweep(ctx, stock, sweepModels, counts, modelDir, o, audioSec)
	}

	switch {
	case o.skipWarmSrv:
		r.Skipped = append(r.Skipped, skipNote{"warm server", "disabled with -no-warm-server"})
	case len(sweepModels) == 0:
		r.Skipped = append(r.Skipped, skipNote{"warm server", "none of the -sweep-models are installed"})
	case !whisperServerAvailable():
		r.Skipped = append(r.Skipped, skipNote{"warm server", "no whisper-server or whisper-cpp-server on PATH"})
	default:
		fmt.Fprintf(os.Stderr, "\nwarm server: %d models\n", len(sweepModels))
		r.WarmServer = benchWarmServer(ctx, sweepModels, counts, modelDir, o, audioSec, coldTimes(r.ThreadScaling))
	}
}

// whisperServerAvailable mirrors the binaries the speech supervisor looks for,
// so the report skips the section for the same reason the daemon would fail to
// start the engine.
func whisperServerAvailable() bool {
	for _, name := range []string{"whisper-server", "whisper-cpp-server"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// hasGPUBackend reports whether a ggml backend list contains anything that
// runs on a GPU. CPU-only builds list "CPU" and nothing else.
func hasGPUBackend(backends []string) bool {
	for _, b := range backends {
		switch strings.ToLower(b) {
		case "vulkan", "cuda", "metal", "rocm", "hip", "sycl", "opencl":
			return true
		}
	}
	return false
}
