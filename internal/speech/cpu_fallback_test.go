package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The helper injects GPU failures without exhausting the user's real GPU.
// Recovery still launches real child processes and transfers WAV bytes over HTTP.
func TestCPURecoveryHelperProcess(t *testing.T) {
	if os.Getenv("MAVOR_CPU_RECOVERY_HELPER") != "1" {
		return
	}
	mode := os.Getenv("MAVOR_CPU_RECOVERY_MODE")
	if mode == "startup-exit" || mode == "cpu-startup-exit" {
		fmt.Fprintln(os.Stderr, mode+": simulated device allocation failure")
		os.Exit(1)
	}
	if mode == "startup-timeout" {
		// Keep writing while the parent checks readiness, exercising stderr synchronization.
		for {
			fmt.Fprintln(os.Stderr, "simulated GPU model loading")
			time.Sleep(time.Millisecond)
		}
	}
	if delay, err := time.ParseDuration(os.Getenv("MAVOR_CPU_RECOVERY_DELAY")); err == nil {
		time.Sleep(delay)
	}
	listener, err := net.Listen("tcp", os.Getenv("MAVOR_CPU_RECOVERY_ADDRESS"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "inference-failure", "cpu-inference-failure":
			http.Error(w, mode+": simulated device allocation failure", http.StatusInternalServerError)
			return
		case "disconnect":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		case "client-error":
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		audio, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": string(audio)})
	})}
	_ = srv.Serve(listener)
	os.Exit(0)
}

type recoveryLaunch struct {
	cpu      bool
	endpoint string
}

type recoveryLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *recoveryLogs) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *recoveryLogs) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func recoverySupervisor(t *testing.T, gpuMode, cpuMode string, noGPU bool) (*Supervisor, *[]recoveryLaunch, *recoveryLogs) {
	t.Helper()
	var launches []recoveryLaunch
	var logs recoveryLogs
	sup := NewSupervisor(SupervisorConfig{
		NoGPU:        noGPU,
		ReadyTimeout: 500 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(&logs, nil)),
		CommandFunc: func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd {
			launches = append(launches, recoveryLaunch{cpu: cfg.NoGPU, endpoint: cfg.ServerSocket})
			mode := gpuMode
			if cfg.NoGPU {
				mode = cpuMode
			}
			host, port := hostPort(cfg.ServerSocket)
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCPURecoveryHelperProcess$")
			cmd.Env = append(os.Environ(), "MAVOR_CPU_RECOVERY_HELPER=1", "MAVOR_CPU_RECOVERY_MODE="+mode, "MAVOR_CPU_RECOVERY_ADDRESS="+net.JoinHostPort(host, port))
			return cmd
		},
	})
	t.Cleanup(func() { _ = sup.Stop() })
	return sup, &launches, &logs
}

func recoveryWAV(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recording.wav")
	if err := os.WriteFile(path, []byte("the exact original recording"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGPUStartupFailureRetriesCPUWithOriginalRecording(t *testing.T) {
	for _, mode := range []string{"startup-exit", "startup-timeout"} {
		t.Run(mode, func(t *testing.T) {
			sup, launches, logs := recoverySupervisor(t, mode, "success", false)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st := NewServerTranscriber("")
			st.Supervisor = sup
			path := recoveryWAV(t)
			text, err := st.Transcribe(ctx, path)
			if err != nil {
				t.Fatalf("recording lost instead of CPU recovery: %v", err)
			}
			if text != "the exact original recording" {
				t.Fatalf("CPU got different audio: %q", text)
			}
			if len(*launches) != 2 || (*launches)[0].cpu || !(*launches)[1].cpu {
				t.Fatalf("launches = %+v", *launches)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("original recording removed: %v", err)
			}
			// CPU is kept warm for subsequent recordings, including explicit restarts.
			if _, err := st.Transcribe(ctx, path); err != nil {
				t.Fatal(err)
			}
			if len(*launches) != 2 {
				t.Fatal("retried GPU on the next dictation")
			}
			if err := sup.Restart(ctx); err != nil {
				t.Fatal(err)
			}
			if len(*launches) != 3 || !(*launches)[2].cpu {
				t.Fatal("CPU choice did not survive child restart")
			}
			if err := sup.Stop(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "CPU") {
				t.Fatalf("missing recovery warning: %s", logs)
			}
		})
	}
}

func TestGPUInferenceFailureRetriesCPUWithOriginalRecording(t *testing.T) {
	for _, mode := range []string{"inference-failure", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			sup, launches, _ := recoverySupervisor(t, mode, "success", false)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st := NewServerTranscriber("")
			st.Supervisor = sup
			text, err := st.Transcribe(ctx, recoveryWAV(t))
			if err != nil {
				t.Fatalf("recording lost instead of CPU recovery: %v", err)
			}
			if text != "the exact original recording" {
				t.Fatalf("CPU got different audio: %q", text)
			}
			if len(*launches) != 2 || !(*launches)[1].cpu {
				t.Fatalf("launches = %+v", *launches)
			}
		})
	}
}

func TestCPURecoveryIsBoundedWhenBothDevicesFail(t *testing.T) {
	for _, mode := range []string{"startup", "inference"} {
		t.Run(mode, func(t *testing.T) {
			gpuMode, cpuMode := "startup-exit", "cpu-startup-exit"
			if mode == "inference" {
				gpuMode, cpuMode = "inference-failure", "cpu-inference-failure"
			}
			sup, launches, _ := recoverySupervisor(t, gpuMode, cpuMode, false)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st := NewServerTranscriber("")
			st.Supervisor = sup
			text, err := st.Transcribe(ctx, recoveryWAV(t))
			if err == nil || text != "" {
				t.Fatalf("both failures must remain an error: text=%q err=%v", text, err)
			}
			if len(*launches) != 2 || !(*launches)[1].cpu {
				t.Fatalf("want exactly one CPU retry: %+v", *launches)
			}
			if !strings.Contains(err.Error(), "cpu-") {
				t.Fatalf("missing CPU failure reason: %v", err)
			}
		})
	}
}

func TestCPURecoveryDoesNotRetryExplicitCPUOrInvalidInput(t *testing.T) {
	for _, mode := range []string{"cpu-only", "bad-request", "missing-wav", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			gpuMode := "inference-failure"
			if mode == "bad-request" {
				gpuMode = "client-error"
			}
			if mode == "missing-wav" {
				gpuMode = "success"
			}
			if mode == "canceled" {
				gpuMode = "startup-timeout"
			}
			sup, launches, _ := recoverySupervisor(t, gpuMode, "cpu-inference-failure", mode == "cpu-only")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if mode == "canceled" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
			}
			defer cancel()
			st := NewServerTranscriber("")
			st.Supervisor = sup
			path := recoveryWAV(t)
			if mode == "missing-wav" {
				path += ".missing"
			}
			if _, err := st.Transcribe(ctx, path); err == nil {
				t.Fatal("expected failure")
			}
			if len(*launches) != 1 {
				t.Fatalf("unexpected CPU retry: %+v", *launches)
			}
		})
	}
}

func TestCPURecoveryDoesNotRestartRemoteServer(t *testing.T) {
	var calls atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); http.Error(w, "device failure", 500) })}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()
	st := NewServerTranscriber("http://" + listener.Addr().String())
	if _, err := st.Transcribe(context.Background(), recoveryWAV(t)); err == nil {
		t.Fatal("expected remote server error")
	}
	if calls.Load() != 1 {
		t.Fatalf("remote request retried %d times", calls.Load())
	}
}

type lifecycleTranscriber struct {
	starts, closes int
	startContext   context.Context
	err            error
}

func (t *lifecycleTranscriber) Transcribe(context.Context, string) (string, error) { return "", nil }
func (t *lifecycleTranscriber) Start(ctx context.Context) error {
	t.starts++
	t.startContext = ctx
	return t.err
}
func (t *lifecycleTranscriber) Close() error { t.closes++; return t.err }

func TestChunkingPreservesEngineStartupAndShutdown(t *testing.T) {
	wrapped := &lifecycleTranscriber{err: errors.New("engine failure")}
	tr := WrapChunking(wrapped, "auto", nil)
	starter, ok := tr.(interface{ Start(context.Context) error })
	if !ok {
		t.Fatal("chunking hides warm server startup")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := starter.Start(ctx); !errors.Is(err, wrapped.err) {
		t.Fatalf("startup error swallowed: %v", err)
	}
	closer, ok := tr.(io.Closer)
	if !ok {
		t.Fatal("chunking hides child server shutdown")
	}
	if err := closer.Close(); !errors.Is(err, wrapped.err) {
		t.Fatalf("shutdown error swallowed: %v", err)
	}
	if wrapped.starts != 1 || wrapped.closes != 1 || wrapped.startContext != ctx {
		t.Fatalf("lifecycle not forwarded: %+v", wrapped)
	}
}

func TestGPURecoveryAllowsSlowerCPULoad(t *testing.T) {
	sup, launches, _ := recoverySupervisor(t, "startup-exit", "success", false)
	sup.cfg.ReadyTimeout = 100 * time.Millisecond
	sup.cfg.CPUReadyTimeout = time.Second
	command := sup.cfg.CommandFunc
	sup.cfg.CommandFunc = func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd {
		cmd := command(ctx, cfg)
		if cfg.NoGPU {
			cmd.Env = append(cmd.Env, "MAVOR_CPU_RECOVERY_DELAY=250ms")
		}
		return cmd
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("CPU inherited overly short GPU startup deadline: %v", err)
	}
	if len(*launches) != 2 || sup.GPUEnabled() {
		t.Fatalf("CPU recovery missing: %+v", *launches)
	}
}

func TestCPURecoveryStartupTimeoutIsBounded(t *testing.T) {
	sup, launches, _ := recoverySupervisor(t, "startup-exit", "startup-timeout", false)
	sup.cfg.CPUReadyTimeout = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	err := sup.Start(ctx)
	if err == nil || !strings.Contains(err.Error(), "CPU recovery failed") || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("missing bounded CPU startup failure: %v", err)
	}
	if len(*launches) != 2 || sup.IsRunning() {
		t.Fatalf("extra retry or leaked process: %+v", *launches)
	}
	if time.Since(started) > time.Second {
		t.Fatal("CPU ignored its readiness timeout")
	}
}

func TestCPURecoveryDoesNotRetryMissingServerBinary(t *testing.T) {
	launches := 0
	sup := NewSupervisor(SupervisorConfig{CommandFunc: func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd {
		launches++
		return exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-whisper-server"))
	}})
	if err := sup.Start(context.Background()); err == nil {
		t.Fatal("expected missing binary error")
	}
	if launches != 1 {
		t.Fatalf("missing binary incorrectly treated as GPU failure: launches=%d", launches)
	}
}

func TestCPURecoveryDefaultStartupDeadlines(t *testing.T) {
	gpu := NewSupervisor(SupervisorConfig{})
	cpu := NewSupervisor(SupervisorConfig{NoGPU: true})
	if gpu.cfg.ReadyTimeout != 10*time.Second || cpu.cfg.ReadyTimeout != 60*time.Second || gpu.cfg.CPUReadyTimeout != 60*time.Second {
		t.Fatal("unexpected startup deadlines")
	}
}

func TestChildStderrKeepsBoundedDiagnosticTail(t *testing.T) {
	var stderr childStderr
	_, _ = stderr.Write(bytes.Repeat([]byte("x"), 128*1024))
	_, _ = stderr.Write([]byte("last allocation failure"))
	tail := stderr.String()
	if len(tail) != 64*1024 || !strings.HasSuffix(tail, "last allocation failure") {
		t.Fatal("diagnostics lost their bounded error tail")
	}
}

func TestChunkingLifecycleIsSafeForEnginesWithoutLifecycle(t *testing.T) {
	for _, wrapped := range []Transcriber{&Mock{}, &MockStreamTranscriber{}} {
		tr := WrapChunking(wrapped, "auto", nil)
		if err := tr.(interface{ Start(context.Context) error }).Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := tr.(io.Closer).Close(); err != nil {
			t.Fatal(err)
		}
	}
}
