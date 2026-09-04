package overlay

import (
	"image"
	"image/color"
	"testing"
)

// Render is a pure function from state to pixels, so the whole visual layer is
// testable without a compositor — which is the point of having split it out.

func nonTransparent(img *image.RGBA) int {
	n := 0
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] > 0 {
			n++
		}
	}
	return n
}

func TestRenderProducesEachVisualState(t *testing.T) {
	for _, v := range []Visual{Recording, Transcribing, Error} {
		t.Run(v.String(), func(t *testing.T) {
			img, err := Render(Scene{Visual: v, Phase: 0.5})
			if err != nil {
				t.Fatal(err)
			}
			w, h, err := SceneSize(Scene{Visual: v})
			if err != nil {
				t.Fatal(err)
			}
			if got := img.Bounds().Dx(); got != w {
				t.Errorf("rendered width %d, SceneSize said %d — the surface would be the wrong size", got, w)
			}
			if got := img.Bounds().Dy(); got != h {
				t.Errorf("rendered height %d, SceneSize said %d", got, h)
			}
			// A pill covers most of its box; near-empty means nothing drew.
			if ink := nonTransparent(img); ink < w*h/2 {
				t.Errorf("only %d of %d pixels are opaque — the pill did not draw", ink, w*h)
			}
		})
	}
}

// Hidden must produce nothing at all, or the overlay would flash on the way
// down.
func TestRenderHiddenIsFullyTransparent(t *testing.T) {
	img, err := Render(Scene{Visual: Hidden})
	if err != nil {
		t.Fatal(err)
	}
	if ink := nonTransparent(img); ink != 0 {
		t.Errorf("%d opaque pixels in the hidden state, want 0", ink)
	}
}

// The corners must be transparent or the pill renders as a rectangle with a
// rounded shape painted inside it.
func TestPillCornersAreRounded(t *testing.T) {
	img, err := Render(Scene{Visual: Recording})
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	// The pill is centred, so probe relative to the pill, not the image.
	pillW, pillH := 0, 0
	f, err := textFaces()
	if err != nil {
		t.Fatal(err)
	}
	pillW, pillH = pillSize(f, Scene{Visual: Recording})
	x0 := (b.Dx() - pillW) / 2

	for _, p := range []image.Point{
		{x0, 0}, {x0 + pillW - 1, 0},
		{x0, pillH - 1}, {x0 + pillW - 1, pillH - 1},
	} {
		if _, _, _, a := img.At(p.X, p.Y).RGBA(); a > 0x4000 {
			t.Errorf("corner %v has alpha %d — the pill is not rounded there", p, a>>8)
		}
	}
	// The centre must be solid.
	if _, _, _, a := img.At(x0+pillW/2, pillH/2).RGBA(); a < 0xf000 {
		t.Errorf("pill centre alpha %d, want opaque", a>>8)
	}
}

// The recording pill is red and the transcribing pill amber; a renderer that
// silently drew the wrong gradient would still pass a coverage check.
func TestPillGradientMatchesTheState(t *testing.T) {
	cases := []struct {
		v          Visual
		wantHotter string // which channel should dominate
	}{
		{Recording, "red"},
		{Transcribing, "amber"},
	}
	for _, tc := range cases {
		t.Run(tc.v.String(), func(t *testing.T) {
			img, err := Render(Scene{Visual: tc.v})
			if err != nil {
				t.Fatal(err)
			}
			f, _ := textFaces()
			pillW, pillH := pillSize(f, Scene{Visual: tc.v})
			x := (img.Bounds().Dx()-pillW)/2 + 4 // inside the pill, left of any ink
			r, g, b, _ := img.At(x, pillH/2).RGBA()
			r, g, b = r>>8, g>>8, b>>8
			if r <= b {
				t.Errorf("%s pill at (%d,%d) = #%02x%02x%02x, want red-dominant", tc.v, x, pillH/2, r, g, b)
			}
			if tc.wantHotter == "amber" && g < 40 {
				t.Errorf("transcribing pill green channel %d, want a visible amber component", g)
			}
			if tc.wantHotter == "red" && g > 60 {
				t.Errorf("recording pill green channel %d, want it near zero for red", g)
			}
		})
	}
}

// The waveform must respond to level: silence draws a flat line, loud audio
// draws tall bars. Comparing ink counts catches a meter wired to nothing.
func TestWaveformRespondsToLevel(t *testing.T) {
	quiet := make([]float64, waveCols)
	loud := make([]float64, waveCols)
	for i := range loud {
		loud[i] = 1.0
	}

	qi, err := Render(Scene{Visual: Recording, Levels: quiet})
	if err != nil {
		t.Fatal(err)
	}
	li, err := Render(Scene{Visual: Recording, Levels: loud})
	if err != nil {
		t.Fatal(err)
	}

	count := func(img *image.RGBA) int {
		// Count near-white pixels: the trace, not the red pill behind it.
		n := 0
		for y := 0; y < img.Bounds().Dy(); y++ {
			for x := 0; x < img.Bounds().Dx(); x++ {
				r, g, b, a := img.At(x, y).RGBA()
				if a > 0x8000 && r>>8 > 200 && g>>8 > 200 && b>>8 > 200 {
					n++
				}
			}
		}
		return n
	}
	q, l := count(qi), count(li)
	if l <= q {
		t.Errorf("loud audio drew %d bright pixels, silence drew %d — the meter is not reading the level", l, q)
	}
}

// The preview strip only exists while recording, and widens the surface when
// the text is longer than the pill.
func TestPreviewStripWidensTheSurface(t *testing.T) {
	base, _, err := sceneWH(Scene{Visual: Recording})
	if err != nil {
		t.Fatal(err)
	}
	long := Scene{Visual: Recording, Preview: "a partial transcription long enough to exceed the pill's own width"}
	w, h, err := sceneWH(long)
	if err != nil {
		t.Fatal(err)
	}
	if w <= base {
		t.Errorf("preview scene is %dpx wide, want wider than the bare pill's %dpx", w, base)
	}
	_, pillH := func() (int, int) { f, _ := textFaces(); return pillSize(f, long) }()
	if h <= pillH {
		t.Errorf("preview scene is %dpx tall, want taller than the %dpx pill", h, pillH)
	}
	if _, err := Render(long); err != nil {
		t.Fatal(err)
	}
}

func sceneWH(s Scene) (int, int, error) { return SceneSize(s) }

// The pulse animation has to actually change pixels, or the dot is static.
func TestPulsePhaseChangesTheDot(t *testing.T) {
	a, err := Render(Scene{Visual: Recording, Phase: 0})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Render(Scene{Visual: Recording, Phase: 1})
	if err != nil {
		t.Fatal(err)
	}
	diff := 0
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			diff++
		}
	}
	if diff == 0 {
		t.Error("phase 0 and phase 1 render identically — the pulse is not animating")
	}
}

// The whole reason the dot is drawn rather than typeset: it must sit on the
// cap-height centre of the label, not the centre of the line box.
func TestCapRiseIsPositiveForAnAllCapsFont(t *testing.T) {
	f, err := textFaces()
	if err != nil {
		t.Fatal(err)
	}
	if f.capRise <= 0 {
		t.Errorf("capRise = %.2f, want positive — capitals sit above the line-box centre", f.capRise)
	}
	if f.capRise > 4 {
		t.Errorf("capRise = %.2f, implausibly large for a %dpx font", f.capRise, labelSize)
	}
}

var _ = color.RGBA{}
