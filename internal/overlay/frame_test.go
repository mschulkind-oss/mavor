package overlay

import (
	"context"
	"testing"
	"time"
)

func TestWLFrameObserverContract(t *testing.T) {
	o := &WL{done: make(chan struct{})}
	observer, ok := any(o).(FrameObserver)
	if !ok {
		t.Fatal("WL missing FrameObserver")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := observer.SyncFrame(ctx); err == nil {
		t.Fatal("unsubmitted frame acknowledged")
	}
}

func TestCanceledFrameReceipt(t *testing.T) {
	var f frameStore
	f.publish(FrameReceipt{Revision: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.wait(ctx, 1, make(chan struct{})); err == nil {
		t.Fatal("canceled caller accepted receipt")
	}
}

func TestWLRebuildCarriesReceiptRevision(t *testing.T) {
	old := &wlState{revision: 42, scene: Scene{Visual: Recording, Preview: "partial"}, levels: []float64{.3}}
	fresh := &wlState{levels: make([]float64, 1)}
	carryWLScene(fresh, old)
	if fresh.revision != 42 || fresh.scene.Preview != "partial" || fresh.scene.Levels[0] != .3 {
		t.Fatalf("lost submission state: %+v", fresh)
	}
	old.levels[0] = 0
	if fresh.scene.Levels[0] != .3 {
		t.Fatal("borrowed waveform after rebuild")
	}
}

func TestWLRejectsInvalidVisual(t *testing.T) {
	o := &WL{done: make(chan struct{})}
	if o.Show(Visual(99)) == nil {
		t.Fatal("invalid visual accepted")
	}
}

func TestWLReceiptDoesNotInventAbsolutePlacement(t *testing.T) {
	st := &wlState{revision: 7, scene: Scene{Visual: Recording}}
	r := wlReceipt(st, 640, 101)
	if !r.Screen.Empty() || r.Canvas.X != 640 || r.Canvas.Y != 101 || r.Revision != 7 {
		t.Fatalf("invented global placement: %+v", r)
	}
}
