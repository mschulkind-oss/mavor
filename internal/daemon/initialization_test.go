package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/ipc"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
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
	hud := &closingInitializationHUD{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Overlay = hud
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
	hud.mu.Lock()
	notice := hud.diagnostics[overlay.Initializing]
	hud.mu.Unlock()
	if notice == "" {
		t.Fatal("cancel test never displayed requested notice")
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
	if hud.closes != 1 {
		t.Fatalf("overlay closes=%d", hud.closes)
	}
	hud.mu.Lock()
	defer hud.mu.Unlock()

	for _, v := range hud.visuals {
		if v == overlay.Recording {
			t.Fatal("cancellation started recording")
		}
	}
}

// Initialization has no deadline of its own, and this is the test that says so.
//
// It used to have a 180-second budget, and the supervisor under it a 120-second
// one. Both turned a slow model load into a failed start; systemd's
// Restart=on-failure then restarted the daemon, which re-read the model from the
// very resource that had been too slow — a loop that made its own cause worse.
// What ends initialization now is the initialization returning, or the caller's
// context, and nothing else.
//
// Testing the real 180 seconds is impractical, so this asserts the shape rather
// than the number: a loader that never returns must leave the daemon starting up
// rather than failed, for longer than any deadline it used to have would have
// taken to fire *and* shorter ones would have to.
func TestInitializationHasNoDeadlineOfItsOwn(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Initialize = func(ctx context.Context) (Initialized, error) {
			close(entered)
			select {
			case <-release:
				return Initialized{Transcriber: &speech.Mock{Text: "ready"}}, nil
			case <-ctx.Done():
				return Initialized{}, ctx.Err()
			}
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	<-entered

	// Still initializing, not failed and not exited.
	if got := sendWithRetry(t, sock, "status"); got.State != "initializing" {
		t.Fatalf("state = %q while the loader is still running, want initializing", got.State)
	}
	select {
	case err := <-done:
		t.Fatalf("initialization ended by itself after no time at all: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	// A loader that finishes late is a working daemon, still up.
	close(release)
	select {
	case err := <-done:
		t.Fatalf("daemon exited after a slow but successful initialization: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	// Only a shutdown ends it.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown hung")
	}
}

func TestInitializationNoticeIsDemandOnly(t *testing.T) {
	for _, action := range []string{"start", "toggle"} {
		t.Run(action, func(t *testing.T) {
			release := make(chan struct{})
			hud := &diagnosticHUD{}
			rec := &audio.MockRecorder{LevelVal: .5}
			duck := &audio.MockDucker{}
			out := &output.Mock{}
			store := &history.Store{Path: t.TempDir() + "/history.jsonl"}
			d, sock := newTestDaemon(t, func(c *Config) {
				c.Overlay = hud
				c.Recorder = rec
				c.Ducker = duck
				c.Output = out
				c.History = store
				c.Initialize = func(ctx context.Context) (Initialized, error) {
					select {
					case <-release:
						return Initialized{Transcriber: &speech.Mock{}}, nil
					case <-ctx.Done():
						return Initialized{}, ctx.Err()
					}
				}
			})
			stop := runDaemon(t, d)
			defer stop()
			assertHUD := func(want overlay.Visual, explain bool) {
				t.Helper()
				hud.mu.Lock()
				defer hud.mu.Unlock()
				if hud.visual != want {
					t.Fatalf("visual=%v want=%v", hud.visual, want)
				}
				if explain && (!strings.Contains(hud.text, "cannot record") || !strings.Contains(hud.text, "Press again") || !strings.Contains(hud.text, "initializing")) {
					t.Fatalf("missing explanation: %q", hud.text)
				}
			}
			for _, a := range []string{"status", "stop", "unknown"} {
				got := sendWithRetry(t, sock, a)
				if a != "unknown" && got.State != "initializing" {
					t.Fatal(got)
				}
				assertHUD(overlay.Hidden, false)
			}
			for _, a := range []string{action, action, "stop", "status", "unknown"} {
				sendWithRetry(t, sock, a)
				assertHUD(overlay.Initializing, true)
			}
			close(release)
			waitForState(t, sock, "idle")
			assertHUD(overlay.Hidden, false)
			if rec.Level() > 0 || len(out.Calls()) != 0 {
				t.Fatal("initialization started recording/output")
			}
			if a, b := duck.Calls(); a != 0 || b != 0 {
				t.Fatalf("duck/restore=%d/%d", a, b)
			}
			if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
				t.Fatalf("history touched: %v", err)
			}
			if got := sendWithRetry(t, sock, action); got.State != "recording" || rec.Level() == 0 {
				t.Fatalf("explicit request failed: %+v", got)
			}
		})
	}
}

// A request paused inside Show must finish its subtitle before ready/failure
// can transition. Otherwise that stale subtitle can repaint the new state.
type gatedInitializationHUD struct {
	diagnosticHUD
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *gatedInitializationHUD) ShowStack(pills []overlay.Visual) error {
	if len(pills) > 0 && pills[0] == overlay.Initializing {
		h.once.Do(func() { close(h.entered); <-h.release })
	}
	return h.diagnosticHUD.ShowStack(pills)
}
func TestInitializationRequestCannotOverwriteCompletion(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			load := make(chan struct{})
			returned := make(chan struct{})
			hud := &gatedInitializationHUD{entered: make(chan struct{}), release: make(chan struct{})}
			d, sock := newTestDaemon(t, func(c *Config) {
				c.Overlay = hud
				c.ErrorDuration = time.Second
				c.Initialize = func(ctx context.Context) (Initialized, error) {
					select {
					case <-load:
					case <-ctx.Done():
						return Initialized{}, ctx.Err()
					}
					defer close(returned)
					if fail {
						return Initialized{}, errors.New("genuine load failure")
					}
					return Initialized{Transcriber: &speech.Mock{}}, nil
				}
			})
			stop := runDaemon(t, d)
			defer stop()
			sendWithRetry(t, sock, "status")
			response := make(chan ipc.Response, 1)
			go func() { r, _ := ipc.Send(sock, ipc.Request{Action: "start"}, time.Second); response <- r }()
			<-hud.entered
			close(load)
			<-returned
			close(hud.release)
			if r := <-response; r.State != "initializing" {
				t.Fatal(r)
			}
			want := "idle"
			visual := overlay.Hidden
			if fail {
				want = "failed"
				visual = overlay.Error
			}
			waitForState(t, sock, want)
			// A subsequent blocked control also must not resurrect initialization.
			sendWithRetry(t, sock, "stop")
			hud.mu.Lock()
			defer hud.mu.Unlock()
			if hud.visual != visual || strings.Contains(hud.text, "cannot record") {
				t.Fatalf("stale HUD: %v %q", hud.visual, hud.text)
			}
		})
	}
}

func TestQuietInitializationFailureStillShowsError(t *testing.T) {
	hud := &diagnosticHUD{}
	d, _ := newTestDaemon(t, func(c *Config) {
		c.Overlay = hud
		c.Initialize = func(context.Context) (Initialized, error) { return Initialized{}, errors.New("quiet startup failed") }
	})
	if err := d.Run(t.Context()); err == nil {
		t.Fatal("lost failure")
	}
	hud.mu.Lock()
	defer hud.mu.Unlock()
	if hud.visual != overlay.Error || hud.text != "quiet startup failed" {
		t.Fatalf("%v %q", hud.visual, hud.text)
	}
	for _, v := range hud.visuals {
		if v == overlay.Initializing {
			t.Fatal("quiet failure revealed initializing")
		}
	}
}

type closingInitializationHUD struct {
	diagnosticHUD
	closes int
}

func (h *closingInitializationHUD) Close() error { h.closes++; return nil }
