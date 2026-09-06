package overlay

import (
	"image"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The frame budget is pulsePeriod/24 = 37.5ms. A frame that misses it makes the
// ticker drop ticks, and a dropped tick is a visible stutter in the waveform —
// which is exactly what happened when the preview arrived and started costing
// 40ms a frame.
func TestFrameRendersWellInsideItsBudget(t *testing.T) {
	if raceEnabled {
		// The race detector instruments every memory access, so this would
		// measure the instrumentation — about twenty times the real cost.
		// TestFrameCostDoesNotScaleWithTheScreen still runs and is the
		// stronger assertion anyway.
		t.Skip("wall-clock frame timing is meaningless under the race detector")
	}

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
		threshold = budget / 4 // a quarter, so a much slower machine still keeps up
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

// The invariant the timing test above can only measure indirectly: a frame
// costs what the pill and the waveform cost, and NOT what the screen costs.
//
// This is the shape of the bug that made frames take 20ms. Every waveform bar
// was filled through a rasterizer and an alpha mask sized to the whole
// surface, and composited across all of it, to paint four pixels by forty.
// Nothing about that is visible in the output — the frames were correct, just
// fifty times more expensive than they needed to be, and the cost grew with
// the user's monitor, so it was worst exactly where it was least testable.
//
// Allocation is the sharpest probe: a full-surface rasterizer and mask are
// O(width), a bar-sized one is O(1). A wall-clock assertion would catch this
// too, but only on a machine fast enough for the threshold to mean anything.
func TestFrameCostDoesNotScaleWithTheScreen(t *testing.T) {
	perFrameBytes := func(width int) uint64 {
		scene := Scene{
			Visual:          Recording,
			Preview:         "the quick brown fox",
			MaxPreviewWidth: 400, // fixed, so the strip is the same work either way
			SurfaceW:        width,
			SurfaceH:        91,
			Levels:          make([]float64, waveCols),
		}
		for i := range scene.Levels {
			scene.Levels[i] = float64(i%10) / 10
		}
		img := image.NewRGBA(image.Rect(0, 0, scene.SurfaceW, scene.SurfaceH))
		if err := RenderInto(img, scene); err != nil { // warm both caches
			t.Fatal(err)
		}

		const frames = 30
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := 0; i < frames; i++ {
			scene.Phase = float64(i%24) / 24
			copy(scene.Levels, scene.Levels[1:])
			if err := RenderInto(img, scene); err != nil {
				t.Fatal(err)
			}
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / frames
	}

	const (
		narrow = 1280
		wide   = 5120 // a 4x wider screen, showing the same pill
	)
	small, large := perFrameBytes(narrow), perFrameBytes(wide)
	t.Logf("per frame: %d B at %dpx, %d B at %dpx", small, narrow, large, wide)

	// Some growth is legitimate — the scene is centred, so a few coordinates
	// differ — but nothing should grow with the area being drawn on.
	if large > small*3/2 {
		t.Errorf("a %dpx-wide screen costs %d B a frame against %d B at %dpx: "+
			"something is sized to the surface rather than to the shape it draws",
			wide, large, small, narrow)
	}
}

func benchScene() (Scene, *image.RGBA) {
	s := Scene{
		Visual: Recording, Preview: strings.Repeat("the quick brown fox ", 12),
		MaxPreviewWidth: 1280, SurfaceW: 1280, SurfaceH: 91,
		Levels: make([]float64, waveCols),
	}
	for i := range s.Levels {
		s.Levels[i] = float64(i%10) / 10
	}
	return s, image.NewRGBA(image.Rect(0, 0, s.SurfaceW, s.SurfaceH))
}

func BenchmarkFrame(b *testing.B) {
	s, img := benchScene()
	_ = RenderInto(img, s) // warm the strip cache
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Phase = float64(i%24) / 24
		if err := RenderInto(img, s); err != nil {
			b.Fatal(err)
		}
	}
}

// The pill alone, with no preview strip and no waveform to speak of.
func BenchmarkPillOnly(b *testing.B) {
	s := Scene{Visual: Transcribing, SurfaceW: 1280, SurfaceH: 91}
	img := image.NewRGBA(image.Rect(0, 0, s.SurfaceW, s.SurfaceH))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Phase = float64(i%24) / 24
		if err := RenderInto(img, s); err != nil {
			b.Fatal(err)
		}
	}
}
