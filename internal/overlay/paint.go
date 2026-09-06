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

	// SurfaceW and SurfaceH pin the canvas size. When set, the scene is laid
	// out INSIDE them instead of defining them, and SceneSize returns them
	// unchanged however much or little there is to draw.
	//
	// This is what lets the overlay allocate one Wayland surface and never
	// resize it. Resizing to hug the contents is what made the surface
	// re-centre on every new word, block the render loop on a compositor
	// round-trip, and race a stale configure. Zero leaves the old
	// hug-the-contents behaviour, which is what tests and the storybook want.
	SurfaceW, SurfaceH int
}

// FixedSurfaceSize is the canvas the overlay allocates once and keeps: wide
// enough for the preview cap, and tall enough for the preview strip whether or
// not one is showing.
//
// The height is reserved unconditionally on purpose. A surface that grows when
// the preview arrives is a resize, and the whole point is to have none. The
// reserved region is transparent, so an idle overlay looks exactly as it did.
func FixedSurfaceSize(maxPreviewWidth int) (int, int, error) {
	f, err := textFaces()
	if err != nil {
		return 0, 0, err
	}
	// The widest pill of any state, so the canvas fits whichever is showing.
	w := 0
	for _, v := range []Visual{Recording, Transcribing, Error} {
		pw, _ := pillSize(f, Scene{Visual: v})
		if pw > w {
			w = pw
		}
	}
	if maxPreviewWidth > w {
		w = maxPreviewWidth
	}
	_, pillH := pillSize(f, Scene{Visual: Recording})
	h := pillH + previewGap + previewSize + 2*previewPadY + 4
	return w, h, nil
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
func roundRectPath(r *path, x, y, w, h, rad float32) {
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
func circlePath(r *path, cx, cy, rad float32) {
	const k = 0.5523 // circle-to-bezier constant, scaled for quadratics below
	c := rad * k * 1.5
	r.MoveTo(cx, cy-rad)
	r.QuadTo(cx+c, cy-rad, cx+rad, cy)
	r.QuadTo(cx+rad, cy+c, cx, cy+rad)
	r.QuadTo(cx-c, cy+rad, cx-rad, cy)
	r.QuadTo(cx-rad, cy-c, cx, cy-rad)
	r.ClosePath()
}

// gradientCache holds the pill's vertical ramp. The ramp is a pure function
// of its size and its two colours — nothing in it moves — but it was being
// rebuilt pixel by pixel on every frame, which made it a third of the cost of
// a frame that had nothing else left to do.
type gradientCache struct {
	img         *image.RGBA
	w, h        int
	top, bottom color.NRGBA
}

func (c *gradientCache) ramp(w, h int, top, bottom color.NRGBA) *image.RGBA {
	if c.img != nil && c.w == w && c.h == h && c.top == top && c.bottom == bottom {
		return c.img
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
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
		// One row is built, then copied down: the ramp varies only in y.
		off := img.PixOffset(0, y)
		line := img.Pix[off : off+4*w]
		for x := 0; x < 4*w; x += 4 {
			line[x], line[x+1], line[x+2], line[x+3] = row.R, row.G, row.B, row.A
		}
	}
	c.img, c.w, c.h, c.top, c.bottom = img, w, h, top, bottom
	return img
}

// gradients, like cache below, is process-wide: one overlay is on screen at a
// time, and a second would cost the two of them a rebuild each.
var gradients gradientCache

// path builds an outline in SURFACE coordinates while the rasterizer beneath
// it is only as large as the region being filled.
//
// The distinction is the whole reason this type exists. vector.Rasterizer
// works in its own 0..w,0..h space, so a caller that wants absolute
// coordinates has to make the rasterizer as big as the screen — and every
// waveform bar was doing exactly that, allocating a full-surface rasterizer
// and mask and compositing over all 116,480 pixels to paint a bar four pixels
// wide, fifty-odd times a frame. Subtracting the origin here lets a caller
// keep absolute coordinates AND a rasterizer sized to the shape.
type path struct {
	r   *vector.Rasterizer
	off image.Point
}

func (p *path) MoveTo(x, y float32) {
	p.r.MoveTo(x-float32(p.off.X), y-float32(p.off.Y))
}

func (p *path) LineTo(x, y float32) {
	p.r.LineTo(x-float32(p.off.X), y-float32(p.off.Y))
}

func (p *path) QuadTo(cx, cy, x, y float32) {
	p.r.QuadTo(cx-float32(p.off.X), cy-float32(p.off.Y), x-float32(p.off.X), y-float32(p.off.Y))
}

func (p *path) ClosePath() { p.r.ClosePath() }

// shapeRect is the pixel rectangle a shape at (x, y) sized w by h touches,
// with a pixel of slop on every side for the anti-aliased edge. It is what a
// caller passes to fillPath as the bounds, and getting it too small clips the
// shape while getting it too large only costs time.
func shapeRect(x, y, w, h float64) image.Rectangle {
	return image.Rect(
		int(math.Floor(x))-1, int(math.Floor(y))-1,
		int(math.Ceil(x+w))+1, int(math.Ceil(y+h))+1,
	)
}

// fillPath rasterizes a path into dst with a solid colour, compositing over
// what is already there. bounds is the region the path may touch, in dst's
// coordinates: it clips the result and, more importantly, sizes the work.
func fillPath(dst *image.RGBA, bounds image.Rectangle, c color.Color, build func(*path)) {
	bounds = bounds.Intersect(dst.Bounds())
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	r := vector.NewRasterizer(w, h)
	build(&path{r: r, off: bounds.Min})
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	r.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(dst, bounds, image.NewUniform(c), image.Point{}, mask, image.Point{}, draw.Over)
}

// fillGradient rasterizes a path and fills it with a vertical gradient, which
// is what gives the pill the same shaded look the CSS produced.
func fillGradient(dst *image.RGBA, bounds image.Rectangle, top, bottom color.NRGBA, build func(*path)) {
	bounds = bounds.Intersect(dst.Bounds())
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	r := vector.NewRasterizer(w, h)
	build(&path{r: r, off: bounds.Min})
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	r.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})

	grad := gradients.ramp(w, h, top, bottom)
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
	// A pinned canvas is the answer whatever is on it: the surface must not
	// change size as the contents do.
	if s.SurfaceW > 0 && s.SurfaceH > 0 {
		return s.SurfaceW, s.SurfaceH, nil
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
	fillGradient(img, pillRect, top, bottom, func(r *path) {
		roundRectPath(r, float32(pillX), 0, float32(pillW), float32(pillH), pillRadius)
	})

	// A one-pixel light line inside the top edge, the CSS inset highlight.
	fillPath(img, pillRect, color.NRGBA{0xff, 0xff, 0xff, 0x40}, func(r *path) {
		roundRectPath(r, float32(pillX)+pillRadius/2, 0, float32(pillW)-pillRadius, 1, 0.5)
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
		rad := recDotDiameter / 2 * scale
		fillPath(img, shapeRect(x+recDotBoxW/2-rad, cy-rad, 2*rad, 2*rad), dc, func(r *path) {
			circlePath(r, float32(x+recDotBoxW/2), float32(cy), float32(rad))
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
			dx := x + float64(i*typingDotPitch)
			fillPath(img, shapeRect(dx-typingDotRadius, cy-typingDotRadius, 2*typingDotRadius, 2*typingDotRadius), c, func(r *path) {
				circlePath(r, float32(dx), float32(cy), typingDotRadius)
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
		strip := cache.strip(f, s, pw, ph)
		draw.Draw(img, image.Rect(px, py, px+pw, py+ph), strip, image.Point{}, draw.Over)
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
		fillPath(img, shapeRect(bx, by, waveBarWidth, float64(bh)), c, func(r *path) {
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
	fillPath(img, shapeRect(x, top, s, s), c, func(r *path) {
		r.MoveTo(float32(x+s/2), float32(top))
		r.LineTo(float32(x+s), float32(top+s))
		r.LineTo(float32(x), float32(top+s))
		r.ClosePath()
	})
	// The bang, punched back out in the pill's own colour would need the
	// gradient; a darker translucent stroke reads the same at this size.
	fillPath(img, shapeRect(x+s/2-2, top+s*0.35, 4, s*0.5), color.NRGBA{0x5c, 0x00, 0x00, 0xdd}, func(r *path) {
		r.MoveTo(float32(x+s/2-0.9), float32(top+s*0.35))
		r.LineTo(float32(x+s/2+0.9), float32(top+s*0.35))
		r.LineTo(float32(x+s/2+0.9), float32(top+s*0.68))
		r.LineTo(float32(x+s/2-0.9), float32(top+s*0.68))
		r.ClosePath()
		circlePath(r, float32(x+s/2), float32(top+s*0.82), 1.0)
	})
}

// stripCache keeps the last rendered preview strip.
//
// Drawing it is by far the most expensive thing in a frame: the rounded
// background is rasterised and then every glyph is drawn one at a time. With
// preview text on screen a frame measured 40 ms against a 37.5 ms budget,
// while a frame without it took 18 ms — so the render loop could not keep its
// deadline, the ticker dropped ticks, and the waveform stuttered. That is the
// whole reason the waveform got worse when the preview arrived.
//
// The text changes a few times a second and the frame rate is 27 a second, so
// almost every one of those redraws produced pixels identical to the last.
// Caching them turns the common frame into a blit.
type stripCache struct {
	img *image.RGBA
	key string
}

// strip returns the rendered preview strip, drawing it only when something
// about it has actually changed.
func (c *stripCache) strip(f *faces, s Scene, pw, ph int) *image.RGBA {
	text := previewText(f, s)
	key := fmt.Sprintf("%d\x00%d\x00%s", pw, ph, text)
	if c.img != nil && c.key == key {
		return c.img
	}

	img := image.NewRGBA(image.Rect(0, 0, pw, ph))
	rect := img.Bounds()
	fillPath(img, rect, previewBG, func(r *path) {
		roundRectPath(r, 0, 0, float32(pw), float32(ph), previewRadius)
	})
	fillPath(img, rect, previewEdge, func(r *path) {
		roundRectPath(r, 0, 0, float32(pw), float32(ph), previewRadius)
		roundRectPath(r, 1, 1, float32(pw)-2, float32(ph)-2, previewRadius-1)
	})
	pm := f.preview.Metrics()
	baseline := float64(ph)/2 + float64(pm.Ascent-pm.Descent)/128.0
	drawText(img, f.preview, text, float64(previewPadX), baseline, previewInk, 0.03*previewSize)

	c.img, c.key = img, key
	return img
}

// cache is process-wide, which is right for what it holds: one overlay is on
// screen at a time, and the storybook renders scenes one after another. A
// second overlay would only ever cost the two of them a redraw each.
var cache stripCache
