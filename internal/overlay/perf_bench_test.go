package overlay

import (
	"image"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/wayland"
)

// These benchmarks measure the per-frame cost of the render/commit path the
// wl render loop in overlay_wl.go runs at ~26.7 Hz (pulsePeriod/24) whenever
// the overlay is not Hidden. 1280x91 is the real surface size on a 2560px
// output at the default 0.5 preview fraction (see FixedSurfaceSize) — the
// same 116,480-pixel canvas paint.go's waveform-bar fix was measured against.

// BenchmarkBlit measures the channel-reorder copy from the RGBA scratch image
// into the wl_shm buffer, at the size that copy actually runs at in
// production.
func BenchmarkBlit(b *testing.B) {
	const w, h = 1280, 91
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Fill with non-zero data so the copy isn't operating on an all-zero page.
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	buf := &wayland.Buffer{Width: w, Height: h, Stride: w * 4, Pix: make([]byte, w*h*4)}
	full := image.Rect(0, 0, w, h)

	b.ReportAllocs()
	b.SetBytes(int64(w * h * 4))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		blit(img, buf, full)
	}
}

// BenchmarkBlitSmall is the same copy at the small (no-output-advertised)
// fallback size, 640x91, for comparison.
func BenchmarkBlitSmall(b *testing.B) {
	const w, h = 640, 91
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	buf := &wayland.Buffer{Width: w, Height: h, Stride: w * 4, Pix: make([]byte, w*h*4)}
	full := image.Rect(0, 0, w, h)

	b.ReportAllocs()
	b.SetBytes(int64(w * h * 4))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		blit(img, buf, full)
	}
}

// BenchmarkFrameFull is RenderInto + blit together: the full per-tick cost
// paid by the render goroutine in overlay_wl.go's paint(), minus the Wayland
// protocol requests (attach/damage/commit), which do not allocate or spin.
// RenderInto itself is already covered by BenchmarkFrame in
// render_bench_test.go; this adds blit on top to show its share of the total.
func BenchmarkFrameFull(b *testing.B) {
	const w, h = 1280, 91
	levels := make([]float64, waveCols)
	for i := range levels {
		levels[i] = 0.5
	}
	scene := Scene{
		Visual:          Recording,
		Levels:          levels,
		MaxPreviewWidth: 1280,
		SurfaceW:        w,
		SurfaceH:        h,
		Phase:           0.3,
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	buf := &wayland.Buffer{Width: w, Height: h, Stride: w * 4, Pix: make([]byte, w*h*4)}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scene.Phase = float64(i%100) / 100
		if err := RenderInto(img, scene); err != nil {
			b.Fatal(err)
		}
		blit(img, buf, image.Rect(0, 0, w, h))
	}
}

// BenchmarkFrameSteadyState is what the render loop actually pays per tick:
// RenderInto plus the partial fill of the shm buffer, with the buffer already
// holding the previous frame — which is the case for every frame but the
// first of a dictation.
func BenchmarkFrameSteadyState(b *testing.B) {
	const w, h = 1280, 91
	scene := Scene{
		Visual: Recording, Preview: "the quick brown fox jumps over the lazy dog",
		MaxPreviewWidth: 640, SurfaceW: w, SurfaceH: h,
		Levels: make([]float64, waveCols),
	}
	for i := range scene.Levels {
		scene.Levels[i] = float64(i%10) / 10
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	buf := &wayland.Buffer{Width: w, Height: h, Stride: w * 4, Pix: make([]byte, w*h*4)}

	bounds, err := SceneBounds(scene)
	if err != nil {
		b.Fatal(err)
	}
	if err := RenderInto(img, scene); err != nil { // warm the caches
		b.Fatal(err)
	}
	dirty := fillBuffer(img, buf, bounds, image.Rectangle{})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scene.Phase = float64(i%24) / 24
		if err := RenderInto(img, scene); err != nil {
			b.Fatal(err)
		}
		dirty = fillBuffer(img, buf, bounds, dirty)
	}
}
