package speech

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHelperServerProcess(t *testing.T) {
	if os.Getenv("TEST_SUPERVISOR_HELPER") != "1" {
		return
	}
	args := os.Args
	var socketPath string
	for i := 0; i < len(args); i++ {
		if args[i] == "--socket" && i+1 < len(args) {
			socketPath = args[i+1]
			break
		}
	}
	if socketPath == "" {
		os.Exit(2)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		os.Exit(1)
	}
	defer listener.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text": "fake server transcript"}`))
		}),
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		_ = srv.Close()
		_ = listener.Close()
		os.Exit(0)
	}()

	_ = srv.Serve(listener)
	os.Exit(0)
}

func TestSupervisorStartAndStopUnixSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "supervisor-test.sock")
	sup := NewSupervisor(SupervisorConfig{
		ServerSocket: sockPath,
		PollInterval: 10 * time.Millisecond,
		ReadyTimeout: 3 * time.Second,
		CommandFunc: func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=TestHelperServerProcess", "--", "--socket", cfg.ServerSocket)
			cmd.Env = append(os.Environ(), "TEST_SUPERVISOR_HELPER=1")
			return cmd
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Supervisor.Start failed: %v", err)
	}

	if !sup.IsRunning() {
		t.Fatal("expected supervisor to report running")
	}
	if sup.PID() <= 0 {
		t.Fatalf("expected PID > 0, got %d", sup.PID())
	}

	// Verify server responds
	st := NewServerTranscriber(sockPath)
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	_ = os.WriteFile(wavPath, []byte("fake-wav"), 0o644)
	got, err := st.Transcribe(ctx, wavPath)
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if got != "fake server transcript" {
		t.Fatalf("got %q, want %q", got, "fake server transcript")
	}

	// Stop supervisor
	if err := sup.Stop(); err != nil {
		t.Fatalf("Supervisor.Stop failed: %v", err)
	}
	if sup.IsRunning() {
		t.Fatal("expected supervisor to report not running after Stop")
	}
}

func TestSupervisorReadinessTimeout(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "timeout-test.sock")
	sup := NewSupervisor(SupervisorConfig{
		ServerSocket: sockPath,
		PollInterval: 10 * time.Millisecond,
		ReadyTimeout: 100 * time.Millisecond,
		CommandFunc: func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd {
			// Runs sleep so it never listens on socket
			return exec.Command("sleep", "10")
		},
	})

	err := sup.Start(context.Background())
	if err == nil {
		t.Fatal("expected error on readiness timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error %q should mention timed out", err)
	}
	if sup.IsRunning() {
		t.Fatal("supervisor should not be running after failed start")
	}
}

func TestSupervisorPrematureExit(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "crash-test.sock")
	sup := NewSupervisor(SupervisorConfig{
		ServerSocket: sockPath,
		PollInterval: 10 * time.Millisecond,
		ReadyTimeout: 2 * time.Second,
		CommandFunc: func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd {
			return exec.Command("sh", "-c", "echo simulated crash >&2; exit 3")
		},
	})

	err := sup.Start(context.Background())
	if err == nil {
		t.Fatal("expected error on premature child exit")
	}
	if !strings.Contains(err.Error(), "simulated crash") && !strings.Contains(err.Error(), "exited prematurely") {
		t.Fatalf("error %q should describe premature exit", err)
	}
	if sup.IsRunning() {
		t.Fatal("supervisor should not be running after crash")
	}
}

func TestSupervisorRestart(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "restart-test.sock")
	sup := NewSupervisor(SupervisorConfig{
		ServerSocket: sockPath,
		PollInterval: 10 * time.Millisecond,
		ReadyTimeout: 3 * time.Second,
		CommandFunc: func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=TestHelperServerProcess", "--", "--socket", cfg.ServerSocket)
			cmd.Env = append(os.Environ(), "TEST_SUPERVISOR_HELPER=1")
			return cmd
		},
	})

	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	firstPID := sup.PID()

	if err := sup.Restart(ctx); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	secondPID := sup.PID()

	if firstPID == secondPID {
		t.Fatalf("expected different PID after restart, got %d for both", firstPID)
	}
	if !sup.IsRunning() {
		t.Fatal("expected supervisor to be running after restart")
	}

	_ = sup.Stop()
}

func TestSupervisorIdempotent(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "idempotent-test.sock")
	sup := NewSupervisor(SupervisorConfig{
		ServerSocket: sockPath,
		PollInterval: 10 * time.Millisecond,
		ReadyTimeout: 3 * time.Second,
		CommandFunc: func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd {
			cmd := exec.Command(os.Args[0], "-test.run=TestHelperServerProcess", "--", "--socket", cfg.ServerSocket)
			cmd.Env = append(os.Environ(), "TEST_SUPERVISOR_HELPER=1")
			return cmd
		},
	})

	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("second Start should succeed idempotently: %v", err)
	}
	if err := sup.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := sup.Stop(); err != nil {
		t.Fatalf("second Stop should succeed idempotently: %v", err)
	}
}

func TestDefaultServerCommandArgs(t *testing.T) {
	cfg := SupervisorConfig{
		BinaryPath:   "/usr/bin/whisper-server",
		ModelPath:    "/models/base.bin",
		ServerSocket: "/run/user/1000/mavor-server.sock",
		GPULayers:    16,
		Threads:      6,
	}
	cmd := DefaultServerCommand(context.Background(), cfg)
	args := strings.Join(cmd.Args, " ")

	if !strings.Contains(args, "-m /models/base.bin") {
		t.Errorf("expected -m in args: %s", args)
	}
	if !strings.Contains(args, "-ngl 16") {
		t.Errorf("expected -ngl 16 in args: %s", args)
	}
	if !strings.Contains(args, "-t 6") {
		t.Errorf("expected -t 6 in args: %s", args)
	}
	if !strings.Contains(args, "--socket /run/user/1000/mavor-server.sock") {
		t.Errorf("expected --socket in args: %s", args)
	}
}

func TestDefaultServerCommandHTTPArgs(t *testing.T) {
	cfg := SupervisorConfig{
		BinaryPath:   "/usr/bin/whisper-server",
		ModelPath:    "/models/base.bin",
		ServerSocket: "http://127.0.0.1:8080",
	}
	cmd := DefaultServerCommand(context.Background(), cfg)
	args := strings.Join(cmd.Args, " ")

	if !strings.Contains(args, "--host 127.0.0.1") {
		t.Errorf("expected --host 127.0.0.1 in args: %s", args)
	}
	if !strings.Contains(args, "--port 8080") {
		t.Errorf("expected --port 8080 in args: %s", args)
	}
}

func TestIsUnixSocket(t *testing.T) {
	tests := []struct {
		endpoint string
		wantUnix bool
		wantPath string
	}{
		{"unix:///run/mavor.sock", true, "/run/mavor.sock"},
		{"/run/user/1000/mavor.sock", true, "/run/user/1000/mavor.sock"},
		{"./mavor.sock", true, "./mavor.sock"},
		{"http://localhost:8080", false, ""},
		{"https://api.example.com", false, ""},
		{"127.0.0.1:8080", false, ""},
	}
	for _, tc := range tests {
		gotUnix, gotPath := IsUnixSocket(tc.endpoint)
		if gotUnix != tc.wantUnix || gotPath != tc.wantPath {
			t.Errorf("IsUnixSocket(%q) = (%v, %q), want (%v, %q)",
				tc.endpoint, gotUnix, gotPath, tc.wantUnix, tc.wantPath)
		}
	}
}
