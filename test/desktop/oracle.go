package desktop

import (
	"fmt"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"image"
	"image/color"
	"image/draw"
	"math"
)

// fixture blends reference pixels only in memory. Exported screenshots always
// come from the compositor, never this comparison helper.
func fixture(base image.Image, s overlay.Scene, p image.Point) *image.RGBA {
	out := image.NewRGBA(base.Bounds())
	draw.Draw(out, out.Bounds(), base, base.Bounds().Min, draw.Src)
	ref, _ := overlay.Render(s)
	draw.Draw(out, ref.Bounds().Add(p), ref, image.Point{}, draw.Over)
	return out
}
func rgb(c color.Color) (int, int, int) {
	r, g, b, _ := c.RGBA()
	return int(r >> 8), int(g >> 8), int(b >> 8)
}
func distance(a, b color.Color) int {
	ar, ag, ab := rgb(a)
	br, bg, bb := rgb(b)
	return abs(ar-br) + abs(ag-bg) + abs(ab-bb)
}
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Validate independently locates screenshot-space canvas placement by matching
// the receipt's shared painter pixels, not Screen or a guessed shell margin.
// Animated indicator pixels are tolerated; labels, waveform and preview are not.
func Validate(base, actual image.Image, r overlay.FrameReceipt) (image.Rectangle, error) {
	if actual.Bounds() != image.Rect(0, 0, 1920, 1080) || base.Bounds() != actual.Bounds() {
		return image.Rectangle{}, fmt.Errorf("viewport must be 1920x1080")
	}
	if r.Status != "submitted" || r.Frame == 0 || r.Revision == 0 || (r.Backend != "x11" && r.Backend != "wayland") {
		return image.Rectangle{}, fmt.Errorf("invalid receipt")
	}
	if r.Scene.Visual == overlay.Hidden {
		accents := 0
		for y := 32; y < 180; y++ {
			for x := 600; x < 1320; x++ {
				rr, g, b := rgb(actual.At(x, y))
				if rr > 90 && float64(rr) > float64(g)*1.4 && rr > b*2 {
					accents++
				}
			}
		}
		if accents > 30 {
			return image.Rectangle{}, fmt.Errorf("HUD accent contaminates hidden baseline")
		}

		// Native panel clock changes are outside the HUD capture area.
		for y := 32; y < 180; y++ {
			for x := 0; x < 1920; x++ {
				if distance(base.At(x, y), actual.At(x, y)) > 30 {
					return image.Rectangle{}, fmt.Errorf("hidden HUD residue")
				}
			}
		}
		return image.Rectangle{}, nil
	}
	ref, err := overlay.Render(r.Scene)
	if err != nil {
		return image.Rectangle{}, err
	}
	// The report sessions use native 1x capture. Other production scales remain
	// distinct and are covered by the backend geometry acceptance suite.
	if math.Abs(r.Scale-1) > .001 {
		return image.Rectangle{}, fmt.Errorf("report requires native 1x capture")
	}
	w, h := ref.Bounds().Dx(), ref.Bounds().Dy()
	// Mask ONLY pixels whose production indicator changes with animation phase.
	// RMS columns and subtitle/label pixels never depend on Phase; their oracle
	// stays exact at the original 1.5 threshold. No fabricated capture is exported.
	animated := make([]bool, w*h)
	for _, phase := range []float64{0, .125, .25, .375, .5, .625, .75, .875, 1} {
		scene := r.Scene
		scene.Phase = phase
		sample, e := overlay.Render(scene)
		if e != nil {
			return image.Rectangle{}, e
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if sample.RGBAAt(x, y) != ref.RGBAAt(x, y) {
					animated[y*w+x] = true
				}
			}
		}
	}
	best := 1e9
	pos := image.Point{}
	score := func(p image.Point, step int) float64 {
		sum, n := 0, 0
		for y := 0; y < h; y += step {
			for x := 0; x < w; x += step {
				c := ref.RGBAAt(x, y)
				if c.A < 200 || animated[y*w+x] {
					continue
				}
				br, bg, bb := rgb(base.At(x+p.X, y+p.Y))
				a := int(c.A)
				expect := color.RGBA{uint8(int(c.R) + (br*(255-a)+127)/255), uint8(int(c.G) + (bg*(255-a)+127)/255), uint8(int(c.B) + (bb*(255-a)+127)/255), 255}
				sum += distance(expect, actual.At(x+p.X, y+p.Y))
				n++
			}
		}
		if n == 0 {
			return 1e9
		}
		return float64(sum) / float64(n)
	}
	for y := 20; y < 90; y++ {
		for x := (1920-w)/2 - 30; x <= (1920-w)/2+30; x++ {
			v := score(image.Pt(x, y), 4)
			if v < best {
				best = v
				pos = image.Pt(x, y)
			}
		}
	}
	best = score(pos, 1)
	if best > 1.5 {
		return image.Rectangle{}, fmt.Errorf("shared painter mismatch %.2f at %v", best, pos)
	}
	bounds := image.Rect(pos.X, pos.Y, pos.X+w, pos.Y+h)
	// Search the entire frame for additional changed accent pixels, not just the
	// expected canvas. Native chrome/caret changes are not HUD distinctness.
	outside := 0
	for y := 0; y < 1080; y++ {
		for x := 0; x < 1920; x++ {
			if image.Pt(x, y).In(bounds) {
				continue
			}
			rr, g, b := rgb(actual.At(x, y))
			if rr > 90 && float64(rr) > float64(g)*1.4 && rr > b*2 && distance(base.At(x, y), actual.At(x, y)) > 50 {
				outside++
			}
		}
	}
	if outside > 30 {
		return bounds, fmt.Errorf("unexpected HUD accent outside measured bounds: %d", outside)
	}
	return bounds, nil
}
