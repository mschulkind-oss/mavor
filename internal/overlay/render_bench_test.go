package overlay

import (
	"image"
	"strings"
	"testing"
	"time"
)

// The frame budget is pulsePeriod/24 = 37.5ms. A frame that misses it makes the
// ticker drop ticks, and a dropped tick is a visible stutter in the waveform —
// which is exactly what happened when the preview arrived and started costing
// 40ms a frame.
func TestFrameRendersWellInsideItsBudget(t *testing.T) {
	const budget = 37500 * time.Microsecond

	scene := Scene{
		Visual:          Recording,
		Preview:         strings.Repeat("the quick brown fox ", 12),
		MaxPreviewWidth: 1280,
		SurfaceW:        1280,
		SurfaceH:        91,
		Levels:          make([]float64, waveCols),
	}
	for i := range scene.Levels {
		scene.Levels[i] = float64(i%10) / 10
	}
	img := image.NewRGBA(image.Rect(0, 0, scene.SurfaceW, scene.SurfaceH))

	// Warm the strip cache the way the first frame of a dictation does, then
	// measure the frames that follow — which is almost all of them, since the
	// preview text changes a few times a second against 27 frames a second.
	if err := RenderInto(img, scene); err != nil {
		t.Fatal(err)
	}

	// The MINIMUM across several batches, not the mean of one.
	//
	// A wall-clock assertion in a unit test is a liability: this one failed
	// once already because it happened to run beside the integration suite,
	// and a test that fails when the machine is busy teaches people to ignore
	// it. The minimum is what the machine can do when it is not contended,
	// which is the property actually being asserted — that the work per frame
	// is small, not that this run had a quiet CPU.
	const (
		batches   = 5
		perBatch  = 20
		threshold = budget / 2 // half, so a slower machine still keeps up
	)
	best := time.Hour
	for b := 0; b < batches; b++ {
		start := time.Now()
		for i := 0; i < perBatch; i++ {
			// Phase advances and the waveform scrolls every frame in the real
			// loop, so this is not re-rendering one identical scene.
			scene.Phase = float64(i%24) / 24
			copy(scene.Levels, scene.Levels[1:])
			if err := RenderInto(img, scene); err != nil {
				t.Fatal(err)
			}
		}
		if per := time.Since(start) / perBatch; per < best {
			best = per
		}
	}

	t.Logf("steady-state frame: %v (budget %v, threshold %v)",
		best.Round(time.Microsecond), budget, threshold)
	if best > threshold {
		t.Errorf("a frame takes %v at best, over the %v threshold — the render loop will drop ticks and the waveform will stutter",
			best, threshold)
	}
}

// The cache must not show stale text: a preview that changed and a frame that
// did not redraw it is worse than a slow frame.
func TestStripCacheRedrawsWhenTheTextChanges(t *testing.T) {
	base := Scene{Visual: Recording, MaxPreviewWidth: 600, SurfaceW: 600, SurfaceH: 91}

	first := base
	first.Preview = "aaaa"
	a, err := Render(first)
	if err != nil {
		t.Fatal(err)
	}

	second := base
	second.Preview = "bbbb bbbb bbbb"
	b, err := Render(second)
	if err != nil {
		t.Fatal(err)
	}

	same := true
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("two different preview texts rendered identical pixels — the cache is serving stale text")
	}
}
