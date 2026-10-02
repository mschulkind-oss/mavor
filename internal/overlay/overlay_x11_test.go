package overlay

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestX11LatestState(t *testing.T) {
	o := &X11{wake: make(chan struct{}, 1)}
	if err := o.Show(Recording); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		_ = o.SetLevel(float64(i))
	}
	if len(o.want.levels) != 64 || o.want.levels[0] != 936 {
		t.Fatal("unbounded or wrong sample batch")
	}
	_ = o.SetText("preview")
	_ = o.Show(Transcribing)
	_ = o.SetText("must not survive")
	_ = o.Show(Recording)
	if o.want.preview != "" {
		t.Fatal("stale preview")
	}
	if o.Show(Visual(99)) == nil {
		t.Fatal("invalid visual accepted")
	}
	o.closing = true
	if o.SetText("late") == nil || o.SetLevel(0) == nil || o.Show(Hidden) == nil {
		t.Fatal("mutation after close")
	}
}
func TestFrameReceiptOwnership(t *testing.T) {
	var f frameStore
	done := make(chan struct{})
	levels := []float64{.5}
	f.publish(FrameReceipt{Revision: 2, Scene: Scene{Levels: levels}})
	levels[0] = 0
	r, err := f.wait(context.Background(), 2, done)
	if err != nil || r.Scene.Levels[0] != .5 || r.Status != "submitted" {
		t.Fatalf("%+v %v", r, err)
	}
	r.Scene.Levels[0] = 1
	r, _ = f.wait(context.Background(), 2, done)
	if r.Scene.Levels[0] != .5 {
		t.Fatal("borrowed receipt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err = f.wait(ctx, 3, done); err == nil {
		t.Fatal("stale receipt accepted")
	}
	close(done)
	if _, err = f.wait(context.Background(), 2, done); err == nil {
		t.Fatal("closed receipt accepted")
	}
}

func TestX11ConcurrentProducers(t *testing.T) {
	o := &X11{wake: make(chan struct{}, 1)}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 1000; j++ {
				_ = o.Show(Recording)
				_ = o.SetLevel(.2)
				_ = o.SetText("latest")
			}
		}()
	}
	group.Wait()
	_ = o.Show(Error)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.want.visual != Error || o.want.preview != "" || len(o.want.levels) > 64 {
		t.Fatal("lost state or unbounded sample batch")
	}
}
