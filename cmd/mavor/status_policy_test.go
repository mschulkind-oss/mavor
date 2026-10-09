package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/ipc"
)

// `mavor status` prints the derived state first and the live dictations stacked
// beneath it, recording row first, with provenance kept on stderr.
func TestStatusRendersStateAndStackOnStdout(t *testing.T) {
	var out, diag bytes.Buffer
	resp := ipc.Response{
		State:        "recording",
		Recording:    true,
		Transcribing: 2,
		Source:       "companion-backup",
		Warning:      "GPU-enabled server request failed",
	}
	if err := writeStatus(&out, &diag, resp); err != nil {
		t.Fatal(err)
	}
	want := "recording\n  - recording\n  - transcribing\n  - transcribing\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
	if !strings.Contains(diag.String(), "companion-backup") || !strings.Contains(diag.String(), "GPU-enabled server request failed") {
		t.Fatalf("stderr = %q, want provenance", diag.String())
	}
}

// Idle with nothing in flight is just the state word: no empty stack lines.
func TestStatusIdleHasNoStackRows(t *testing.T) {
	var out, diag bytes.Buffer
	if err := writeStatus(&out, &diag, ipc.Response{State: "idle"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "idle\n" {
		t.Fatalf("stdout = %q, want %q", out.String(), "idle\n")
	}
}

// Beyond the three visible rows the rest collapse to a count.
func TestStatusStackCollapsesBeyondThreeRows(t *testing.T) {
	var out, diag bytes.Buffer
	if err := writeStatus(&out, &diag, ipc.Response{State: "recording", Recording: true, Transcribing: 5}); err != nil {
		t.Fatal(err)
	}
	want := "recording\n  - recording\n  - transcribing\n  - transcribing\n  - ... and 3 more\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
}

// startStatusServer puts a one-response daemon on the socket runStatus reads
// from config.Load and returns once it is reachable. Readiness is probed with a
// complete request/response: a bare Dial would leave the server waiting out its
// read deadline on shutdown.
func startStatusServer(t *testing.T, resp ipc.Response) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	sock := filepath.Join(dir, "mavor.sock")
	srv := ipc.NewServer(sock, func(ipc.Request) ipc.Response { return resp })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("status server did not exit after cancel")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := ipc.Send(sock, ipc.Request{Action: "status"}, 200*time.Millisecond); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("status server never became reachable")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// captureRunStatus returns what runStatus writes to os.Stdout along with its
// error, by pointing os.Stdout at a temp file for the call.
func captureRunStatus(t *testing.T, jsonOut bool) (string, error) {
	t.Helper()
	capture, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	old := os.Stdout
	os.Stdout = capture
	defer func() { os.Stdout = old }()
	runErr := runStatus(jsonOut)
	if _, err := capture.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(capture)
	if err != nil {
		t.Fatal(err)
	}
	return string(body), runErr
}

// `mavor status --json` is runStatus, and it emits the whole response rather
// than just the state word. Driving the real entry point against a real socket
// is what makes the flag's implementation, not a hand-rolled round trip, the
// thing under test.
func TestStatusJSONSerializesWholeResponse(t *testing.T) {
	startStatusServer(t, ipc.Response{State: "transcribing", Transcribing: 1, Source: "main"})
	body, err := captureRunStatus(t, true)
	if err != nil {
		t.Fatal(err)
	}
	var back ipc.Response
	if err := json.Unmarshal([]byte(body), &back); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if back.State != "transcribing" || back.Transcribing != 1 || back.Source != "main" {
		t.Fatalf("decoded status = %+v", back)
	}
}

// A response error propagates out of --json rather than being encoded as a
// successful status.
func TestStatusJSONReturnsResponseError(t *testing.T) {
	startStatusServer(t, ipc.Response{Error: "daemon exploded"})
	_, err := captureRunStatus(t, true)
	if err == nil || !strings.Contains(err.Error(), "daemon exploded") {
		t.Fatalf("runStatus error = %v, want the response error", err)
	}
}
