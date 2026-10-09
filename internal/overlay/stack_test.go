package overlay

import (
	"errors"
	"image"
	"reflect"
	"strings"
	"testing"
)

// The overlay allocates one surface at startup and never resizes it . A stacked
// row therefore has to be paid for in FixedSurfaceSize up front: the reservation
// is what makes the second and third pills possible at all.

// The fixed canvas reserves exactly maxStackRows rows — one recording plus two
// visible transcribing — plus the preview and notice strips. This is the test
// that fails if a row is added without widening the reservation: the formula is
// pinned to maxStackRows, and all three rows are required to paint inside the
// canvas the overlay actually allocates.
func TestFixedCanvasReservesThreeStackedRows(t *testing.T) {
	if maxStackRows != 3 {
		t.Fatalf("maxStackRows = %d, want 3 (one recording plus two visible transcribing)", maxStackRows)
	}
	f, err := textFaces()
	if err != nil {
		t.Fatal(err)
	}
	const cap = 640
	w, h, err := FixedSurfaceSize(cap)
	if err != nil {
		t.Fatal(err)
	}
	_, pillH := pillSize(f, Scene{Visual: Recording})
	wantH := rowsHeight(pillH, maxStackRows) + 2*(previewGap+stripHeight())
	if h != wantH {
		t.Fatalf("fixed canvas height = %d, want %d (%d rows plus both strips)", h, wantH, maxStackRows)
	}

	// An unpinned three-row scene must fit inside the allocated canvas, or the
	// second and third pills would be clipped.
	full := Scene{
		Visual:          Recording,
		Stack:           []Visual{Transcribing, Transcribing},
		Preview:         strings.Repeat("the quick brown fox ", 8),
		Notice:          testNotice,
		MaxPreviewWidth: cap,
	}
	ow, oh, err := SceneSize(full)
	if err != nil {
		t.Fatal(err)
	}
	if ow > w || oh > h {
		t.Fatalf("a three-row scene wants %dx%d, but the fixed canvas is %dx%d", ow, oh, w, h)
	}

	// Pinned to that canvas, every reserved row must actually paint.
	pinned := full
	pinned.SurfaceW, pinned.SurfaceH = w, h
	pinned.Levels = ramp()
	img, err := Render(pinned)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxStackRows; i++ {
		band := image.Rect(0, rowTop(pillH, i), w, rowTop(pillH, i)+pillH)
		if !rowHasInk(img, band) {
			t.Fatalf("row %d (%v) is empty — the fixed canvas did not reserve it", i, band)
		}
	}
}

// A stack longer than the reservation is collapsed into a count on the last
// visible row rather than clipped or allowed to grow the surface.
func TestOverlongStackCollapsesIntoACount(t *testing.T) {
	const cap = 640
	w, h, err := FixedSurfaceSize(cap)
	if err != nil {
		t.Fatal(err)
	}
	// Recording plus four transcribing: two more rows than the canvas shows.
	s := Scene{
		Visual:          Recording,
		Stack:           []Visual{Transcribing, Transcribing, Transcribing, Transcribing},
		MaxPreviewWidth: cap,
		SurfaceW:        w, SurfaceH: h,
		Levels: ramp(),
	}
	img, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	if painted := paintedBounds(img); !painted.In(img.Bounds()) {
		t.Fatalf("a collapsed stack escaped the surface: painted %v surface %v", painted, img.Bounds())
	}

	rows := rowsFor(s)
	if len(rows) != maxStackRows {
		t.Fatalf("drew %d rows, want the cap of %d", len(rows), maxStackRows)
	}
	if got := rows[len(rows)-1].hidden; got != 2 {
		t.Fatalf("collapsed count = %d, want 2 (four stack rows less the cap after the primary)", got)
	}
	if !strings.Contains(rows[len(rows)-1].label(), "2") {
		t.Fatalf("collapsed row label %q does not name the count", rows[len(rows)-1].label())
	}

	// The count widens the row, so the canvas must still hold it.
	f, _ := textFaces()
	plainW, _ := pillSizeFor(f, Transcribing, labelFor(Transcribing))
	last := rows[len(rows)-1]
	collapsedW, _ := pillSizeFor(f, last.v, last.label())
	if collapsedW <= plainW {
		t.Fatalf("collapsed row is %dpx, no wider than a plain transcribing pill at %dpx", collapsedW, plainW)
	}
	if collapsedW > w {
		t.Fatalf("collapsed row is %dpx, wider than the %dpx canvas", collapsedW, w)
	}
}

// §7.6: a failed cycle's error pill is drawn alongside the other rows, not
// instead of them.
func TestErrorPillRendersAlongsideTheStack(t *testing.T) {
	f, err := textFaces()
	if err != nil {
		t.Fatal(err)
	}
	_, pillH := pillSize(f, Scene{Visual: Recording})
	const cap = 640
	w, h, err := FixedSurfaceSize(cap)
	if err != nil {
		t.Fatal(err)
	}
	s := Scene{
		Visual:          Recording,
		Stack:           []Visual{Error, Transcribing},
		MaxPreviewWidth: cap,
		SurfaceW:        w, SurfaceH: h,
		Levels: ramp(),
	}
	img, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}

	// The recording row above the error is still there.
	if !rowHasInk(img, image.Rect(0, 0, w, pillH)) {
		t.Fatal("the recording row vanished when a stacked error was present")
	}

	rows := rowsFor(s)
	if rows[1].v != Error {
		t.Fatalf("row 1 is %v, want the error pill", rows[1].v)
	}
	rowW, _ := pillSizeFor(f, rows[1].v, rows[1].label())
	rowX := (w - rowW) / 2
	y := rowTop(pillH, 1)
	// Left of any ink: the crimson error gradient, not the red recording one.
	r, g, b, _ := img.At(rowX+4, y+pillH/2).RGBA()
	r, g, b = r>>8, g>>8, b>>8
	if r <= 0x60 || g > 0x20 || b > 0x20 {
		t.Fatalf("stacked error row = #%02x%02x%02x, want a crimson fill", r, g, b)
	}
}

// The primary row keeps the waveform and subtitle; a stacked row carries
// neither. That separation is what makes a stacked scene read as two dictations
// rather than one doubled pill.
func TestOnlyThePrimaryRowOwnsTheWaveformAndPreview(t *testing.T) {
	const cap = 640
	w, h, err := FixedSurfaceSize(cap)
	if err != nil {
		t.Fatal(err)
	}
	quiet := make([]float64, waveCols)
	loud := make([]float64, waveCols)
	for i := range loud {
		loud[i] = 1.0
	}
	render := func(levels []float64) *image.RGBA {
		img, err := Render(Scene{Visual: Recording, Stack: []Visual{Transcribing}, Preview: "preview", MaxPreviewWidth: cap, SurfaceW: w, SurfaceH: h, Levels: levels})
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	f, _ := textFaces()
	_, pillH := pillSize(f, Scene{Visual: Recording})
	qi, li := render(quiet), render(loud)
	// The stacked row must be byte-identical whether the waveform is quiet or
	// loud: a stacked row that responded to the level is drawing the primary's
	// data somewhere it does not belong.
	for y := rowTop(pillH, 1); y < rowTop(pillH, 1)+pillH; y++ {
		for x := 0; x < w; x++ {
			if qi.RGBAAt(x, y) != li.RGBAAt(x, y) {
				t.Fatalf("the stacked row changed with the recording row's level at (%d,%d) — it is drawing the waveform twice", x, y)
			}
		}
	}
}

// The primary row is drawn first and remains the one the preview/notice strips
// hang from, even with rows below it.
func TestPreviewStripSitsBelowTheWholeStack(t *testing.T) {
	f, err := textFaces()
	if err != nil {
		t.Fatal(err)
	}
	_, pillH := pillSize(f, Scene{Visual: Recording})
	const cap = 640
	w, h, err := FixedSurfaceSize(cap)
	if err != nil {
		t.Fatal(err)
	}
	s := Scene{Visual: Recording, Stack: []Visual{Transcribing}, Preview: "hello there", MaxPreviewWidth: cap, SurfaceW: w, SurfaceH: h, Levels: ramp()}
	rows := rowsFor(s)
	previewRow := image.Rect(0, stripY(pillH, len(rows), 1), w, stripY(pillH, len(rows), 1)+stripHeight())
	img, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	if !rowHasInk(img, previewRow) {
		t.Fatalf("the preview strip %v is empty below a stack of %d rows", previewRow, len(rows))
	}
	// And the strip starts below the last pill, not over it.
	if previewRow.Min.Y < rowTop(pillH, len(rows)-1)+pillH {
		t.Fatalf("the preview strip starts at %d, overlapping the rows", previewRow.Min.Y)
	}
}

func TestNoopRecordsStacks(t *testing.T) {
	n := &Noop{}
	if err := n.ShowStack([]Visual{Recording, Transcribing, Transcribing}); err != nil {
		t.Fatal(err)
	}
	if err := n.Show(Hidden); err != nil {
		t.Fatal(err)
	}
	want := [][]Visual{{Recording, Transcribing, Transcribing}, {Hidden}}
	if got := n.Stacks(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Stacks = %v, want %v", got, want)
	}
	if got := n.Calls(); !reflect.DeepEqual(got, []Visual{Recording, Hidden}) {
		t.Fatalf("Calls = %v, want each call's primary [Recording Hidden]", got)
	}
}

func TestMockRecordsStacksAndErrors(t *testing.T) {
	m := &Mock{}
	if err := m.ShowStack([]Visual{Recording, Transcribing}); err != nil {
		t.Fatal(err)
	}
	if got := m.Stacks(); len(got) != 1 || !reflect.DeepEqual(got[0], []Visual{Recording, Transcribing}) {
		t.Fatalf("Stacks = %v, want one [Recording Transcribing]", got)
	}
	if got := m.Calls(); !reflect.DeepEqual(got, []Visual{Recording}) {
		t.Fatalf("Calls = %v, want the primary [Recording]", got)
	}
	showErr := errors.New("show failed")
	m.ShowErr = showErr
	if err := m.ShowStack([]Visual{Recording}); !errors.Is(err, showErr) {
		t.Fatalf("ShowStack err = %v, want %v", err, showErr)
	}
	m.ShowErr = nil
	m.Reset()
	if len(m.Stacks()) != 0 || len(m.Calls()) != 0 {
		t.Fatalf("Reset kept calls=%v stacks=%v", m.Calls(), m.Stacks())
	}
}

// Both backends carry the stack as the latest-wanted state, and a plain Show
// clears it: the previous dictation's rows must not linger.
func TestOverlayBackendsCarryTheStack(t *testing.T) {
	t.Run("wl", func(t *testing.T) {
		o := &WL{wake: make(chan struct{}, 1)}
		if err := o.ShowStack([]Visual{Recording, Transcribing}); err != nil {
			t.Fatal(err)
		}
		if o.want.visual != Recording || !reflect.DeepEqual(o.want.stack, []Visual{Transcribing}) {
			t.Fatalf("want = {%v %v}, want {Recording [Transcribing]}", o.want.visual, o.want.stack)
		}
		if err := o.Show(Transcribing); err != nil {
			t.Fatal(err)
		}
		if o.want.stack != nil {
			t.Fatalf("a single Show kept a stale stack: %v", o.want.stack)
		}
	})
	t.Run("x11", func(t *testing.T) {
		o := &X11{wake: make(chan struct{}, 1)}
		if err := o.ShowStack([]Visual{Recording, Error}); err != nil {
			t.Fatal(err)
		}
		if o.want.visual != Recording || !reflect.DeepEqual(o.want.stack, []Visual{Error}) {
			t.Fatalf("want = {%v %v}, want {Recording [Error]}", o.want.visual, o.want.stack)
		}
		if err := o.Show(Transcribing); err != nil {
			t.Fatal(err)
		}
		if o.want.stack != nil {
			t.Fatalf("a single Show kept a stale stack: %v", o.want.stack)
		}
	})
}

// An empty stack hides the window, exactly as Show(Hidden) does.
func TestEmptyStackHides(t *testing.T) {
	n := &Noop{}
	if err := n.ShowStack(nil); err != nil {
		t.Fatal(err)
	}
	if got := n.Stacks(); len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("Stacks = %v, want one empty stack", got)
	}
	if got := n.Calls(); len(got) != 0 {
		t.Fatalf("Calls = %v, want no primary recorded for an empty stack", got)
	}
}
