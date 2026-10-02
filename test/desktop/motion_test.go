package desktop

import (
	"image"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func motionFixture() []Evidence {
	var out []Evidence
	at := time.Date(2026, 10, 2, 1, 0, 0, 0, time.UTC)
	for _, s := range Scenes() {
		if s.Motion == "" {
			continue
		}
		e := Evidence{ID: s.ID, CaptureStarted: at, CaptureEnded: at.Add(100 * time.Millisecond)}
		e.Receipt.Scene.Levels = make([]float64, 46)
		switch s.Motion {
		case "speech", "recovery":
			for i := 23; i < 46; i++ {
				e.Receipt.Scene.Levels[i] = .4
			}
		case "pause":
			for i := 0; i < 23; i++ {
				e.Receipt.Scene.Levels[i] = .4
			}
		}
		out = append(out, e)
		at = at.Add(time.Second)
	}
	return out
}
func TestMotionRequiresHistoryAndOrderedTime(t *testing.T) {
	if err := validateMotion(motionFixture()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "reordered", "static", "untimed", "pause-live-edge", "speech-live-edge"} {
		t.Run(name, func(t *testing.T) {
			es := motionFixture()
			switch name {
			case "missing":
				es = es[:4]
			case "reordered":
				es[1], es[2] = es[2], es[1]
			case "static":
				for i := range es[1].Receipt.Scene.Levels {
					es[1].Receipt.Scene.Levels[i] = .4
				}
			case "untimed":
				es[2].CaptureStarted = time.Time{}
			case "pause-live-edge":
				es[2].Receipt.Scene.Levels[45] = .4
			case "speech-live-edge":
				es[1].Receipt.Scene.Levels[45] = 0
			}
			if err := validateMotion(es); err == nil {
				t.Fatal("invalid motion accepted")
			}
		})
	}
}

// This unit test verifies image conversion/timing only, not compositor evidence.
func TestMotionGIFUsesMeasuredIntervals(t *testing.T) {
	dir := t.TempDir()
	es := motionFixture()
	for i := range es {
		es[i].File = es[i].ID + ".png"
		f, err := os.Create(filepath.Join(dir, es[i].File))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, image.NewRGBA(image.Rect(0, 0, 1920, 180)))
		closeErr := f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	if err := saveMotionGIF(dir, "fixture", es); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "fixture-motion.gif"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != len(es) {
		t.Fatal("missing GIF frames")
	}
	for _, delay := range g.Delay {
		if delay != 100 {
			t.Fatal("unmeasured delay", delay)
		}
	}
}
