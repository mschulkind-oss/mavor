package overlay

import (
	"image"
	"strings"
	"testing"
)

// SceneBounds tells the compositor which part of the buffer changed. If it is
// ever smaller than what was actually painted, the difference is stale pixels
// left on screen — a corruption bug that no amount of staring at the render
// code would reveal, because the render code is correct.
//
// So this checks it against the pixels rather than against the geometry it
// was derived from: render the scene, find the bounding box of everything
// that is not fully transparent, and require SceneBounds to contain it.
func TestSceneBoundsContainsEveryPixelDrawn(t *testing.T) {
	scenes := map[string]Scene{
		"recording, no preview":  {Visual: Recording, SurfaceW: 1280, SurfaceH: 91, Levels: ramp()},
		"recording, short text":  {Visual: Recording, Preview: "hello", MaxPreviewWidth: 640, SurfaceW: 1280, SurfaceH: 91, Levels: ramp()},
		"recording, long text":   {Visual: Recording, Preview: strings.Repeat("the quick brown fox ", 12), MaxPreviewWidth: 640, SurfaceW: 1280, SurfaceH: 91, Levels: ramp()},
		"recording, narrow surf": {Visual: Recording, Preview: "hello there", MaxPreviewWidth: 300, SurfaceW: 400, SurfaceH: 91, Levels: ramp()},
		"transcribing":           {Visual: Transcribing, SurfaceW: 1280, SurfaceH: 91},
		"error":                  {Visual: Error, SurfaceW: 1280, SurfaceH: 91},
		"hidden":                 {Visual: Hidden, SurfaceW: 1280, SurfaceH: 91},
	}

	for name, s := range scenes {
		t.Run(name, func(t *testing.T) {
			for _, phase := range []float64{0, 0.3, 0.5, 0.9} {
				s.Phase = phase
				img := image.NewRGBA(image.Rect(0, 0, s.SurfaceW, s.SurfaceH))
				if err := RenderInto(img, s); err != nil {
					t.Fatal(err)
				}
				painted := paintedBounds(img)

				claimed, err := SceneBounds(s)
				if err != nil {
					t.Fatal(err)
				}
				if painted.Empty() {
					continue // nothing drawn; any claim contains it
				}
				if !painted.In(claimed) {
					t.Fatalf("phase %.1f: painted %v is not inside the claimed damage %v — "+
						"the compositor would be told a changed region did not change", phase, painted, claimed)
				}
			}
		})
	}
}

// A damage rectangle that is merely correct is not enough; it has to be
// smaller than the surface, or it saves nothing.
func TestSceneBoundsIsMuchSmallerThanTheSurface(t *testing.T) {
	s := Scene{Visual: Transcribing, SurfaceW: 1280, SurfaceH: 91}
	r, err := SceneBounds(s)
	if err != nil {
		t.Fatal(err)
	}
	surface := s.SurfaceW * s.SurfaceH
	area := r.Dx() * r.Dy()
	t.Logf("transcribing damages %v — %d of %d pixels (%.0f%%)", r, area, surface, 100*float64(area)/float64(surface))
	if area*2 > surface {
		t.Errorf("the damage rect covers %d of %d pixels, which is most of the surface", area, surface)
	}
}

func ramp() []float64 {
	l := make([]float64, waveCols)
	for i := range l {
		l[i] = float64(i%10) / 10
	}
	return l
}

// paintedBounds is the bounding box of every pixel with any alpha at all.
func paintedBounds(img *image.RGBA) image.Rectangle {
	b := img.Bounds()
	out := image.Rectangle{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.RGBAAt(x, y).A != 0 {
				px := image.Rect(x, y, x+1, y+1)
				if out.Empty() {
					out = px
				} else {
					out = out.Union(px)
				}
			}
		}
	}
	return out
}
