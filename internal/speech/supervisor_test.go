package speech

import (
	"context"
	"fmt"
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

// TestHelperServerProcess stands in for whisper.cpp's `whisper-server`. It
// binds a host and port and — like the real binary — treats `--socket` as an
// unknown argument and exits. A fake that accepted any flag is why the daemon
// shipped for weeks unable to start this engine at all.
func TestHelperServerProcess(t *testing.T) {
	if os.Getenv("TEST_SUPERVISOR_HELPER") != "1" {
		return
	}
	args := os.Args
	var host, port string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--socket":
			fmt.Fprintln(os.Stderr, "error: unknown argument: --socket")
			os.Exit(2)
		case "--host":
			if i+1 < len(args) {
				host = args[i+1]
			}
		case "--port":
			if i+1 < len(args) {
				port = args[i+1]
			}
		}
	}
	if host == "" || port == "" {
		fmt.Fprintln(os.Stderr, "error: no --host/--port given")
		os.Exit(2)
	}

	listener, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: listen:", err)
		os.Exit(1)
	}
	defer listener.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// whisper.cpp serves /inference and nothing else.
			if r.URL.Path != "/inference" {
				http.Error(w, "File Not Found", http.StatusNotFound)
				return
			}
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

// whisperServerCommand launches the helper the way the supervisor launches the
// real binary: with whatever endpoint the supervisor decided the child should
// bind.
func whisperServerCommand(_ context.Context, cfg SupervisorConfig) *exec.Cmd {
	host, port := hostPort(cfg.ServerSocket)
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperServerProcess", "--", "--host", host, "--port", port)
	cmd.Env = append(os.Environ(), "TEST_SUPERVISOR_HELPER=1")
	return cmd
}

// A Unix socket path in the config means "run a local server for me". The
// binary that runs cannot bind a Unix socket, so the supervisor picks a
// loopback port and tells everyone — including the client — where it went.
func TestSupervisorConfiguredWithASocketPathListensOnLoopback(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "supervisor-test.sock")
	sup := NewSupervisor(SupervisorConfig{
		ServerSocket: sockPath,
		PollInterval: 10 * time.Millisecond,
		ReadyTimeout: 5 * time.Second,
		CommandFunc:  whisperServerCommand,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Supervisor.Start failed: %v", err)
	}
	defer sup.Stop()

	if !sup.IsRunning() {
		t.Fatal("expected supervisor to report running")
	}
	if sup.PID() <= 0 {
		t.Fatalf("expected PID > 0, got %d", sup.PID())
	}
	ep := sup.Endpoint()
	if !strings.HasPrefix(ep, "http://127.0.0.1:") {
		t.Fatalf("Endpoint() = %q, want a loopback HTTP endpoint", ep)
	}
	if _, err := os.Stat(sockPath); err == nil {
		t.Error("a socket file was created; nothing listens on it and its presence invites a client to dial it")
	}

	// The client must follow the supervisor to where the server actually is.
	st := NewServerTranscriber(sockPath)
	st.Supervisor = sup
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	_ = os.WriteFile(wavPath, []byte("fake-wav"), 0o644)
	got, err := st.Transcribe(ctx, wavPath)
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if got != "fake server transcript" {
		t.Fatalf("got %q, want %q", got, "fake server transcript")
	}

	if err := sup.Stop(); err != nil {
		t.Fatalf("Supervisor.Stop failed: %v", err)
	}
	if sup.IsRunning() {
		t.Fatal("expected supervisor to report not running after Stop")
	}
}

// A restart lands the child on a different port. Anything the client cached
// about where to send requests has to survive that.
func TestTranscriberFollowsTheServerAcrossARestart(t *testing.T) {
	sup := NewSupervisor(SupervisorConfig{
		ServerSocket: filepath.Join(t.TempDir(), "restart-follow.sock"),
		PollInterval: 10 * time.Millisecond,
		ReadyTimeout: 5 * time.Second,
		CommandFunc:  whisperServerCommand,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := sup.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sup.Stop()

	st := NewServerTranscriber(sup.Endpoint())
	st.Supervisor = sup
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	_ = os.WriteFile(wavPath, []byte("fake-wav"), 0o644)

	if _, err := st.Transcribe(ctx, wavPath); err != nil {
		t.Fatalf("first Transcribe: %v", err)
	}
	first := sup.Endpoint()
	if err := sup.Restart(ctx); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if sup.Endpoint() == first {
		t.Skip("the restarted child reused the same port; nothing to check")
	}
	if _, err := st.Transcribe(ctx, wavPath); err != nil {
		t.Fatalf("Transcribe after restart went to the old address: %v", err)
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
		CommandFunc:  whisperServerCommand,
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
		CommandFunc:  whisperServerCommand,
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
		ServerSocket: "http://127.0.0.1:9090",
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
	// whisper.cpp's server has no --socket flag; passing one makes it exit
	// before it binds anything.
	if strings.Contains(args, "--socket") {
		t.Errorf("argv passes --socket, which the server rejects: %s", args)
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
