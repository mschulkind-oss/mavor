package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const sendTimeout = 2 * time.Second

func socketPath(t *testing.T) string {
	t.Helper()
	// Unix socket paths are limited to 108 bytes on Linux. t.TempDir() under
	// /tmp produces short paths; we just append a fixed name.
	return filepath.Join(t.TempDir(), "mavor.sock")
}

func startServer(t *testing.T, h Handler) (string, func()) {
	t.Helper()
	return startServerAt(t, socketPath(t), h)
}

func startServerAt(t *testing.T, sock string, h Handler) (string, func()) {
	t.Helper()
	srv := NewServer(sock, h)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	// We deliberately do NOT probe readiness with Dial — that would create an
	// orphan connection the server would have to time out on shutdown. Tests
	// that need to know the server is up should retry Send via sendWithRetry.

	stop := func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Serve returned %v, want nil or context.Canceled", err)
			}
		case <-time.After(sendTimeout):
			t.Fatal("Serve did not exit after context cancel")
		}
	}
	return sock, stop
}

// sendWithRetry retries Send on dial errors (e.g., the server hasn't bound
// yet). Real callers don't need this — production startup completes before
// the user can press their hotkey — but tests race the goroutine.
func sendWithRetry(t *testing.T, sock string, req Request) Response {
	t.Helper()
	deadline := time.Now().Add(sendTimeout)
	for {
		resp, err := Send(sock, req, 200*time.Millisecond)
		if err == nil {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("Send: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSendInvokesHandlerAndReturnsResponse(t *testing.T) {
	sock, stop := startServer(t, func(r Request) Response {
		switch r.Action {
		case "toggle":
			return Response{State: "recording"}
		case "start":
			return Response{State: "recording"}
		case "stop":
			return Response{State: "transcribing"}
		default:
			t.Errorf("unexpected action %q", r.Action)
			return Response{Error: "unexpected action"}
		}
	})
	defer stop()

	for _, action := range []string{"toggle", "start", "stop"} {
		resp := sendWithRetry(t, sock, Request{Action: action})
		if resp.Error != "" {
			t.Fatalf("action %q returned error: %q", action, resp.Error)
		}
		if action == "stop" {
			if resp.State != "transcribing" {
				t.Fatalf("action %q returned state %q, want %q", action, resp.State, "transcribing")
			}
		} else {
			if resp.State != "recording" {
				t.Fatalf("action %q returned state %q, want %q", action, resp.State, "recording")
			}
		}
	}
}

func TestServerIsGracefullyShutDownByContext(t *testing.T) {
	sock, stop := startServer(t, func(Request) Response { return Response{State: "idle"} })
	// Ensure the server is actually up before shutting it down, so we're
	// testing graceful shutdown rather than racing startup.
	sendWithRetry(t, sock, Request{Action: "toggle"})
	stop()

	// After shutdown the socket file should be removed so a new daemon can
	// bind cleanly.
	if _, err := net.DialTimeout("unix", sock, 100*time.Millisecond); err == nil {
		t.Fatalf("socket %s still accepts connections after shutdown", sock)
	}
}

func TestSendErrorsWhenSocketAbsent(t *testing.T) {
	sock := socketPath(t)
	if _, err := Send(sock, Request{Action: "toggle"}, 200*time.Millisecond); err == nil {
		t.Fatal("Send to nonexistent socket should error")
	}
}

func TestStaleSocketFileIsReplaced(t *testing.T) {
	sock := socketPath(t)
	// Simulate a crashed daemon: bind, then close WITHOUT unlinking, leaving
	// a socket file with no live listener. (Go's default Close unlinks.)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("seed stale socket: %v", err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("stale socket file not present after close: %v", err)
	}

	sock2, stop := startServerAt(t, sock, func(Request) Response { return Response{State: "idle"} })
	defer stop()
	if sock2 != sock {
		t.Fatalf("startServerAt returned %q, want %q", sock2, sock)
	}

	resp := sendWithRetry(t, sock, Request{Action: "toggle"})
	if resp.State != "idle" {
		t.Fatalf("Response.State = %q, want %q", resp.State, "idle")
	}
}

func TestLiveSocketPreventsSecondServer(t *testing.T) {
	sock, stop := startServer(t, func(Request) Response { return Response{State: "ok"} })
	defer stop()
	// Ensure the first server has actually bound before we probe.
	sendWithRetry(t, sock, Request{Action: "toggle"})

	srv := NewServer(sock, func(Request) Response { return Response{} })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := srv.Serve(ctx); err == nil {
		t.Fatal("second Serve on live socket should fail, got nil")
	}
}

func TestConcurrentClients(t *testing.T) {
	var calls int32
	sock, stop := startServer(t, func(Request) Response {
		atomic.AddInt32(&calls, 1)
		return Response{State: "ok"}
	})
	defer stop()

	// First request waits for the server to bind, then subsequent ones can
	// fire concurrently without racing startup.
	sendWithRetry(t, sock, Request{Action: "toggle"})

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			if _, err := Send(sock, Request{Action: "toggle"}, sendTimeout); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Send: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != n+1 {
		t.Fatalf("handler called %d times, want %d", got, n+1)
	}
}
