package overlay

import (
	"image"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/wayland"
)

// The render loop no longer rewrites the whole shm buffer every frame — it
// writes only what differs from what that buffer already held, which is how a
// frame stops paying 300µs to copy transparent pixels onto transparent
// pixels.
//
// The risk is entirely one-sided: too small a region leaves pixels from an
// older frame on screen, and it would show up as a smear of last sentence's
// preview behind this one, on a real compositor, on someone's machine.
//
// So this drives a realistic sequence of scenes through the same three-buffer
// rotation the loop uses, with the same fillBuffer, and requires every buffer
// to come out byte-identical to a full copy of that scene.
func TestPartialFillsMatchAFullCopyExactly(t *testing.T) {
	const w, h = 1280, 91

	// A sequence that grows, shrinks, changes state and hides — the shrinking
	// steps are the ones that catch a missing clear.
	scenes := []Scene{
		{Visual: Recording},
		{Visual: Recording, Preview: strings.Repeat("the quick brown fox ", 12)},
		{Visual: Recording, Preview: "short"},
		{Visual: Recording, Preview: strings.Repeat("longer again ", 20)},
		{Visual: Hidden},
		{Visual: Recording, Preview: "back"},
		{Visual: Transcribing},
		{Visual: Error},
		{Visual: Recording, Preview: strings.Repeat("wide ", 40)},
		{Visual: Hidden},
		{Visual: Recording},
	}

	var bufs [3]*wayland.Buffer
	var dirty [3]image.Rectangle
	for i := range bufs {
		bufs[i] = &wayland.Buffer{Width: w, Height: h, Stride: w * 4, Pix: make([]byte, w*h*4)}
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	want := &wayland.Buffer{Width: w, Height: h, Stride: w * 4, Pix: make([]byte, w*h*4)}

	for frame, s := range scenes {
		s.SurfaceW, s.SurfaceH = w, h
		s.MaxPreviewWidth = 640
		s.Levels = ramp()
		s.Phase = float64(frame%24) / 24

		if err := RenderInto(img, s); err != nil {
			t.Fatal(err)
		}
		bounds, err := SceneBounds(s)
		if err != nil {
			t.Fatal(err)
		}

		i := frame % len(bufs)
		dirty[i] = fillBuffer(img, bufs[i], bounds, dirty[i])

		// The reference: a full copy of the same scene into a clean buffer.
		clear(want.Pix)
		blit(img, want, img.Bounds())

		if diff := firstDiff(bufs[i].Pix, want.Pix); diff >= 0 {
			px := diff / 4
			t.Fatalf("frame %d (%v, %d chars of preview) into buffer %d: byte %d differs at pixel (%d,%d) — "+
				"got %d, want %d. The partial fill left a pixel from an older frame behind; damage was %v, "+
				"buffer was dirty in %v",
				frame, s.Visual, len(s.Preview), i, diff, px%w, px/w,
				bufs[i].Pix[diff], want.Pix[diff], bounds, dirty[i])
		}
	}
}

// The saving is the point: a partial fill must actually touch less than the
// whole buffer, or the dirty tracking is pure overhead.
func TestAPartialFillTouchesFarLessThanTheWholeBuffer(t *testing.T) {
	const w, h = 1280, 91
	s := Scene{Visual: Transcribing, SurfaceW: w, SurfaceH: h}
	bounds, err := SceneBounds(s)
	if err != nil {
		t.Fatal(err)
	}
	// Steady state: the same scene twice, so the union is the scene itself.
	touched := bounds.Union(bounds)
	whole := w * h
	got := touched.Dx() * touched.Dy()
	t.Logf("a steady transcribing frame copies %d of %d pixels (%.0f%%)", got, whole, 100*float64(got)/float64(whole))
	if got*3 > whole {
		t.Errorf("a partial fill copies %d of %d pixels, which is not much of a saving", got, whole)
	}
}

func firstDiff(a, b []byte) int {
	if len(a) != len(b) {
		return 0
	}
	for i := range a {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}
