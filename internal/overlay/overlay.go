// Package overlay shows a small status pill at the top of the screen on a
// wlr-layer-shell surface, drawn pixel by pixel in Go.
//
// The Overlay interface is the seam: the daemon can be driven by Noop or Mock
// in unit tests, the painter in paint.go turns state into an image with no
// compositor involved, and overlay_wl.go is the only part that speaks Wayland.
//
// Architecture and invariants: docs/reference/how-mavor-works.md
package overlay

import "math"

// Visual is the small set of visual states the overlay can be in.
type Visual int

const (
	// Hidden means the overlay window is not on screen.
	Hidden Visual = iota
	// Recording is the "● Recording" red-dot state.
	Recording
	// Transcribing is the spinner + "Transcribing…" state.
	Transcribing
	// Error is the "⚠ ERROR" red warning pill.
	Error
)

func (v Visual) String() string {
	switch v {
	case Hidden:
		return "hidden"
	case Recording:
		return "recording"
	case Transcribing:
		return "transcribing"
	case Error:
		return "error"
	}
	return "unknown"
}

type Overlay interface {
	// Show transitions to the given visual state. Hidden hides the window.
	Show(v Visual) error
	// SetLevel updates the live audio energy level [0.0, 1.0] for active metering.
	SetLevel(level float64) error
	// SetText updates the live partial token transcription preview subtitle text.
	SetText(text string) error
	// Close releases resources. Idempotent.
	Close() error
}

// shiftWave scrolls the time-history ring one slot left and appends the
// newest sample at the right edge (index len-1). Index 0 is always the
// oldest retained sample. The caller holds the ring's owning mutex; the
// slice must already have length == cap (a fixed-size ring).
func shiftWave(wave []float64, level float64) {
	copy(wave[:len(wave)-1], wave[1:])
	wave[len(wave)-1] = level
}

// resetWave clears the time-history ring so a new recording opens on a flat
// baseline instead of replaying the tail of the previous utterance.
func resetWave(wave []float64) {
	for i := range wave {
		wave[i] = 0
	}
}

// overlayUsesSharedApplication records that all overlays in a process share
// one GTK application. GTK cannot be torn down and re-initialized: a second
// gtk.Application in the same binary terminates the process a few seconds
// later. NewGTK therefore starts the application once and builds each
// overlay's window on it, and Close destroys only the window.
const overlayUsesSharedApplication = true

// Recording-pill geometry. These live here rather than beside the GTK widget
// code so they are reachable from tests in the default build, and because the
// bar's padding is duplicated into the stylesheet — keeping both derived from
// one constant is what stops them drifting apart.
//
// The pill's height is waveHeight + 2*barPaddingY: the canvas is the tallest
// thing in the row, so it, not the label, sizes the pill.
const (
	// waveHeight is the canvas height, and so the driver of the pill height.
	// It is deliberately well above barTextLineHeight: sized to the label
	// instead, the trace is capped at the text's line box and reads as a
	// small scribble in a large red pill however loudly you speak.
	waveHeight = 42

	// waveBarInset keeps a full-scale bar's ends off the canvas edge. Total,
	// not per-end.
	waveBarInset = 2

	// waveMinBarHeight is the flat line drawn for silence. Silence has to
	// render as a line rather than as nothing, or the pill reads as broken.
	waveMinBarHeight = 2

	// barPaddingY is the pill's vertical padding, interpolated into the
	// stylesheet. barTextLineHeight is the line box of the 15px label beside
	// the canvas, which is what the pill height would collapse to without a
	// taller waveform.
	barPaddingY       = 7
	barPaddingX       = 22
	barTextLineHeight = 20
)

// Recording-dot geometry. The dot is drawn rather than typeset: as the text
// glyph "●" it was baseline-aligned beside smaller capitals and sat visibly
// low, because ● is not centred on the cap-height centre and the offset varies
// with whichever font resolves. A drawn circle is centred exactly and looks
// the same on every machine.
const (
	recDotDiameter = 14
	// recDotBoxW is the widget's width: the dot plus room for the pulse.
	recDotBoxW = 18
	// recDotBoxH is taller than it is wide because the dot is lifted off the
	// centre of its allocation (see opticalRise) and the pulse scales that
	// offset along with the radius.
	recDotBoxH = 22
	// dotPulseMaxScale mirrors the largest transform in the mavor-pulse
	// keyframes. Kept here so the box can be checked against it.
	dotPulseMaxScale = 1.15
	// recDotMaxRise caps the optical correction at what recDotBoxH can still
	// render once the pulse has scaled it. Real fonts land near a pixel; a
	// font asking for more gets a slightly low dot rather than a clipped one.
	recDotMaxRise = 2.0
)

// opticalRise is how far a label's ink sits above the centre of the line box
// it is vertically centred in, given the label's ink and logical extents.
//
// "RECORDING" is all capitals, so it has no descenders — but its line box
// reserves descender space anyway, and GTK centres the box, not the ink. The
// letters therefore ride above the middle of the row, and a dot centred on the
// row lands below them. Because the dot is also taller than the cap height,
// that bias shows up asymmetrically: the dot's top sits flush with the cap
// tops while its bottom hangs past the baseline, which is what reads as the
// dot sitting low.
//
// Taking this from the real extents rather than a constant keeps it correct
// whichever font resolves — the same reason the dot is drawn instead of
// typeset.
func opticalRise(inkY, inkH, logicalY, logicalH int) float64 {
	inkCentre := float64(inkY) + float64(inkH)/2
	lineCentre := float64(logicalY) + float64(logicalH)/2
	return lineCentre - inkCentre
}

// dotCircle returns the centre and radius for the recording dot drawn into an
// allocation of w by h, lifted by rise so it lines up with the cap-height
// centre of the label beside it. The circle never exceeds the allocation: an
// implausible rise is clamped rather than allowed to push the dot out of its
// own box, since a clipped dot is a worse failure than a slightly low one.
func dotCircle(w, h int, rise float64) (cx, cy, r float64) {
	cx = float64(w) / 2
	r = float64(recDotDiameter) / 2
	if limit := min(float64(w), float64(h)) / 2; r > limit {
		r = limit
	}
	if rise > recDotMaxRise {
		rise = recDotMaxRise
	} else if rise < -recDotMaxRise {
		rise = -recDotMaxRise
	}
	cy = float64(h)/2 - rise
	if cy < r {
		cy = r
	}
	if ceiling := float64(h) - r; cy > ceiling {
		cy = ceiling
	}
	return cx, cy, r
}

// Waveform column layout: how many samples the trace holds and how wide each
// column is.
const (
	// waveCols is how many samples the trace holds. At the daemon's ~33 Hz
	// level cadence this is about 1.4 seconds of history.
	waveCols     = 46
	waveColWidth = 3 // 2 px bar + 1 px gap
	waveBarWidth = 2
)

// pillHeight is the rendered height of the recording pill: the canvas plus its
// padding, since the canvas is the row's tallest element.
func pillHeight() int { return waveHeight + 2*barPaddingY }

// waveBarHeight converts a display level in [0,1] to the pixel height of one
// waveform column on a canvas of the given height.
func waveBarHeight(displayLevel float64, canvasHeight int) int {
	maxH := float64(canvasHeight - waveBarInset)
	minH := float64(waveMinBarHeight)
	if maxH < minH {
		maxH = minH
	}
	return int(minH + (maxH-minH)*displayLevel)
}

// waveFloorDB is the quietest level the meter resolves. Room tone sits near
// -55 dBFS, so this puts an idle mic on the baseline and reserves the widget's
// height for audio loud enough to actually be speech.
const waveFloorDB = -50.0

// waveDisplayLevel maps a raw audio RMS level [0,1] to the display level the
// waveform canvas uses for bar heights, on a decibel scale.
//
// This mapping is what makes the meter legible. Conversational speech sits
// around RMS 0.03-0.2; plotted linearly that fills a few pixels of the canvas
// and the trace reads as dead while you talk. Spreading waveFloorDB..0 dB
// across 0..1 puts ordinary speech in the upper half of the widget, which is
// what every real audio meter does.
func waveDisplayLevel(lvl float64) float64 {
	if lvl <= 0 {
		return 0
	}
	db := 20 * math.Log10(lvl)
	switch {
	case db <= waveFloorDB:
		return 0
	case db >= 0:
		return 1
	}
	return (db - waveFloorDB) / -waveFloorDB
}
