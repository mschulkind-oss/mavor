package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Each sherpa model is measured in a child process rather than in this one.
//
// It is not a matter of taste. sherpa-onnx reports a model it cannot load by
// aborting the process from C++ — no Go error, no recover, exit status 255 —
// so a single misclassified model would end a 24-model sweep at the model
// that failed and lose every result before it. Isolating each one turns a
// fatal abort into a single failed cell with its stderr attached.
//
// It also buys a better memory number. In-process, the only figure available
// is the delta in this process's VmHWM, which is a floor: the allocator does
// not return pages between models, so the second model's cost hides behind
// the first one's peak. A child has its own high-water mark, and
// getrusage(RUSAGE_CHILDREN) reports it exactly — the same measurement the
// whisper rows already get, which also makes the two engines comparable.

// workerEnv names the variable that puts a re-executed copy into worker mode.
// An environment variable rather than a flag, so it cannot collide with the
// user's own arguments and does not appear in -help as though it were a
// supported entry point.
const workerEnv = "MAVOR_BENCH_SHERPA_WORKER"

// workerRequest is what the parent asks a child to do: one model, one mode,
// one file.
type workerRequest struct {
	Model     string `json:"model"`
	ModelDir  string `json:"model_dir"`
	Audio     string `json:"audio"`
	Threads   int    `json:"threads"`
	Provider  string `json:"provider"`
	Streaming bool   `json:"streaming"`
}

// workerResponse is what it reports back on stdout. Timings only — the parent
// scores accuracy, so that every backend is scored by identical code.
type workerResponse struct {
	Text         string  `json:"text"`
	LoadMS       float64 `json:"load_ms"`
	TotalMS      float64 `json:"total_ms"`
	FirstTokenMS float64 `json:"first_token_ms"`
	Error        string  `json:"error,omitempty"`
}

// runWorkerIfRequested handles the child side. It returns true when this
// process was a worker and has finished, so main can exit without parsing
// flags or writing a report.
func runWorkerIfRequested() bool {
	payload := os.Getenv(workerEnv)
	if payload == "" {
		return false
	}
	var req workerRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		emit(workerResponse{Error: fmt.Sprintf("bad worker request: %v", err)})
		return true
	}

	s := sherpaRunner{modelDir: req.ModelDir, threads: req.Threads, provider: req.Provider}
	ctx := context.Background()

	if req.Streaming {
		text, load, first, total, err := s.streamOnce(ctx, req.Model, req.Audio)
		emit(workerResponse{
			Text:         text,
			LoadMS:       float64(load) / float64(time.Millisecond),
			TotalMS:      float64(load+total) / float64(time.Millisecond),
			FirstTokenMS: float64(first) / float64(time.Millisecond),
			Error:        errString(err),
		})
		return true
	}

	text, load, infer, err := s.batchOnce(ctx, req.Model, req.Audio)
	emit(workerResponse{
		Text:    text,
		LoadMS:  float64(load) / float64(time.Millisecond),
		TotalMS: float64(load+infer) / float64(time.Millisecond),
		Error:   errString(err),
	})
	return true
}

// emit writes the response on stdout. Engine chatter goes to stderr, so the
// parent can parse stdout without filtering.
func emit(r workerResponse) {
	json.NewEncoder(os.Stdout).Encode(r)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// callWorker re-executes this binary for one model and reads the result back.
// The child's peak RSS comes from getrusage, so it is the real high-water
// mark of a process that loaded exactly one model.
func callWorker(ctx context.Context, self string, req workerRequest) (workerResponse, int64, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return workerResponse{}, 0, err
	}

	cmd := exec.CommandContext(ctx, self)
	cmd.Env = append(os.Environ(), workerEnv+"="+string(payload))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	var peakKB int64
	if cmd.ProcessState != nil {
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			peakKB = int64(ru.Maxrss)
		}
	}

	if runErr != nil {
		// A model sherpa-onnx refuses to load kills the child outright, and
		// the only account of why is on stderr. Carrying it into the report
		// is the difference between "canary-1b failed" and a message naming
		// the metadata key that was missing.
		detail := tailLines(stderr.String(), 3)
		if detail == "" {
			detail = runErr.Error()
		}
		return workerResponse{}, peakKB, fmt.Errorf("%s", detail)
	}

	var resp workerResponse
	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		return workerResponse{}, peakKB, fmt.Errorf("worker produced no result: %v (stderr: %s)", err, tailLines(stderr.String(), 2))
	}
	if resp.Error != "" {
		return resp, peakKB, fmt.Errorf("%s", resp.Error)
	}
	return resp, peakKB, nil
}
