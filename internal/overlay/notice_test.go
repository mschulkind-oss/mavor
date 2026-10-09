package overlay

import (
	"bytes"
	"image"
	"strings"
	"testing"
)

// The notice is the line under the recording pill that says the model's weights
// were evicted from VRAM into system RAM. It is a second strip, and a second
// strip is the thing that can resize the surface if it is added carelessly —
// which is why the canvas is pinned here rather than assumed.
const testNotice = "GPU memory full — model in system RAM"

// The fixed canvas is allocated once and never resized, so it has to have room
// for the pill, the preview and the notice at their widest. This is the test
// that fails when a new line is added without widening the reservation.
func TestFixedCanvasHoldsPreviewAndNotice(t *testing.T) {
	const cap = 640
	w, h, err := FixedSurfaceSize(cap)
	if err != nil {
		t.Fatal(err)
	}
	unpinned := Scene{Visual: Recording, Preview: strings.Repeat("the quick brown fox ", 8), Notice: testNotice, MaxPreviewWidth: cap}
	ow, oh, err := SceneSize(unpinned)
	if err != nil {
		t.Fatal(err)
	}
	if ow > w || oh > h {
		t.Fatalf("a recording with a preview and a notice wants %dx%d, but the fixed canvas is %dx%d", ow, oh, w, h)
	}
}

func TestNoticeIsPaintedInAFixedCanvas(t *testing.T) {
	const cap = 640
	w, h, err := FixedSurfaceSize(cap)
	if err != nil {
		t.Fatal(err)
	}
	s := Scene{Visual: Recording, Preview: "hello there", Notice: testNotice, MaxPreviewWidth: cap, SurfaceW: w, SurfaceH: h, Levels: ramp()}

	// A pinned canvas is returned as-is: the surface must not follow its
	// contents, and a notice is contents.
	if sw, sh, err := SceneSize(s); err != nil || sw != w || sh != h {
		t.Fatalf("SceneSize() = %dx%d, %v; want the pinned %dx%d", sw, sh, err, w, h)
	}

	img, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	without := s
	without.Notice = ""
	plain, err := Render(without)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(img.Pix, plain.Pix) {
		t.Fatal("a notice painted nothing")
	}

	claimed, err := SceneBounds(s)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed.In(img.Bounds()) {
		t.Fatalf("the notice's damage %v is not inside the surface %v", claimed, img.Bounds())
	}
	if painted := paintedBounds(img); !painted.In(claimed) {
		t.Fatalf("painted %v is not inside the claimed damage %v", painted, claimed)
	}

	// The notice has its own strip below the preview's, and it is the only
	// thing in it: the two strips are not drawn on top of each other.
	f, err := textFaces()
	if err != nil {
		t.Fatal(err)
	}
	_, pillH := pillSize(f, s)
	noticeRow := image.Rect(0, stripY(pillH, 2), w, stripY(pillH, 2)+stripHeight())
	if !rowHasInk(img, noticeRow) {
		t.Fatalf("nothing was painted in the notice's strip %v", noticeRow)
	}
	previewRow := image.Rect(0, stripY(pillH, 1), w, stripY(pillH, 1)+stripHeight())
	if !rowHasInk(img, previewRow) {
		t.Fatalf("the preview stopped being painted at %v", previewRow)
	}
}

// A scene that is not recording must paint the notice nowhere at all: a stale
// residency fact over a transcribing or error pill would be a lie about what is
// happening.
func TestNoticePaintsOnlyWhileRecording(t *testing.T) {
	for _, v := range []Visual{Hidden, Transcribing, Error, Initializing, Degraded} {
		t.Run(v.String(), func(t *testing.T) {
			base := Scene{Visual: v, SurfaceW: 1280, SurfaceH: 170, Levels: ramp()}
			withNotice := base
			withNotice.Notice = testNotice

			plain, err := Render(base)
			if err != nil {
				t.Fatal(err)
			}
			noticed, err := Render(withNotice)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(plain.Pix, noticed.Pix) {
				t.Errorf("%v painted a notice", v)
			}
			pb, err := SceneBounds(base)
			if err != nil {
				t.Fatal(err)
			}
			nb, err := SceneBounds(withNotice)
			if err != nil {
				t.Fatal(err)
			}
			if pb != nb {
				t.Errorf("%v claimed more damage with a notice: %v vs %v", v, nb, pb)
			}
		})
	}
}

// An unpinned scene (the storybook, and any hand-built scene) has to make room
// for the notice in the slot it is drawn in, or the strip would fall outside
// the image it reported.
func TestNoticeReservesItsOwnSlot(t *testing.T) {
	preview := Scene{Visual: Recording, Preview: "hello there"}
	pw, ph, err := SceneSize(preview)
	if err != nil {
		t.Fatal(err)
	}
	both := preview
	both.Notice = testNotice
	bw, bh, err := SceneSize(both)
	if err != nil {
		t.Fatal(err)
	}
	if bh-ph != previewGap+stripHeight() {
		t.Errorf("adding the notice grew the scene by %d, want one strip and its gap (%d)", bh-ph, previewGap+stripHeight())
	}
	if bw < pw {
		t.Errorf("the notice narrowed the scene from %d to %d", pw, bw)
	}

	// And a notice with no preview at all still keeps the second slot, so it
	// does not jump down when the first partial arrives.
	noticeOnly := Scene{Visual: Recording, Notice: testNotice}
	_, nh, err := SceneSize(noticeOnly)
	if err != nil {
		t.Fatal(err)
	}
	if nh != bh {
		t.Errorf("a notice without a preview sized %d, want the same %d as with one", nh, bh)
	}
}

func TestNoticeIsTrimmedToTheCanvas(t *testing.T) {
	const cap = 320
	w, h, err := FixedSurfaceSize(cap)
	if err != nil {
		t.Fatal(err)
	}
	s := Scene{Visual: Recording, Notice: strings.Repeat("evicted ", 20), MaxPreviewWidth: cap, SurfaceW: w, SurfaceH: h}
	img, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := SceneBounds(s)
	if err != nil {
		t.Fatal(err)
	}
	if !paintedBounds(img).In(claimed) || !claimed.In(img.Bounds()) {
		t.Fatalf("an over-long notice escaped its strip: painted %v claimed %v surface %v",
			paintedBounds(img), claimed, img.Bounds())
	}
}

// Show starts the strip over on a state change, the way it already does for the
// preview: the notice belonged to the state being left.
func TestOverlayBackendsClearTheNoticeOnShow(t *testing.T) {
	t.Run("wl", func(t *testing.T) {
		o := &WL{wake: make(chan struct{}, 1)}
		if err := o.Show(Recording); err != nil {
			t.Fatal(err)
		}
		if err := o.SetNotice(testNotice); err != nil {
			t.Fatal(err)
		}
		if o.want.notice != testNotice {
			t.Fatalf("the WL backend dropped a notice: %q", o.want.notice)
		}
		if err := o.Show(Transcribing); err != nil {
			t.Fatal(err)
		}
		if o.want.notice != "" {
			t.Errorf("the WL backend kept a stale notice across a state change: %q", o.want.notice)
		}
	})
	t.Run("x11", func(t *testing.T) {
		o := &X11{wake: make(chan struct{}, 1)}
		if err := o.Show(Recording); err != nil {
			t.Fatal(err)
		}
		if err := o.SetNotice(testNotice); err != nil {
			t.Fatal(err)
		}
		if o.want.notice != testNotice {
			t.Fatalf("the X11 backend dropped a notice: %q", o.want.notice)
		}
		if err := o.Show(Error); err != nil {
			t.Fatal(err)
		}
		if o.want.notice != "" {
			t.Errorf("the X11 backend kept a stale notice across a state change: %q", o.want.notice)
		}
	})
}

// rowHasInk reports whether any pixel in r has any alpha.
func rowHasInk(img *image.RGBA, r image.Rectangle) bool {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.RGBAAt(x, y).A != 0 {
				return true
			}
		}
	}
	return false
}
