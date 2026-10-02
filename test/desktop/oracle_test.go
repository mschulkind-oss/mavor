package desktop

import (
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"image"
	"image/color"
	"testing"
)

func TestOracleNegatives(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for y := 0; y < 1080; y++ {
		for x := 0; x < 1920; x++ {
			base.Set(x, y, color.RGBA{20, 30, 40, 255})
		}
	}
	scene := overlay.Scene{Visual: overlay.Recording, Levels: make([]float64, 46), SurfaceW: 560, SurfaceH: 101, MaxPreviewWidth: 560}
	for i := range scene.Levels {
		scene.Levels[i] = .75
	}
	r := overlay.FrameReceipt{Backend: "wayland", Status: "submitted", Frame: 1, Revision: 1, Scene: scene, Canvas: image.Pt(560, 101), Scale: 1}
	good := fixture(base, scene, image.Pt(680, 40))
	if _, err := Validate(base, good, r); err != nil {
		t.Fatal(err)
	}
	cases := map[string]image.Image{"missing": base}
	wrong := scene
	wrong.Visual = overlay.Error
	cases["swapped"] = fixture(base, wrong, image.Pt(680, 40))
	wrong = scene
	wrong.Levels = make([]float64, 46)
	cases["waveform absent"] = fixture(base, wrong, image.Pt(680, 40))
	wrong = scene
	wrong.Levels = append([]float64(nil), scene.Levels...)
	for i := range wrong.Levels {
		wrong.Levels[i] = .15
	}
	cases["wrong waveform"] = fixture(base, wrong, image.Pt(680, 40))
	duplicate := fixture(good, scene, image.Pt(100, 40))
	cases["duplicate"] = duplicate
	for name, img := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Validate(base, img, r); err == nil {
				t.Fatal("accepted incorrect HUD")
			}
		})
	}
}

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Scenes() {
		if seen[s.ID] {
			t.Fatal("duplicate scene", s.ID)
		}
		seen[s.ID] = true
	}
	for _, id := range []string{"hidden", "recording-00", "preview-long", "initializing", "initialization-error", "degraded", "motion-quiet", "motion-speech", "motion-pause", "motion-recovery", "motion-decayed"} {
		if !seen[id] {
			t.Fatal("missing required scene", id)
		}
	}
}

func TestDistinctRecordingFrames(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	levels := []float64{0, .6704365036222726, .8176272177401103, .8961450757976975, .95002450535668, 1}
	for i, lv := range levels {
		s := overlay.Scene{Visual: overlay.Recording, Levels: make([]float64, 46), SurfaceW: 960, SurfaceH: 101, MaxPreviewWidth: 960}
		for k := range s.Levels {
			s.Levels[k] = lv
		}
		r := overlay.FrameReceipt{Backend: "x11", Status: "submitted", Frame: 1, Revision: 1, Scene: s, Scale: 1}
		for j, other := range levels {
			if i == j {
				continue
			}
			wrong := s
			wrong.Levels = make([]float64, 46)
			for k := range wrong.Levels {
				wrong.Levels[k] = other
			}
			if _, err := Validate(base, fixture(base, wrong, image.Pt(480, 40)), r); err == nil {
				t.Errorf("reused level %.2f accepted as %.2f", other, lv)
			}
		}
	}
}

func TestPreviewAndReceiptNegatives(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	s := overlay.Scene{Visual: overlay.Recording, Levels: make([]float64, 46), SurfaceW: 960, SurfaceH: 101, MaxPreviewWidth: 960, Preview: "Live preview"}
	r := overlay.FrameReceipt{Backend: "x11", Status: "submitted", Frame: 1, Revision: 1, Scene: s, Scale: 1}
	missing := s
	missing.Preview = ""
	if _, err := Validate(base, fixture(base, missing, image.Pt(480, 40)), r); err == nil {
		t.Fatal("missing preview accepted")
	}
	wrong := s
	wrong.Preview = "Wrong preview"
	if _, err := Validate(base, fixture(base, wrong, image.Pt(480, 40)), r); err == nil {
		t.Fatal("wrong preview accepted")
	}
	r.Status = "queued"
	if _, err := Validate(base, fixture(base, s, image.Pt(480, 40)), r); err == nil {
		t.Fatal("submission receipt required")
	}
}

func TestHiddenBaselineCannotContainHUD(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	scene := overlay.Scene{Visual: overlay.Error, SurfaceW: 960, SurfaceH: 101}
	bad := fixture(base, scene, image.Pt(480, 40))
	r := overlay.FrameReceipt{Backend: "x11", Status: "submitted", Frame: 1, Revision: 1, Scene: overlay.Scene{Visual: overlay.Hidden}, Scale: 1}
	if _, err := Validate(bad, bad, r); err == nil {
		t.Fatal("contaminated hidden baseline accepted")
	}
}

func TestOracleAllowsOnlyIndicatorAnimation(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for _, v := range []overlay.Visual{overlay.Initializing, overlay.Transcribing, overlay.Recording} {
		s := overlay.Scene{Visual: v, SurfaceW: 960, SurfaceH: 101, Preview: "Controlled diagnostic", MaxPreviewWidth: 960, Phase: 0}
		r := overlay.FrameReceipt{Backend: "wayland", Status: "submitted", Frame: 1, Revision: 1, Scene: s, Scale: 1}
		other := s
		other.Phase = .35
		if _, err := Validate(base, fixture(base, other, image.Pt(480, 40)), r); err != nil {
			t.Fatalf("legitimate indicator animation %v: %v", v, err)
		}
		other.Preview = "wrong diagnostic"
		if _, err := Validate(base, fixture(base, other, image.Pt(480, 40)), r); err == nil && v != overlay.Transcribing {
			t.Fatal("animation allowance hid missing diagnostic")
		}
	}
}

func TestOracleCompositesDiagnosticAlphaWithRounding(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for y := 0; y < 1080; y++ {
		for x := 0; x < 1920; x++ {
			base.SetRGBA(x, y, color.RGBA{32, 60, 70, 255})
		}
	}
	s := overlay.Scene{Visual: overlay.Error, Preview: "Readiness inference failed: controlled fixture diagnostic", SurfaceW: 960, SurfaceH: 101, MaxPreviewWidth: 960}
	r := overlay.FrameReceipt{Backend: "wayland", Status: "submitted", Frame: 1, Revision: 1, Scene: s, Scale: 1}
	actual := fixture(base, s, image.Pt(480, 40))
	ref, _ := overlay.Render(s)
	for y := 0; y < 101; y++ {
		for x := 0; x < 960; x++ {
			c := ref.RGBAAt(x, y)
			a := int(c.A)
			actual.SetRGBA(x+480, y+40, color.RGBA{uint8(int(c.R) + (32*(255-a)+127)/255), uint8(int(c.G) + (60*(255-a)+127)/255), uint8(int(c.B) + (70*(255-a)+127)/255), 255})
		}
	}
	if _, err := Validate(base, actual, r); err != nil {
		t.Fatal(err)
	}
}
