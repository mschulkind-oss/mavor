package output

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fakeXclip(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "xclip"), []byte("#!/bin/sh\n"+body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestX11ClipboardOwnerLifecycle(t *testing.T) {
	file := filepath.Join(t.TempDir(), "input")
	t.Setenv("TEST_INPUT", file)
	fakeXclip(t, `[ "$*" = "-selection clipboard -target UTF8_STRING -quiet -loops 0" ] || exit 44
/bin/cat > "$TEST_INPUT"
printf 'Waiting for selection requests, Control-C to quit\n' >&2
exec /bin/sleep 60
`)
	c := NewX11Clipboard()
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Emit(ctx, " --clear $(bad) café\n世界 "); err != nil {
		t.Fatal(err)
	}
	cancel() // Successful ownership is independent of launch cancellation.
	b, err := os.ReadFile(file)
	if err != nil || string(b) != "--clear $(bad) café 世界" {
		t.Fatalf("stdin = %q, %v", b, err)
	}
	first := c.owner
	select {
	case <-first.done:
		t.Fatal("successful owner expired")
	case <-time.After(30 * time.Millisecond):
	}
	if err := c.Emit(context.Background(), "replacement"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("previous owner not reaped")
	}
	last := c.owner
	fakeXclip(t, "exec /bin/sleep 60\n")
	failedCtx, failedCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer failedCancel()
	if err := c.Emit(failedCtx, "failed replacement"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if c.owner != last {
		t.Fatal("failed replacement discarded successful owner")
	}
	select {
	case <-last.done:
		t.Fatal("failed replacement killed previous owner")
	default:
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-last.done:
	default:
		t.Fatal("Close returned before reap")
	}
	if err := c.Emit(context.Background(), "late"); err == nil {
		t.Fatal("closed dispatcher reused")
	}
}

func TestX11ClipboardFailedLaunch(t *testing.T) {
	fakeXclip(t, "exec /bin/sleep 60\n")
	c := NewX11Clipboard()
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := c.Emit(ctx, "text"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if c.owner != nil {
		t.Fatal("failed launch retained owner")
	}
	cancelCtx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if err := c.Emit(cancelCtx, "text"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	fakeXclip(t, "echo invalid-display >&2\nexit 1\n")
	if err := c.Emit(context.Background(), "text"); err == nil {
		t.Fatal("helper failure accepted")
	}
}

func TestX11ClipboardEmptyDoesNotLaunch(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	c := NewX11Clipboard()
	defer c.Close()
	if err := c.Emit(context.Background(), " \n\t"); err != nil {
		t.Fatal(err)
	}
}

func TestX11StartupAcknowledgmentIsBounded(t *testing.T) {
	s := &x11Startup{ready: make(chan struct{})}
	_, _ = s.Write([]byte("Waiting for selection requests, "))
	select {
	case <-s.ready:
		t.Fatal("partial acknowledgment accepted")
	default:
	}
	_, _ = s.Write([]byte("Control-C to quit\n"))
	select {
	case <-s.ready:
	default:
		t.Fatal("fragmented acknowledgment missed")
	}
	_, _ = s.Write(make([]byte, 8192))
	if len(s.diagnostic()) > 4096 {
		t.Fatal("unbounded stderr retention")
	}
}

func TestX11ClipboardSerialWaitRespectsCancellation(t *testing.T) {
	c := NewX11Clipboard()
	c.gate <- struct{}{} // Simulate another in-flight Emit.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Emit(ctx, "queued transcript"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-c.gate
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
