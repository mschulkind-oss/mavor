package overlay

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Painting is a pure function from state to pixels: Render takes a Scene and
// fills an RGBA image. Nothing here touches Wayland, which is what lets the
// whole visual layer be tested without a compositor.

// Palette values are the ones the GTK stylesheet used, so the pill looks the
// same after the move off GTK as before it.
var (
	recTop    = color.NRGBA{0xc8, 0x18, 0x18, 0xff}
	recBottom = color.NRGBA{0x8a, 0x00, 0x00, 0xff}
	tscTop    = color.NRGBA{0xd6, 0x89, 0x10, 0xff}
	tscBottom = color.NRGBA{0x7a, 0x48, 0x07, 0xff}
	errTop    = color.NRGBA{0xa8, 0x00, 0x00, 0xff}
	errBottom = color.NRGBA{0x5c, 0x00, 0x00, 0xff}

	inkWhite  = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	dotWarm   = color.NRGBA{0xff, 0xe0, 0xe0, 0xff}
	typingInk = color.NRGBA{0xff, 0xe0, 0xa8, 0xff}
	errInk    = color.NRGBA{0xff, 0xcc, 0xcc, 0xff}

	previewBG   = color.NRGBA{0x0f, 0x17, 0x2a, 0xd9}
	previewInk  = color.NRGBA{0xf3, 0xf4, 0xf6, 0xff}
	previewEdge = color.NRGBA{0xff, 0xff, 0xff, 0x26}
)

const (
	pillRadius    = 20
	previewRadius = 14
	labelSize     = 15
	previewSize   = 13
	labelTracking = 0.08 // em, matching the stylesheet's letter-spacing
	previewPadX   = 18
	previewPadY   = 6
	previewGap    = 6
)

// Scene is everything the renderer needs to draw one frame.
type Scene struct {
	Visual Visual
	// Levels is the waveform history, oldest first, each in [0,1].
	Levels []float64
	// Preview is the partial transcription strip below the pill; empty hides it.
	//
	// It is drawn on ONE line. The preview exists to show that recognition is
	// keeping up, not to be read back, so a long dictation shows its tail
	// rather than growing a surface nobody can fit on screen.
	Preview string
	// MaxPreviewWidth caps the preview strip in pixels, including its
	// padding. Zero means uncapped, which is what a scene built by hand in a
	// test gets. See FitPreviewTail for what happens to text that exceeds it.
	MaxPreviewWidth int
	// Phase drives the pulsing dot and the typing dots, in [0,1).
	Phase float64
}

// face lazily builds the text faces from the embedded Go font. Embedding the
// font rather than resolving a family name is what makes rendering identical
// on every machine: the GTK version asked for "Inter, Cantarell, sans-serif"
// and got whichever the box happened to have, which is how the recording dot
// came to be misaligned in the first place.
type faces struct {
	label   font.Face
	preview font.Face
	// capRise is how far the label's cap-height centre sits above the centre
	// of its line box, the correction that puts the dot level with the text.
	capRise float64
}

var loaded *faces

func textFaces() (*faces, error) {
	if loaded != nil {
		return loaded, nil
	}
	f, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return nil, err
	}
	mk := func(size float64) (font.Face, error) {
		return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	}
	label, err := mk(labelSize)
	if err != nil {
		return nil, err
	}
	preview, err := mk(previewSize)
	if err != nil {
		return nil, err
	}

	// Cap height from a flat-topped capital: round letters overshoot the cap
	// line, so measuring "H" gives the true box where "RECORDING" would not.
	m := label.Metrics()
	capBounds, _, _ := label.GlyphBounds('H')
	capTop := -float64(capBounds.Min.Y) / 64.0
	ascent := float64(m.Ascent) / 64.0
	descent := float64(m.Descent) / 64.0
	// Ink centre measured down from the baseline, versus line-box centre.
	inkCentre := -capTop / 2.0
	lineCentre := (descent - ascent) / 2.0
	loaded = &faces{label: label, preview: preview, capRise: inkCentre - lineCentre}
	return loaded, nil
}

// textWidth measures a string with the tracking the stylesheet applied.
func textWidth(f font.Face, s string, tracking float64) float64 {
	w := 0.0
	var prev rune
	for i, r := range s {
		if i > 0 {
			w += float64(f.Kern(prev, r)) / 64.0
		}
		adv, ok := f.GlyphAdvance(r)
		if !ok {
			continue
		}
		w += float64(adv)/64.0 + tracking
		prev = r
	}
	return w
}

// drawText draws s with its baseline at y and its left edge at x.
func drawText(dst *image.RGBA, f font.Face, s string, x, y float64, c color.Color, tracking float64) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f}
	pen := fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}
	var prev rune
	for i, r := range s {
		if i > 0 {
			pen.X += f.Kern(prev, r)
		}
		d.Dot = pen
		d.DrawString(string(r))
		adv, ok := f.GlyphAdvance(r)
		if !ok {
			continue
		}
		pen.X += adv + fixed.Int26_6(tracking*64)
		prev = r
	}
}

// roundRectPath appends a rounded rectangle to a rasterizer.
func roundRectPath(r *vector.Rasterizer, x, y, w, h, rad float32) {
	if rad > w/2 {
		rad = w / 2
	}
	if rad > h/2 {
		rad = h / 2
	}
	// Quadratic beziers approximate the corner arcs closely enough at these
	// radii that the difference is below one pixel of coverage.
	r.MoveTo(x+rad, y)
	r.LineTo(x+w-rad, y)
	r.QuadTo(x+w, y, x+w, y+rad)
	r.LineTo(x+w, y+h-rad)
	r.QuadTo(x+w, y+h, x+w-rad, y+h)
	r.LineTo(x+rad, y+h)
	r.QuadTo(x, y+h, x, y+h-rad)
	r.LineTo(x, y+rad)
	r.QuadTo(x, y, x+rad, y)
	r.ClosePath()
}

// circlePath appends a circle, built from four quadratic arcs.
func circlePath(r *vector.Rasterizer, cx, cy, rad float32) {
	const k = 0.5523 // circle-to-bezier constant, scaled for quadratics below
	c := rad * k * 1.5
	r.MoveTo(cx, cy-rad)
	r.QuadTo(cx+c, cy-rad, cx+rad, cy)
	r.QuadTo(cx+rad, cy+c, cx, cy+rad)
	r.QuadTo(cx-c, cy+rad, cx-rad, cy)
	r.QuadTo(cx-rad, cy-c, cx, cy-rad)
	r.ClosePath()
}

// fillPath rasterizes a path into dst with a solid colour, compositing over
// what is already there.
func fillPath(dst *image.RGBA, bounds image.Rectangle, c color.Color, build func(*vector.Rasterizer)) {
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	r := vector.NewRasterizer(w, h)
	build(r)
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	r.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(dst, bounds, image.NewUniform(c), image.Point{}, mask, image.Point{}, draw.Over)
}

// fillGradient rasterizes a path and fills it with a vertical gradient, which
// is what gives the pill the same shaded look the CSS produced.
func fillGradient(dst *image.RGBA, bounds image.Rectangle, top, bottom color.NRGBA, build func(*vector.Rasterizer)) {
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	r := vector.NewRasterizer(w, h)
	build(r)
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	r.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})

	grad := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		t := 0.0
		if h > 1 {
			t = float64(y) / float64(h-1)
		}
		row := color.RGBA{
			R: uint8(lerp(float64(top.R), float64(bottom.R), t)),
			G: uint8(lerp(float64(top.G), float64(bottom.G), t)),
			B: uint8(lerp(float64(top.B), float64(bottom.B), t)),
			A: 0xff,
		}
		for x := 0; x < w; x++ {
			grad.SetRGBA(x, y, row)
		}
	}
	draw.DrawMask(dst, bounds, grad, image.Point{}, mask, image.Point{}, draw.Over)
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// SceneSize reports the pixel size the scene needs, so the surface can be
// allocated before anything is drawn.
// previewEllipsis marks a preview that has been trimmed to its tail. A leading
// mark rather than a trailing one, because the tail is the part still arriving
// and the cut is behind it.
const previewEllipsis = "…"

// FitPreviewTail returns the longest suffix of s whose rendered width fits
// maxTextPx, prefixed with an ellipsis when anything was dropped.
//
// The tail, not the head: the useful half of a live preview is the words just
// spoken. Returns s unchanged when maxTextPx is zero or the text already fits,
// so an uncapped scene behaves exactly as it did before this existed.
func FitPreviewTail(f font.Face, s string, maxTextPx float64, tracking float64) string {
	if s == "" || maxTextPx <= 0 {
		return s
	}

	// Walk backwards accumulating one glyph's advance at a time, rather than
	// re-measuring the whole suffix for every candidate cut. The obvious
	// version of this is quadratic in the number of characters kept, and it
	// runs on every frame of the render loop: at ~90 visible characters that
	// was ~8000 glyph lookups per paint, which showed up as 150 ms frames.
	r := []rune(s)
	markPx := textWidth(f, previewEllipsis, tracking)

	total := 0.0
	kept := 0
	for i := len(r) - 1; i >= 0; i-- {
		adv, ok := f.GlyphAdvance(r[i])
		if !ok {
			continue
		}
		w := float64(adv)/64.0 + tracking
		if i+1 < len(r) {
			w += float64(f.Kern(r[i], r[i+1])) / 64.0
		}

		// Everything still fits: no mark is needed, so the whole string
		// is the answer and there is nothing left to decide.
		if total+w <= maxTextPx && i == 0 {
			return s
		}
		// Once a cut is certain, the mark has to fit beside the text.
		if total+w > maxTextPx-markPx {
			break
		}
		total += w
		kept++
	}

	if kept == 0 {
		return previewEllipsis
	}
	return previewEllipsis + string(r[len(r)-kept:])
}

// previewStripWidth is the width of the preview strip in pixels.
//
// Capped scenes get a CONSTANT width rather than one that hugs the text. The
// overlay is centre-anchored, so every width change is a re-centre: a strip
// that grows with the transcript makes the whole overlay walk sideways while
// you speak. Holding it at the cap costs one resize when the preview appears
// and one when it clears, and none in between.
func previewStripWidth(f *faces, s Scene) int {
	if s.MaxPreviewWidth > 0 {
		return s.MaxPreviewWidth
	}
	return int(math.Ceil(textWidth(f.preview, previewText(f, s), 0.03*previewSize))) + 2*previewPadX
}

// previewText is the string actually drawn, and the single place the cap is
// applied — SceneSize and Render must agree on it or the surface will not
// match its contents.
func previewText(f *faces, s Scene) string {
	if s.Preview == "" {
		return ""
	}
	maxText := float64(s.MaxPreviewWidth - 2*previewPadX)
	if s.MaxPreviewWidth == 0 {
		maxText = 0
	}
	return FitPreviewTail(f.preview, s.Preview, maxText, 0.03*previewSize)
}

func SceneSize(s Scene) (int, int, error) {
	f, err := textFaces()
	if err != nil {
		return 0, 0, err
	}
	pillW, pillH := pillSize(f, s)
	w, h := pillW, pillH
	if s.Visual == Recording && s.Preview != "" {
		pw := previewStripWidth(f, s)
		ph := previewSize + 2*previewPadY + 4
		if pw > w {
			w = pw
		}
		h += previewGap + ph
	}
	return w, h, nil
}

// pillSize measures the recording pill for the current state.
func pillSize(f *faces, s Scene) (int, int) {
	h := waveHeight + 2*barPaddingY
	w := 2 * barPaddingX
	switch s.Visual {
	case Recording:
		w += recDotBoxW + 10
		w += int(math.Ceil(textWidth(f.label, recordingLabelText, labelTracking*labelSize)))
		w += 12 + waveCols*waveColWidth
	case Transcribing:
		w += int(math.Ceil(textWidth(f.label, transcribingLabelText, labelTracking*labelSize)))
		w += 12 + 3*typingDotPitch
	case Error:
		w += errIconBox + 10
		w += int(math.Ceil(textWidth(f.label, errorLabelText, labelTracking*labelSize)))
	}
	return w, h
}

// Label strings, kept here so measurement and drawing cannot drift apart.
const (
	recordingLabelText    = "RECORDING"
	transcribingLabelText = "TRANSCRIBING"
	errorLabelText        = "ERROR"
	typingDotPitch        = 12
	typingDotRadius       = 3.0
	errIconBox            = 14
)

// Render draws the scene into a fresh RGBA image of exactly the size
// SceneSize reports.
// Render draws a scene into a newly allocated image. Convenient for tests and
// the storybook; the render loop uses RenderInto so it can reuse its buffer.
func Render(s Scene) (*image.RGBA, error) {
	w, h, err := SceneSize(s)
	if err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := RenderInto(img, s); err != nil {
		return nil, err
	}
	return img, nil
}

// RenderInto draws a scene into an existing image, which must be at least the
// size SceneSize reports. Separated from Render so the render loop allocates
// once rather than once per frame: at ~190 KB an image and 33 frames a second
// that was 6 MB/s of garbage, and the resulting collections showed up as
// occasional 150 ms paints.
//
// The destination is cleared first, since a reused buffer still holds the
// previous frame.
func RenderInto(img *image.RGBA, s Scene) error {
	f, err := textFaces()
	if err != nil {
		return err
	}
	w, h, err := SceneSize(s)
	if err != nil {
		return err
	}
	if b := img.Bounds(); b.Dx() < w || b.Dy() < h {
		return fmt.Errorf("overlay: destination %dx%d is smaller than the scene's %dx%d", b.Dx(), b.Dy(), w, h)
	}
	draw.Draw(img, img.Bounds(), image.Transparent, image.Point{}, draw.Src)
	if s.Visual == Hidden {
		return nil
	}

	pillW, pillH := pillSize(f, s)
	// The pill is centred horizontally; the preview strip below may be wider.
	pillX := (w - pillW) / 2
	pillRect := image.Rect(pillX, 0, pillX+pillW, pillH)

	top, bottom := recTop, recBottom
	switch s.Visual {
	case Transcribing:
		top, bottom = tscTop, tscBottom
	case Error:
		top, bottom = errTop, errBottom
	}
	fillGradient(img, pillRect, top, bottom, func(r *vector.Rasterizer) {
		roundRectPath(r, 0, 0, float32(pillW), float32(pillH), pillRadius)
	})

	// A one-pixel light line inside the top edge, the CSS inset highlight.
	fillPath(img, pillRect, color.NRGBA{0xff, 0xff, 0xff, 0x40}, func(r *vector.Rasterizer) {
		roundRectPath(r, pillRadius/2, 0, float32(pillW)-pillRadius, 1, 0.5)
	})

	// Baseline that centres the caps vertically in the pill.
	m := f.label.Metrics()
	baseline := float64(pillH)/2 + float64(m.Ascent-m.Descent)/128.0

	x := float64(pillX + barPaddingX)
	switch s.Visual {
	case Recording:
		// The dot rides on the cap-height centre, not the line-box centre.
		cy := float64(pillH)/2 - f.capRise
		scale := 0.85 + 0.30*s.Phase // matches the CSS pulse keyframes
		alpha := uint8(0.35*255 + (1.0-0.35)*255*s.Phase)
		dc := dotWarm
		dc.A = alpha
		fillPath(img, img.Bounds(), dc, func(r *vector.Rasterizer) {
			circlePath(r, float32(x+recDotBoxW/2), float32(cy), float32(recDotDiameter/2*scale))
		})
		x += recDotBoxW + 10

		drawText(img, f.label, recordingLabelText, x, baseline, inkWhite, labelTracking*labelSize)
		x += textWidth(f.label, recordingLabelText, labelTracking*labelSize) + 12

		drawWave(img, s.Levels, x, float64(barPaddingY), waveHeight)

	case Transcribing:
		drawText(img, f.label, transcribingLabelText, x, baseline, inkWhite, labelTracking*labelSize)
		x += textWidth(f.label, transcribingLabelText, labelTracking*labelSize) + 12
		cy := float64(pillH) / 2
		for i := 0; i < 3; i++ {
			// Each dot leads the next by a fixed slice of the cycle.
			p := math.Mod(s.Phase+float64(i)*0.16, 1.0)
			a := 0.25 + 0.75*math.Max(0, 1-math.Abs(p-0.3)/0.3)
			c := typingInk
			c.A = uint8(a * 255)
			fillPath(img, img.Bounds(), c, func(r *vector.Rasterizer) {
				circlePath(r, float32(x+float64(i*typingDotPitch)), float32(cy), typingDotRadius)
			})
		}

	case Error:
		cy := float64(pillH) / 2
		drawWarning(img, x, cy, errIconBox, errInk)
		x += errIconBox + 10
		drawText(img, f.label, errorLabelText, x, baseline, inkWhite, labelTracking*labelSize)
	}

	if s.Visual == Recording && s.Preview != "" {
		pw := previewStripWidth(f, s)
		ph := previewSize + 2*previewPadY + 4
		px := (w - pw) / 2
		py := pillH + previewGap
		rect := image.Rect(px, py, px+pw, py+ph)
		fillPath(img, rect, previewBG, func(r *vector.Rasterizer) {
			roundRectPath(r, 0, 0, float32(pw), float32(ph), previewRadius)
		})
		fillPath(img, rect, previewEdge, func(r *vector.Rasterizer) {
			roundRectPath(r, 0, 0, float32(pw), float32(ph), previewRadius)
			roundRectPath(r, 1, 1, float32(pw)-2, float32(ph)-2, previewRadius-1)
		})
		pm := f.preview.Metrics()
		pb := float64(py) + float64(ph)/2 + float64(pm.Ascent-pm.Descent)/128.0
		drawText(img, f.preview, previewText(f, s), float64(px+previewPadX), pb, previewInk, 0.03*previewSize)
	}

	return nil
}

// drawWave paints the time-scrolling level trace, newest column at the right.
func drawWave(img *image.RGBA, levels []float64, x, y float64, height int) {
	for i := 0; i < waveCols; i++ {
		level := 0.0
		// Right-align the history so the newest sample is always at the edge.
		if idx := len(levels) - waveCols + i; idx >= 0 && idx < len(levels) {
			level = levels[idx]
		}
		bh := waveBarHeight(level, height)
		bx := x + float64(i*waveColWidth)
		by := y + float64(height-bh)/2
		// Older columns fade, which is what reads as motion in a still frame.
		alpha := uint8(90 + 165*float64(i)/float64(waveCols-1))
		c := inkWhite
		c.A = alpha
		fillPath(img, img.Bounds(), c, func(r *vector.Rasterizer) {
			roundRectPath(r, float32(bx), float32(by), waveBarWidth, float32(bh), waveBarWidth/2)
		})
	}
}

// drawWarning paints the triangle-and-bang used by the error state, rather
// than typesetting "⚠" — the glyph is missing from many fonts, including the
// embedded one.
func drawWarning(img *image.RGBA, x, cy float64, size int, c color.NRGBA) {
	s := float64(size)
	top := cy - s/2
	fillPath(img, img.Bounds(), c, func(r *vector.Rasterizer) {
		r.MoveTo(float32(x+s/2), float32(top))
		r.LineTo(float32(x+s), float32(top+s))
		r.LineTo(float32(x), float32(top+s))
		r.ClosePath()
	})
	// The bang, punched back out in the pill's own colour would need the
	// gradient; a darker translucent stroke reads the same at this size.
	fillPath(img, img.Bounds(), color.NRGBA{0x5c, 0x00, 0x00, 0xdd}, func(r *vector.Rasterizer) {
		r.MoveTo(float32(x+s/2-0.9), float32(top+s*0.35))
		r.LineTo(float32(x+s/2+0.9), float32(top+s*0.35))
		r.LineTo(float32(x+s/2+0.9), float32(top+s*0.68))
		r.LineTo(float32(x+s/2-0.9), float32(top+s*0.68))
		r.ClosePath()
		circlePath(r, float32(x+s/2), float32(top+s*0.82), 1.0)
	})
}
