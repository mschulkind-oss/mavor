package overlay

import (
	"image"
	"strings"
	"testing"
)

func previewFace(t *testing.T) *faces {
	t.Helper()
	f, err := textFaces()
	if err != nil {
		t.Fatalf("textFaces: %v", err)
	}
	return f
}

func TestShortPreviewIsUntouched(t *testing.T) {
	f := previewFace(t)
	const s = "hello there"
	if got := FitPreviewTail(f.preview, s, 10_000, 0); got != s {
		t.Errorf("FitPreviewTail = %q, want it unchanged", got)
	}
}

// Zero means uncapped, which is what a Scene built by hand in a test gets.
func TestZeroWidthMeansUncapped(t *testing.T) {
	f := previewFace(t)
	s := strings.Repeat("word ", 500)
	if got := FitPreviewTail(f.preview, s, 0, 0); got != s {
		t.Error("a zero cap trimmed the text; it must mean uncapped")
	}
}

// The tail is the point: the useful half of a live preview is what was just
// said, so the START is what gets dropped.
func TestLongPreviewKeepsItsTailAndMarksTheCut(t *testing.T) {
	f := previewFace(t)
	s := "alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima"

	got := FitPreviewTail(f.preview, s, 200, 0)
	if got == s {
		t.Fatal("text was not trimmed despite exceeding the cap")
	}
	if !strings.HasPrefix(got, previewEllipsis) {
		t.Errorf("trimmed preview %q does not mark the cut", got)
	}
	if !strings.HasSuffix(s, strings.TrimPrefix(got, previewEllipsis)) {
		t.Errorf("kept %q, which is not a suffix of the original", got)
	}
	if w := textWidth(f.preview, got, 0); w > 200 {
		t.Errorf("kept text is %.0fpx wide, over the 200px cap", w)
	}
}

// A cap too small for even one character must still produce something drawable
// rather than a panic or the whole untrimmed string.
func TestAbsurdlyNarrowCapDegradesToTheMark(t *testing.T) {
	f := previewFace(t)
	got := FitPreviewTail(f.preview, "some words here", 1, 0)
	if got != previewEllipsis {
		t.Errorf("FitPreviewTail = %q, want %q", got, previewEllipsis)
	}
}

// SceneSize and Render must agree on the drawn string, or the surface will not
// match its contents. Both go through previewText, so this pins that.
func TestCappedSceneStaysWithinItsWidth(t *testing.T) {
	long := Scene{Visual: Recording, Preview: strings.Repeat("chatter ", 200), MaxPreviewWidth: 400}
	w, _, err := SceneSize(long)
	if err != nil {
		t.Fatalf("SceneSize: %v", err)
	}
	if w > 400 {
		t.Errorf("scene width = %d, want it capped at 400", w)
	}

	uncapped := long
	uncapped.MaxPreviewWidth = 0
	uw, _, err := SceneSize(uncapped)
	if err != nil {
		t.Fatalf("SceneSize: %v", err)
	}
	if uw <= w {
		t.Errorf("uncapped width %d is not wider than capped %d — the cap did nothing", uw, w)
	}
}

// The overlay is centre-anchored, so a width that hugs the text re-centres the
// whole thing on every new word. A capped scene must therefore be a CONSTANT
// width regardless of how much text is in it.
func TestCappedSceneWidthDoesNotChangeAsTextGrows(t *testing.T) {
	widthFor := func(text string) int {
		t.Helper()
		w, _, err := SceneSize(Scene{Visual: Recording, Preview: text, MaxPreviewWidth: 600})
		if err != nil {
			t.Fatalf("SceneSize: %v", err)
		}
		return w
	}

	short := widthFor("one")
	medium := widthFor("one two three four five six seven")
	long := widthFor(strings.Repeat("chatter ", 200))

	if short != medium || medium != long {
		t.Errorf("widths %d, %d, %d differ; a capped preview must not resize as it grows", short, medium, long)
	}
	if short != 600 {
		t.Errorf("capped width = %d, want the cap 600", short)
	}
}

// An uncapped scene keeps the old hug-the-text behaviour, which is what the
// storybook and every hand-built test Scene rely on.
func TestUncappedSceneStillHugsItsText(t *testing.T) {
	// Both strings must be long enough to out-measure the pill itself, which
	// is ~330px wide and otherwise sets the scene width on its own.
	narrow, _, err := SceneSize(Scene{Visual: Recording, Preview: strings.Repeat("word ", 20)})
	if err != nil {
		t.Fatal(err)
	}
	wide, _, err := SceneSize(Scene{Visual: Recording, Preview: strings.Repeat("word ", 60)})
	if err != nil {
		t.Fatal(err)
	}
	if wide <= narrow {
		t.Errorf("uncapped widths %d and %d: more text should be wider", narrow, wide)
	}
}

// RenderInto is the reused-buffer path; it must clear what the previous frame
// left behind, or old glyphs ghost through.
func TestRenderIntoClearsTheBuffer(t *testing.T) {
	busy := Scene{Visual: Recording, Preview: "aaaaaaaaaaaaaaaaaaaaaaaa", MaxPreviewWidth: 600}
	w, h, err := SceneSize(busy)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := RenderInto(img, busy); err != nil {
		t.Fatal(err)
	}

	// Now draw a hidden scene into the same buffer: everything must go.
	if err := RenderInto(img, Scene{Visual: Hidden}); err != nil {
		t.Fatal(err)
	}
	for i, v := range img.Pix {
		if v != 0 {
			t.Fatalf("pixel byte %d = %d after clearing; the buffer still holds the previous frame", i, v)
		}
	}
}

func TestRenderIntoRefusesATooSmallBuffer(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	if err := RenderInto(img, Scene{Visual: Recording, Preview: "plenty of text here", MaxPreviewWidth: 600}); err == nil {
		t.Error("RenderInto accepted a buffer smaller than the scene")
	}
}
