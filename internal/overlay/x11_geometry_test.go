package overlay

import (
	"image"
	"testing"
)

func TestWorkareaPlacement(t *testing.T) {
	r := image.Rect(-1920, 32, 0, 1080)
	got, err := placeHUD(r, 640, 91, 8)
	if err != nil || got != image.Rect(-1280, 40, -640, 131) {
		t.Fatalf("%v %v", got, err)
	}
	for _, r := range []image.Rectangle{{}, image.Rect(0, 0, 10, 10)} {
		if _, err := placeHUD(r, 640, 91, 8); err == nil {
			t.Fatal("accepted unusable workarea")
		}
	}
}

func TestMonitorCoordinateMapping(t *testing.T) {
	for _, tc := range []struct {
		logical, x image.Rectangle
		factor     float64
	}{{image.Rect(0, 0, 1920, 1080), image.Rect(0, 0, 1920, 1080), 1}, {image.Rect(-1920, 0, 0, 1080), image.Rect(-3840, 0, 0, 2160), 2}, {image.Rect(0, 0, 1536, 864), image.Rect(0, 0, 3072, 1728), 2}} {
		factor, err := coordinateFactor([]image.Rectangle{tc.logical}, []image.Rectangle{tc.x})
		if err != nil || factor != tc.factor {
			t.Fatalf("factor %v err %v", factor, err)
		}
	}
	if _, err := coordinateFactor(nil, nil); err == nil {
		t.Fatal("no monitor accepted")
	}
	if _, err := coordinateFactor([]image.Rectangle{image.Rect(0, 0, 1920, 1080)}, []image.Rectangle{image.Rect(0, 0, 1920, 1000)}); err == nil {
		t.Fatal("ambiguous scale accepted")
	}
	// Mixed per-monitor scale still uses one consistent global X coordinate factor.
	logical := []image.Rectangle{image.Rect(-1920, 0, 0, 1080), image.Rect(0, 0, 1536, 864)}
	xs := []image.Rectangle{image.Rect(0, 0, 3072, 1728), image.Rect(-3840, 0, 0, 2160)}
	if f, e := coordinateFactor(logical, xs); e != nil || f != 2 {
		t.Fatalf("mixed layout: %v %v", f, e)
	}
}

func TestWorkspaceGeometryAmbiguity(t *testing.T) {
	if !sameWorkareas([][]uint32{{0, 32, 1920, 1048}, {0, 32, 1920, 1048}}) {
		t.Fatal("equal desktop areas rejected")
	}
	for _, a := range [][][]uint32{nil, {{}}, {{0, 32, 1920, 1048}, {0, 64, 1920, 1016}}, {{0, 32, 1920, 1048}, {0, 32, 1920}}} {
		if sameWorkareas(a) {
			t.Fatal("ambiguous desktop areas accepted")
		}
	}
}
