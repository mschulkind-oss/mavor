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

	const frames = 30
	start := time.Now()
	for i := 0; i < frames; i++ {
		// Phase changes every frame in the real loop, and the waveform
		// scrolls, so this is not re-rendering an identical scene.
		scene.Phase = float64(i%24) / 24
		copy(scene.Levels, scene.Levels[1:])
		if err := RenderInto(img, scene); err != nil {
			t.Fatal(err)
		}
	}
	per := time.Since(start) / frames

	t.Logf("steady-state frame: %v (budget %v)", per.Round(time.Microsecond), budget)
	// Half the budget, so a slower machine than this one still keeps up.
	if per > budget/2 {
		t.Errorf("a frame takes %v, over half the %v budget — the render loop will drop ticks and the waveform will stutter", per, budget)
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
