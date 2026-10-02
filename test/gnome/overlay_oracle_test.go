//go:build gnome

package gnome

import (
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"image"
	"image/draw"
	"testing"
)

// In-memory negative fixtures are never exported as compositor screenshots.
func TestBackendOracleRejectsSwapsAndDuplicates(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	scene := overlay.Scene{Visual: overlay.Recording, SurfaceW: 960, SurfaceH: 101, MaxPreviewWidth: 960, Levels: make([]float64, 46)}
	for i := range scene.Levels {
		scene.Levels[i] = .75
	}
	composite := func(s overlay.Scene, duplicate bool) image.Image {
		img := image.NewRGBA(base.Bounds())
		ref, err := overlay.Render(s)
		if err != nil {
			t.Fatal(err)
		}
		draw.Draw(img, ref.Bounds().Add(image.Pt(480, 40)), ref, image.Point{}, draw.Over)
		if duplicate {
			draw.Draw(img, ref.Bounds().Add(image.Pt(100, 240)), ref, image.Point{}, draw.Over)
		}
		return img
	}
	for _, visual := range []overlay.Visual{overlay.Recording, overlay.Error} {
		expected := scene
		expected.Visual = visual
		r := overlay.FrameReceipt{Backend: "x11", Status: "submitted", Frame: 1, Revision: 1, Scale: 1, Canvas: image.Pt(960, 101), Screen: image.Rect(480, 40, 1440, 141), Scene: expected}
		if _, err := validateOverlayCapture(base, composite(expected, false), r); err != nil {
			t.Fatal(err)
		}
		wrong := scene
		if visual == overlay.Recording {
			wrong.Visual = overlay.Error
		}
		t.Run(visual.String(), func(t *testing.T) {
			if _, err := validateOverlayCapture(base, composite(wrong, false), r); err == nil {
				t.Error("accepted Recording/Error swap")
			}
			if _, err := validateOverlayCapture(base, composite(expected, true), r); err == nil {
				t.Error("accepted duplicate HUD outside canvas")
			}
		})
	}
}
