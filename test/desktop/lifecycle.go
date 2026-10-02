package desktop

// This controlled desktop fixture drives the real daemon lifecycle and IPC,
// not Show/SetText. Models come only from the caller's existing mounted cache.
import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/daemon"
	"github.com/mschulkind-oss/mavor/internal/ipc"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

type retainedHUD struct{ overlay.Overlay }

func (h retainedHUD) Close() error { return nil }
func (h retainedHUD) SyncFrame(ctx context.Context) (overlay.FrameReceipt, error) {
	return h.Overlay.(overlay.FrameObserver).SyncFrame(ctx)
}

type Lifecycle struct {
	socket, dir string
	release     chan struct{}
	cancel      context.CancelFunc
	done        chan error
	out         *output.Mock
	fail        bool
}

func StartLifecycle(o overlay.Overlay, fail bool) (*Lifecycle, error) {
	root := os.Getenv("MAVOR_READINESS_MODEL_DIR")
	if root == "" {
		return nil, errors.New("mounted model path required")
	}
	dir, e := os.MkdirTemp("", "mavor-desktop-lifecycle-")
	if e != nil {
		return nil, e
	}
	l := &Lifecycle{socket: filepath.Join(dir, "mavor.sock"), dir: dir, release: make(chan struct{}), done: make(chan error, 1), out: &output.Mock{}, fail: fail}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := daemon.New(daemon.Config{Socket: l.socket, Overlay: retainedHUD{o}, Recorder: &audio.MockRecorder{}, Output: l.out, Logger: logger, ErrorDuration: 5 * time.Second, Initialize: func(ctx context.Context) (daemon.Initialized, error) {
		select {
		case <-l.release:
		case <-ctx.Done():
			return daemon.Initialized{}, ctx.Err()
		}
		cfg := config.Default()
		cfg.Model = "zipformer-streaming-20m"
		cfg.Paths.Models = root
		cfg.Advanced.GPU = "off"
		cfg.Advanced.Threads = 2
		if fail {
			cfg.Paths.Models = filepath.Join(dir, "deliberately-missing")
		}
		res, e := speech.Resolve(cfg)
		if e != nil {
			return daemon.Initialized{}, e
		}
		tr, e := speech.FactoryFor(cfg, res, logger)
		if e != nil {
			return daemon.Initialized{}, e
		}
		if e = speech.VerifyReadiness(ctx, tr); e != nil {
			if c, ok := tr.(io.Closer); ok {
				_ = c.Close()
			}
			return daemon.Initialized{}, e
		}
		return daemon.Initialized{Transcriber: tr, MainModel: cfg.Model}, nil
	}})
	go func() { l.done <- d.Run(ctx) }()
	for _, action := range []string{"status", "stop"} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			r, e := ipc.Send(l.socket, ipc.Request{Action: action}, time.Second)
			if e == nil {
				if r.State != "initializing" {
					l.Close()
					return nil, fmt.Errorf("early %s: %+v", action, r)
				}
				break
			}
			if time.Now().After(deadline) {
				l.Close()
				return nil, e
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return l, nil
}

// RequestNotice drives the real blocked recording controls after quiet capture.
func (l *Lifecycle) RequestNotice() error {
	for _, action := range []string{"start", "toggle", "stop", "status"} {
		r, err := ipc.Send(l.socket, ipc.Request{Action: action}, time.Second)
		if err != nil {
			return err
		}
		if r.State != "initializing" {
			return fmt.Errorf("blocked %s: %+v", action, r)
		}
	}
	return nil
}
func (l *Lifecycle) Release() { close(l.release) }
func (l *Lifecycle) Await() (ipc.Response, error) {
	want := "idle"
	if l.fail {
		want = "failed"
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		r, e := ipc.Send(l.socket, ipc.Request{Action: "status"}, time.Second)
		if e != nil {
			return r, e
		}
		if r.State == want {
			if len(l.out.Calls()) != 0 {
				return r, errors.New("initialization emitted text")
			}
			return r, nil
		}
		if time.Now().After(deadline) {
			return r, errors.New("lifecycle deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func (l *Lifecycle) Close() error {
	l.cancel()
	e := <-l.done
	defer os.RemoveAll(l.dir)
	if _, err := os.Stat(l.socket); !os.IsNotExist(err) {
		return errors.New("daemon socket survived close")
	}
	if l.fail && (e == nil || errors.Is(e, context.Canceled)) {
		return fmt.Errorf("original initialization diagnostic lost: %v", e)
	}
	if !l.fail && e != nil && !errors.Is(e, context.Canceled) {
		return e
	}
	return nil
}
