package overlay

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestNoopRecordsShows(t *testing.T) {
	n := &Noop{}
	if err := n.Show(Recording); err != nil {
		t.Fatal(err)
	}
	if err := n.Show(Transcribing); err != nil {
		t.Fatal(err)
	}
	if err := n.Show(Hidden); err != nil {
		t.Fatal(err)
	}
	want := []Visual{Recording, Transcribing, Hidden}
	if !reflect.DeepEqual(n.Calls(), want) {
		t.Fatalf("Calls = %v, want %v", n.Calls(), want)
	}
	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestVisualStringer(t *testing.T) {
	cases := []struct {
		v    Visual
		want string
	}{
		{Hidden, "hidden"},
		{Recording, "recording"},
		{Transcribing, "transcribing"},
		{Error, "error"},
		{Visual(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.v.String(); got != tc.want {
			t.Errorf("Visual(%d).String() = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestNoopRecordsLevels(t *testing.T) {
	n := &Noop{}
	if n.LastLevel() != 0.0 {
		t.Errorf("initial LastLevel = %v, want 0.0", n.LastLevel())
	}
	if err := n.SetLevel(0.25); err != nil {
		t.Fatal(err)
	}
	if err := n.SetLevel(0.85); err != nil {
		t.Fatal(err)
	}
	if err := n.SetLevel(0.0); err != nil {
		t.Fatal(err)
	}

	wantLevels := []float64{0.25, 0.85, 0.0}
	if !reflect.DeepEqual(n.Levels(), wantLevels) {
		t.Fatalf("Levels() = %v, want %v", n.Levels(), wantLevels)
	}
	if n.LastLevel() != 0.0 {
		t.Fatalf("LastLevel() = %v, want 0.0", n.LastLevel())
	}
}

func TestNoopRecordsTexts(t *testing.T) {
	n := &Noop{}
	if n.LastText() != "" {
		t.Errorf("initial LastText = %q, want \"\"", n.LastText())
	}
	if err := n.SetText("hel"); err != nil {
		t.Fatal(err)
	}
	if err := n.SetText("hello"); err != nil {
		t.Fatal(err)
	}
	if err := n.SetText("hello world"); err != nil {
		t.Fatal(err)
	}
	if err := n.SetText(""); err != nil {
		t.Fatal(err)
	}

	wantTexts := []string{"hel", "hello", "hello world", ""}
	if !reflect.DeepEqual(n.Texts(), wantTexts) {
		t.Fatalf("Texts() = %v, want %v", n.Texts(), wantTexts)
	}
	if n.LastText() != "" {
		t.Fatalf("LastText() = %q, want \"\"", n.LastText())
	}
}

func TestMockRecordsInteractionsAndErrors(t *testing.T) {
	m := &Mock{}
	if err := m.Show(Recording); err != nil {
		t.Fatal(err)
	}
	if err := m.SetLevel(0.42); err != nil {
		t.Fatal(err)
	}
	if err := m.SetText("testing streaming"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	if len(m.Calls()) != 1 || m.Calls()[0] != Recording {
		t.Fatalf("Calls = %v, want [Recording]", m.Calls())
	}
	if m.LastLevel() != 0.42 {
		t.Fatalf("LastLevel = %v, want 0.42", m.LastLevel())
	}
	if len(m.Levels()) != 1 || m.Levels()[0] != 0.42 {
		t.Fatalf("Levels = %v, want [0.42]", m.Levels())
	}
	if m.LastText() != "testing streaming" {
		t.Fatalf("LastText = %q, want \"testing streaming\"", m.LastText())
	}
	if len(m.Texts()) != 1 || m.Texts()[0] != "testing streaming" {
		t.Fatalf("Texts = %v, want [\"testing streaming\"]", m.Texts())
	}

	// Error injection
	showErr := errors.New("show failed")
	levelErr := errors.New("level failed")
	textErr := errors.New("text failed")
	closeErr := errors.New("close failed")

	m.ShowErr = showErr
	m.LevelErr = levelErr
	m.TextErr = textErr
	m.CloseErr = closeErr

	if err := m.Show(Transcribing); !errors.Is(err, showErr) {
		t.Fatalf("Show err = %v, want %v", err, showErr)
	}
	if err := m.SetLevel(0.99); !errors.Is(err, levelErr) {
		t.Fatalf("SetLevel err = %v, want %v", err, levelErr)
	}
	if err := m.SetText("error token"); !errors.Is(err, textErr) {
		t.Fatalf("SetText err = %v, want %v", err, textErr)
	}
	if err := m.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close err = %v, want %v", err, closeErr)
	}

	// Reset
	m.Reset()
	if len(m.Calls()) != 0 || len(m.Levels()) != 0 || len(m.Texts()) != 0 {
		t.Fatalf("Reset did not clear slices: calls=%v levels=%v texts=%v", m.Calls(), m.Levels(), m.Texts())
	}
	if m.LastLevel() != 0.0 || m.LastText() != "" {
		t.Fatalf("Reset did not clear last values: level=%v text=%q", m.LastLevel(), m.LastText())
	}
}

// The meter exists to show that speech is being heard, so these assert the
// requirement — where ordinary speech lands on the widget — rather than
// restating whichever curve the implementation happens to use.
func TestWaveDisplayLevelSpansTheWidget(t *testing.T) {
	if got := waveDisplayLevel(0); got != 0 {
		t.Errorf("waveDisplayLevel(0) = %v, want 0 (silence is the baseline)", got)
	}
	if got := waveDisplayLevel(1); got != 1 {
		t.Errorf("waveDisplayLevel(1) = %v, want 1 (full scale tops out)", got)
	}

	// Conversational speech sits around RMS 0.03-0.2. All of it must deflect
	// well clear of the floor, or the trace reads as dead while you talk.
	for _, lvl := range []float64{0.03, 0.08, 0.2} {
		if got := waveDisplayLevel(lvl); got < 0.3 {
			t.Errorf("waveDisplayLevel(%v) = %.3f, want >= 0.3 so speech is visible", lvl, got)
		}
	}
	// Room tone must stay near the baseline so silence is distinguishable.
	if got := waveDisplayLevel(0.002); got > 0.15 {
		t.Errorf("waveDisplayLevel(0.002) = %.3f, want <= 0.15 so room tone stays flat", got)
	}
	// Monotonic: louder never draws shorter.
	prev := -1.0
	for _, lvl := range []float64{0, 0.01, 0.05, 0.1, 0.3, 0.6, 1.0} {
		got := waveDisplayLevel(lvl)
		if got < prev {
			t.Fatalf("waveDisplayLevel not monotonic at %v: %v < %v", lvl, got, prev)
		}
		prev = got
	}
}

func TestShiftWaveScrollsHistory(t *testing.T) {
	// Regression: the ring must scroll left — dropping the oldest sample and
	// landing the newest at the right edge — not pin index 0 and overwrite the
	// tail (which left only the live column with a real height).
	wave := make([]float64, 5)
	shiftWave(wave, 1) // [0 0 0 0 1]
	shiftWave(wave, 2) // [0 0 0 1 2]
	shiftWave(wave, 3) // [0 0 1 2 3]
	want := []float64{0, 0, 1, 2, 3}
	if !reflect.DeepEqual(wave, want) {
		t.Errorf("after 3 shifts wave = %v, want %v", wave, want)
	}

	// Once the ring is full, the oldest sample must fall off the left edge.
	shiftWave(wave, 4) // [0 1 2 3 4]
	shiftWave(wave, 5) // [1 2 3 4 5]
	want = []float64{1, 2, 3, 4, 5}
	if !reflect.DeepEqual(wave, want) {
		t.Errorf("after 5 shifts wave = %v, want %v", wave, want)
	}
}

// A fresh recording must open on a flat baseline. Without a reset the ring
// still holds the previous utterance, which then scrolls away on screen as if
// the user were still speaking it.
func TestResetWaveClearsTheTrace(t *testing.T) {
	wave := []float64{0.4, 0.7, 0.2, 0.9}
	resetWave(wave)
	for i, v := range wave {
		if v != 0 {
			t.Errorf("wave[%d] = %v after reset, want 0", i, v)
		}
	}
}

// The recording pill's height is the waveform canvas plus the bar's vertical
// padding, because the canvas is the tallest thing in the row. What the user
// perceives as "the waveform" is a full-scale bar, so that bar's share of the
// pill is the thing worth pinning: at 26px canvas / 8px padding / 4px inset it
// was 22px in a 42px pill — barely half, and it read as a small trace floating
// in a lot of red.
func TestFullScaleBarFillsMostOfThePill(t *testing.T) {
	pill := pillHeight()
	full := waveBarHeight(1.0, waveHeight)

	if got := float64(full) / float64(pill); got < 0.65 {
		t.Errorf("a full-scale bar is %dpx in a %dpx pill (%.0f%%); want at least 65%% "+
			"so the waveform reads as theprimary element rather than padding", full, pill, got*100)
	}
}

// Within its own canvas the bar should use nearly all the height available.
// The inset exists only to keep the bar's ends off the canvas edge.
func TestFullScaleBarFillsItsCanvas(t *testing.T) {
	full := waveBarHeight(1.0, waveHeight)
	if got := float64(full) / float64(waveHeight); got < 0.9 {
		t.Errorf("a full-scale bar uses %.0f%% of the canvas; want at least 90%%", got*100)
	}
}

// Silence must still draw something — a flat line reads as "recording, hearing
// nothing", where an empty canvas reads as "broken".
func TestSilenceStillDrawsAVisibleLine(t *testing.T) {
	if h := waveBarHeight(0, waveHeight); h < 1 {
		t.Errorf("waveBarHeight(0) = %d, want at least 1px so silence is still visible", h)
	}
}

// The bar height has to grow with the level over the whole range, or the meter
// is lying somewhere in the middle.
func TestBarHeightIsMonotonicInLevel(t *testing.T) {
	prev := -1
	for i := 0; i <= 20; i++ {
		lvl := float64(i) / 20
		h := waveBarHeight(lvl, waveHeight)
		if h < prev {
			t.Errorf("bar height fell from %d to %d at display level %.2f", prev, h, lvl)
		}
		prev = h
	}
}

// The canvas has to be taller than the text beside it, or the pill is sized by
// the label and the waveform is capped at the text's line box — which is the
// shape this layout started in.
func TestCanvasIsTallerThanTheLabelBesideIt(t *testing.T) {
	if waveHeight <= barTextLineHeight {
		t.Errorf("waveHeight = %d but the label's line box is %d; the label would drive the pill height",
			waveHeight, barTextLineHeight)
	}
}

// GTK cannot be torn down and re-initialized within one process: a second
// gtk.Application in the same binary takes the process down with a SIGTERM a
// few seconds later. That is a real constraint on this type, and it made the
// integration suite fail as a whole while every test passed alone —
// TestWaveformRingScrolls and TestUIStorybookReport each build an overlay.
//
// This test only pins the contract in the default build. The GTK build has
// TestNewGTKIsReusableWithinOneProcess, which actually creates two.
func TestOverlayDocumentsSingleApplicationPerProcess(t *testing.T) {
	// A sentinel so the constraint is discoverable from the non-GTK build too:
	// if the shared-application machinery is ever removed, this name goes with
	// it and the test stops compiling.
	if !overlayUsesSharedApplication {
		t.Fatal("the overlay must share one GTK application per process; " +
			"creating a second one terminates the binary")
	}
}

// The recording dot used to be the text glyph "●" in a 20px label, baseline-
// aligned beside 15px capitals. Measured from a capture, its centre sat 2.5px
// below the text's, and its ink ran below the text baseline — because ● is not
// centred on the cap-height centre, by an amount that varies with whichever
// font resolves. Drawing the dot instead makes it exact and font-independent.
func TestDotIsCentredInItsAllocation(t *testing.T) {
	for _, box := range []int{recDotBoxH, recDotBoxH + 4, 24} {
		cx, cy, r := dotCircle(box, box, 0)
		if want := float64(box) / 2; cx != want || cy != want {
			t.Errorf("dotCircle(%d,%d,0) centre = (%.1f,%.1f), want (%.1f,%.1f)",
				box, box, cx, cy, want, want)
		}
		if r <= 0 {
			t.Errorf("dotCircle(%d) radius = %.1f, want positive", box, r)
		}
	}
}

// Drawing the circle fixed the glyph's own bearing but left the second half of
// the same bug: GTK centres the label's line box, which reserves descender
// space that all-caps text never uses, so the letters ride above the middle of
// the row and a row-centred dot lands below them. A positive rise lifts the
// dot; it must actually move.
func TestDotIsLiftedToTheLabelsInkCentre(t *testing.T) {
	const rise = 1.0
	_, flat, _ := dotCircle(recDotBoxW, recDotBoxH, 0)
	_, lifted, _ := dotCircle(recDotBoxW, recDotBoxH, rise)

	if lifted >= flat {
		t.Errorf("dotCircle rise=%.1f centre = %.2f, want above the un-lifted %.2f", rise, lifted, flat)
	}
	if got := flat - lifted; math.Abs(got-rise) > 1e-9 {
		t.Errorf("dot rose by %.2fpx, want the full %.2fpx — the rise is being clamped in the normal case", got, rise)
	}

	// Past the design limit the correction stops rather than pushing the dot
	// out of a box the pulse still has to scale.
	_, clamped, _ := dotCircle(recDotBoxW, recDotBoxH, 100)
	if want := float64(recDotBoxH)/2 - recDotMaxRise; math.Abs(clamped-want) > 1e-9 {
		t.Errorf("dotCircle rise=100 centre = %.2f, want it clamped to %.2f", clamped, want)
	}
}

// The rise comes from the label's real extents. An all-caps line box reserves
// descender space the ink does not use, so the ink centre sits above the line
// centre and the rise is positive; a string whose ink fills its line box needs
// no correction at all.
func TestOpticalRiseFromExtents(t *testing.T) {
	cases := []struct {
		name                           string
		inkY, inkH, logicalY, logicalH int
		want                           float64
	}{
		// All-caps ink sitting high in its line box: ink spans 3..15, centre
		// 9; the line box spans 0..20, centre 10. The dot must rise 1px.
		{"all-caps rides above the line centre", 3, 12, 0, 20, 1},
		{"ink fills the line box", 0, 20, 0, 20, 0},
		// Ink hanging below the line centre asks for a negative rise, which
		// pushes the dot down instead of up.
		{"ink below the line centre", 8, 12, 0, 20, -4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := opticalRise(tc.inkY, tc.inkH, tc.logicalY, tc.logicalH); got != tc.want {
				t.Errorf("opticalRise(%d,%d,%d,%d) = %.1f, want %.1f",
					tc.inkY, tc.inkH, tc.logicalY, tc.logicalH, got, tc.want)
			}
		})
	}
}

// A non-square or undersized allocation must still produce a circle that fits,
// or the pulse animation clips it. An implausible rise is clamped rather than
// allowed to push the dot out of its own box.
func TestDotFitsItsAllocation(t *testing.T) {
	dims := [][2]int{{recDotBoxW, recDotBoxH}, {10, 20}, {20, 10}, {4, 4}}
	for _, rise := range []float64{0, 1.8, 40, -40} {
		for _, dim := range dims {
			w, h := dim[0], dim[1]
			cx, cy, r := dotCircle(w, h, rise)
			if cx-r < 0 || cy-r < 0 || cx+r > float64(w) || cy+r > float64(h) {
				t.Errorf("dotCircle(%d,%d,%.1f) = centre (%.1f,%.1f) r=%.1f — does not fit",
					w, h, rise, cx, cy, r)
			}
		}
	}
}

// The allocation has to leave room for the pulse animation's scale, or the
// dot is clipped at its largest.
func TestDotBoxLeavesRoomForThePulse(t *testing.T) {
	// The pulse scales about the widget centre, so it magnifies the rise as
	// well as the radius: the box has to clear both.
	reach := (float64(recDotDiameter)/2 + recDotMaxRise) * dotPulseMaxScale
	if grown := 2 * reach; grown > float64(recDotBoxH) {
		t.Errorf("a %dpx dot lifted %.1fpx and scaled to %.2f reaches %.1fpx, larger than its %dpx box — it would clip",
			recDotDiameter, recDotMaxRise, dotPulseMaxScale, grown, recDotBoxH)
	}
	if float64(recDotDiameter)*dotPulseMaxScale > float64(recDotBoxW) {
		t.Errorf("a %dpx dot scaled to %.2f is wider than its %dpx box",
			recDotDiameter, dotPulseMaxScale, recDotBoxW)
	}
}
