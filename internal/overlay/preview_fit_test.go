package overlay

import (
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
