package daemon

import (
	"context"
	"errors"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/speech"
)

func TestInitializingServesControlsWithoutQueuing(t *testing.T) {
	release := make(chan struct{})
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Initialize = func(ctx context.Context) (Initialized, error) {
			select {
			case <-release:
				return Initialized{Transcriber: &speech.Mock{Text: "ready"}}, nil
			case <-ctx.Done():
				return Initialized{}, ctx.Err()
			}
		}
	})
	stop := runDaemon(t, d)
	defer stop()
	for _, action := range []string{"status", "start", "stop", "toggle"} {
		if got := sendWithRetry(t, sock, action); got.State != "initializing" {
			t.Fatalf("%s: %+v", action, got)
		}
	}
	close(release)
	waitForState(t, sock, "idle")
}
func TestInitializationFailureReturnsOriginal(t *testing.T) {
	want := errors.New("actual decode failed")
	hud := &diagnosticHUD{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Overlay = hud
		c.Initialize = func(ctx context.Context) (Initialized, error) {
			time.Sleep(50 * time.Millisecond)
			return Initialized{}, want
		}
	})
	done := make(chan error, 1)
	go func() { done <- d.Run(t.Context()) }()
	if got := sendWithRetry(t, sock, "start"); got.State != "initializing" {
		t.Fatalf("%+v", got)
	}
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatal(err)
		}
		hud.mu.Lock()
		diagnostic := hud.diagnostics[overlay.Error]
		hud.mu.Unlock()
		if !strings.Contains(diagnostic, want.Error()) {
			t.Fatalf("lost startup subtitle: %q visuals=%v", diagnostic, hud.visuals)
		}
	case <-time.After(time.Second):
		t.Fatal("failure did not exit")
	}
}

type closingInitModel struct {
	speech.Mock
	closes int
}

func (m *closingInitModel) Close() error { m.closes++; return nil }
func TestInitializationCancellationJoinsAndClosesTransfer(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	model := &closingInitModel{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Initialize = func(context.Context) (Initialized, error) {
			close(entered)
			<-release
			return Initialized{Transcriber: model}, nil
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	<-entered
	if got := sendWithRetry(t, sock, "toggle"); got.State != "initializing" {
		t.Fatal(got)
	}
	cancel()
	select {
	case <-done:
		t.Fatal("returned before loader joined")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel join hung")
	}
	if model.closes != 1 {
		t.Fatalf("closes=%d", model.closes)
	}
}
func TestExplicitInitializationDeadlineIsPreserved(t *testing.T) {
	d, _ := newTestDaemon(t, func(c *Config) {
		c.InitializationTimeout = 20 * time.Millisecond
		c.Initialize = func(ctx context.Context) (Initialized, error) { <-ctx.Done(); return Initialized{}, ctx.Err() }
	})
	began := time.Now()
	err := d.Run(t.Context())
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(began) > time.Second {
		t.Fatalf("%v took %s", err, time.Since(began))
	}
}
