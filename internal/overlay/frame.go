package overlay

import (
	"context"
	"errors"
	"image"
	"sync"
	"time"
)

// FrameReceipt describes a production submission, not compositor presentation.
// Scene and its Levels are owned snapshots. See docs/design/gnome-hud.md.
type FrameReceipt struct {
	SubmittedAt time.Time   `json:"submitted_at"`
	Backend     string      `json:"backend"`
	Revision    uint64      `json:"revision"`
	Frame       uint64      `json:"frame"`
	Scene       Scene       `json:"scene"`
	Canvas      image.Point `json:"canvas"`
	// Screen is X protocol placement on GNOME. Layer-shell does not report
	// global placement, so Wayland receipts carry an empty rectangle.
	Screen image.Rectangle `json:"screen"`
	Scale  float64         `json:"scale"`
	Status string          `json:"status"`
}

// FrameObserver is optional; audio producers never wait for a frame.
type FrameObserver interface {
	SyncFrame(context.Context) (FrameReceipt, error)
}

type frameStore struct {
	mu          sync.Mutex
	receipt     FrameReceipt
	history     []FrameReceipt
	notify      chan struct{}
	unavailable error
}

func (f *frameStore) signal() {
	if f.notify != nil {
		close(f.notify)
	}
	f.notify = make(chan struct{})
}
func (f *frameStore) publish(r FrameReceipt) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.Scene.Levels = append([]float64(nil), r.Scene.Levels...)
	r.Frame = f.receipt.Frame + 1
	r.SubmittedAt = time.Now()
	r.Status = "submitted"
	f.receipt = r
	if len(f.history) == 64 {
		copy(f.history, f.history[1:])
		f.history = f.history[:63]
	}
	f.history = append(f.history, r)
	f.unavailable = nil
	f.signal()
}
func (f *frameStore) lost(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unavailable = err
	f.signal()
}
func (f *frameStore) wait(ctx context.Context, revision uint64, done <-chan struct{}) (FrameReceipt, error) {
	for {
		if err := ctx.Err(); err != nil {
			return FrameReceipt{}, err
		}
		f.mu.Lock()
		if f.unavailable != nil {
			e := f.unavailable
			f.mu.Unlock()
			return FrameReceipt{}, e
		}
		if f.receipt.Frame > 0 && f.receipt.Revision >= revision {
			r := f.receipt
			r.Scene.Levels = append([]float64(nil), r.Scene.Levels...)
			f.mu.Unlock()
			select {
			case <-done:
				return FrameReceipt{}, errors.New("overlay: closed")
			default:
			}
			return r, nil
		}
		if f.notify == nil {
			f.notify = make(chan struct{})
		}
		ch := f.notify
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return FrameReceipt{}, ctx.Err()
		case <-done:
			return FrameReceipt{}, errors.New("overlay: closed")
		case <-ch:
		}
	}
}

// FrameHistory exposes bounded real submissions for independent capture matching.
// These are evidence candidates, never proof that a compositor presented them.
type FrameHistory interface{ RecentFrames() []FrameReceipt }

func (f *frameStore) recent() []FrameReceipt {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]FrameReceipt(nil), f.history...)
	for i := range out {
		out[i].Scene.Levels = append([]float64(nil), out[i].Scene.Levels...)
	}
	return out
}
