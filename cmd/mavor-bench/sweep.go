package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mschulkind-oss/mavor/internal/speech"
)

// The two sweeps in this file answer questions the model tables cannot: how
// much a machine's cores buy, and what holding the model warm is worth. Both
// are `config.toml` decisions a user actually makes — `threads` and
// `engine = "server"` — and both were previously answered only by a hand-run
// script whose numbers nothing could reproduce.

// threadCell is one model at one thread count on the stock CPU build.
type threadCell struct {
	Model     string  `json:"model"`
	Threads   int     `json:"threads"`
	Runs      int     `json:"runs"`
	TotalMS   float64 `json:"total_ms"`
	RTF       float64 `json:"rtf"`
	PeakRSSKB int64   `json:"peak_rss_kb"`
	Failed    bool    `json:"failed,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// serverCell is one model under the warm `whisper-server` engine, beside the
// cold `whisper-cli` time for the same model and thread count.
type serverCell struct {
	Model     string  `json:"model"`
	Threads   int     `json:"threads"`
	Runs      int     `json:"runs"`
	StartupMS float64 `json:"startup_ms"`
	WarmMS    float64 `json:"warm_ms"`
	ColdMS    float64 `json:"cold_ms"`
	RTF       float64 `json:"rtf"`
	Failed    bool    `json:"failed,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// parseThreadSweep turns "2,4,6,8" into thread counts: sorted, deduplicated,
// and rejecting anything that is not a positive integer. An empty spec means
// the sweep is off, which is not an error.
func parseThreadSweep(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	seen := map[int]bool{}
	var out []int
	for _, field := range strings.Split(spec, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("thread sweep %q: %q is not a number", spec, field)
		}
		if n < 1 {
			return nil, fmt.Errorf("thread sweep %q: %d threads is not a thread count", spec, n)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, nil
}

// selectSweepModels picks the models the sweeps run over, in the order named,
// keeping only installed whisper models. The sweeps deliberately do not run
// the whole catalog: they vary a setting rather than compare models, and three
// sizes show the shape of the curve at a fraction of the wall time.
func selectSweepModels(installed []catalogModel, want []string) (selected []catalogModel, absent []string) {
	byName := map[string]catalogModel{}
	for _, m := range installed {
		if m.Engine != "whisper" {
			continue
		}
		byName[m.Name] = m
		for _, a := range m.Aliases {
			byName[a] = m
		}
	}
	seen := map[string]bool{}
	for _, n := range want {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		m, ok := byName[n]
		if !ok {
			absent = append(absent, n)
			continue
		}
		if !seen[m.Name] {
			seen[m.Name] = true
			selected = append(selected, m)
		}
	}
	return selected, absent
}

// benchThreadSweep times one model at each thread count on the stock CPU
// build. It uses the same runner and the same median-of-N as the model tables,
// so a cell here is comparable with the corresponding row up there.
func benchThreadSweep(ctx context.Context, w whisperRunner, models []catalogModel, counts []int, modelDir string, o options, audioSec float64) []threadCell {
	var cells []threadCell
	for _, m := range models {
		modelPath := filepath.Join(modelDir, "ggml-"+m.Name+".bin")
		for _, n := range counts {
			fmt.Fprintf(os.Stderr, "  thread sweep: %s @ %d threads\n", m.Name, n)
			cell := threadCell{Model: m.Name, Threads: n}
			runner := w
			runner.threads = n

			var times []float64
			var peak int64
			for i := 0; i < o.runs; i++ {
				runCtx, cancel := context.WithTimeout(ctx, o.timeout)
				_, elapsed, rss, err := runner.run(runCtx, modelPath, o.audio)
				cancel()
				if err != nil {
					cell.Failed, cell.Error = true, err.Error()
					break
				}
				times = append(times, float64(elapsed)/float64(time.Millisecond))
				peak = max(peak, rss)
			}
			if !cell.Failed {
				cell.Runs = len(times)
				cell.TotalMS = median(times)
				cell.PeakRSSKB = peak
				cell.RTF = cell.TotalMS / 1000 / audioSec
			}
			cells = append(cells, cell)
		}
	}
	return cells
}

// benchWarmServer measures the engine the daemon uses when `engine =
// "server"`: a `whisper-server` child holding the model in memory across
// utterances. It drives `internal/speech` rather than reimplementing the
// client, so what the report measures is the code path a user gets.
//
// Startup is timed separately and excluded from the per-utterance figure,
// because a daemon pays it once at login and never again.
func benchWarmServer(ctx context.Context, models []catalogModel, counts []int, modelDir string, o options, audioSec float64, cold map[threadKey]float64) []serverCell {
	threads := o.threads
	if len(counts) > 0 {
		// One thread count is enough: the thread question is answered by the
		// sweep above, and the server's own question — warm versus cold — is
		// clearest at a single setting. Take the middle of the sweep.
		threads = counts[len(counts)/2]
	}

	var cells []serverCell
	for _, m := range models {
		fmt.Fprintf(os.Stderr, "  warm server: %s @ %d threads\n", m.Name, threads)
		cell := serverCell{Model: m.Name, Threads: threads, ColdMS: cold[threadKey{m.Name, threads}]}
		modelPath := filepath.Join(modelDir, "ggml-"+m.Name+".bin")

		port, err := freePort()
		if err != nil {
			cell.Failed, cell.Error = true, err.Error()
			cells = append(cells, cell)
			continue
		}
		// An explicit loopback endpoint rather than the daemon's "pick one
		// for me" socket path: the benchmark needs to know the port it is
		// measuring, and a port chosen here cannot collide with the one the
		// supervisor would have chosen for a previous model still shutting
		// down. The request path is left off so the run exercises the same
		// path discovery a user gets.
		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)

		st := speech.NewServerTranscriber(endpoint)
		st.Model = m.Name
		st.Supervisor = speech.NewSupervisor(speech.SupervisorConfig{
			ModelPath:    modelPath,
			ServerSocket: endpoint,
			Threads:      threads,
			Device:       "cpu",
			Logger:       quietLogger(),
		})

		// The supervisor spawns the child with this context, so it has to
		// outlive the measurement: bounding startup with a timeout here would
		// kill the server the moment the timer was cancelled.
		startedAt := time.Now()
		err = st.Start(ctx)
		cell.StartupMS = float64(time.Since(startedAt)) / float64(time.Millisecond)
		if err != nil {
			cell.Failed, cell.Error = true, err.Error()
			_ = st.Close()
			cells = append(cells, cell)
			continue
		}

		var times []float64
		for i := 0; i < o.runs; i++ {
			runCtx, cancel := context.WithTimeout(ctx, o.timeout)
			at := time.Now()
			_, err := st.Transcribe(runCtx, o.audio)
			elapsed := time.Since(at)
			cancel()
			if err != nil {
				cell.Failed, cell.Error = true, err.Error()
				break
			}
			times = append(times, float64(elapsed)/float64(time.Millisecond))
		}
		_ = st.Close()

		if !cell.Failed {
			cell.Runs = len(times)
			cell.WarmMS = median(times)
			cell.RTF = cell.WarmMS / 1000 / audioSec
		}
		cells = append(cells, cell)
	}
	return cells
}

// threadKey indexes the cold whisper-cli times so the warm rows have
// something to be compared against at the same thread count.
type threadKey struct {
	model   string
	threads int
}

func coldTimes(cells []threadCell) map[threadKey]float64 {
	out := map[threadKey]float64{}
	for _, c := range cells {
		if !c.Failed {
			out[threadKey{c.Model, c.Threads}] = c.TotalMS
		}
	}
	return out
}

// freePort asks the kernel for an unused loopback port and hands it back. A
// fixed port would collide with whatever else is listening, and a collision
// here reads as a broken engine rather than a busy machine.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("finding a free port for the warm server: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
