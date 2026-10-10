package desktop

import (
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedCatalogAndTemplate(t *testing.T) {
	want := []string{"hidden", "recording-00", "recording-15", "recording-35", "recording-55", "recording-75", "recording-100", "transcribing", "error", "stacked", "stacked-transcribing", "stacked-error", "preview-short", "preview-long", "preview-cleared", "initializing", "ready-after-initializing", "initializing-before-error", "initialization-error", "gpu-request-error", "degraded", "motion-quiet", "motion-speech", "motion-pause", "motion-recovery", "motion-decayed", "motion-stacked"}
	scenes := Scenes()
	if len(scenes) != len(want) {
		t.Fatalf("catalog IDs changed: got %d want %d", len(scenes), len(want))
	}
	for i, s := range scenes {
		if s.ID != want[i] || s.Index != i+1 {
			t.Fatalf("catalog drift %v", s)
		}
		n := 1
		if s.Visual == overlay.Recording {
			n = 50
		}
		if s.Motion == "" && s.FeedFrames != n {
			t.Fatal("shared driver frame schedule drift")
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "report.html")
	if err := GenerateHTMLReport(path, ReportData{Compositor: "GNOME <measured>", CaptureMethod: "Shell Screenshot", TotalStates: len(scenes), Captures: []StateCapture{{State: StoryState{Title: "<unsafe>", Index: 1}}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"GNOME &lt;measured&gt;", "&lt;unsafe&gt;", "Shell Screenshot", "data-filter=", "data-view=", "themeToggle", "lightbox", "gnome-clipboard-qa.html"} {
		if !strings.Contains(string(data), token) {
			t.Fatal("missing shared template feature", token)
		}
	}
	if strings.Contains(string(data), "Headless Sway Desktop") {
		t.Fatal("desktop-specific template fork")
	}
}

// The inline crop follows what the scene painted, not a fixed band. A stacked
// scene reserves three pill rows and is taller than the floor, so its crop has
// to grow; a scene that paints nothing has to fall back to the floor rather
// than cropping to a sliver.
func TestCropHeightFollowsPaintedRows(t *testing.T) {
	shot := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	byID := map[string]StoryState{}
	for _, s := range Scenes() {
		byID[s.ID] = s
	}
	// placed reconstructs what the compositor drew: the scene centred under the
	// panel (32px per the report layout) plus the production 8px margin.
	placed := func(id string) (overlay.Scene, image.Rectangle) {
		s := byID[id]
		sc := overlay.Scene{Visual: s.Visual, Stack: s.Stack, Preview: s.Preview, Levels: make([]float64, 46)}
		w, h, err := overlay.SceneSize(sc)
		if err != nil {
			t.Fatal(err)
		}
		const canvasTop = 40
		return sc, image.Rect((1920-w)/2, canvasTop, (1920-w)/2+w, canvasTop+h)
	}

	for _, id := range []string{"recording-55", "stacked", "stacked-error", "preview-long"} {
		sc, canvas := placed(id)
		got, err := cropHeightFor(sc, canvas, shot)
		if err != nil {
			t.Fatal(err)
		}
		b, err := overlay.SceneBounds(sc)
		if err != nil {
			t.Fatal(err)
		}
		if want := canvas.Min.Y + b.Max.Y + cropMargin; got < want {
			t.Fatalf("%s crop %d clips painted rows (need at least %d)", id, got, want)
		}
	}

	stackedScene, stackedCanvas := placed("stacked")
	got, err := cropHeightFor(stackedScene, stackedCanvas, shot)
	if err != nil {
		t.Fatal(err)
	}
	if got <= minCropHeight {
		t.Fatalf("stacked crop %d did not grow past the minimum %d", got, minCropHeight)
	}

	hiddenScene, hiddenCanvas := placed("hidden")
	got, err = cropHeightFor(hiddenScene, hiddenCanvas, shot)
	if err != nil {
		t.Fatal(err)
	}
	if got != minCropHeight {
		t.Fatalf("hidden crop %d != floor %d", got, minCropHeight)
	}
}

// A submitted receipt is not proof that a compositor presented the frame.
// One-shot capture must fail closed, not publish an absent HUD as a report.
func TestReportRejectsUnpresentedSubmission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = png.Encode(f, image.NewRGBA(image.Rect(0, 0, 1920, 1080))); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	var evidence []Evidence
	for i, s := range Scenes() {
		evidence = append(evidence, Evidence{ID: s.ID, File: path, Receipt: overlay.FrameReceipt{
			Backend: "wayland", Status: "submitted", Frame: uint64(i + 1), Revision: uint64(i + 1), Scale: 1,
			Scene: overlay.Scene{Visual: s.Visual, Preview: s.Preview, SurfaceW: 960, SurfaceH: 101, MaxPreviewWidth: 960},
		}})
	}
	if err = BuildReport(dir, "absent", "Sway", "Grim", "fixture", evidence); err == nil || !strings.Contains(err.Error(), "recording-00: shared painter mismatch") {
		t.Fatalf("unpresented submission accepted: %v", err)
	}
	for _, ext := range []string{".html", ".json"} {
		if _, err = os.Stat(filepath.Join(dir, "absent"+ext)); !os.IsNotExist(err) {
			t.Fatalf("failed capture published report %s: %v", ext, err)
		}
	}
}

func TestMotionEvidenceRejectsMissingTimingAndStaticHistory(t *testing.T) {
	var e []Evidence
	for _, s := range Scenes() {
		if s.Motion != "" {
			e = append(e, Evidence{ID: s.ID})
		}
	}
	if err := validateMotion(e); err == nil {
		t.Fatal("untimed frames accepted")
	}
}
