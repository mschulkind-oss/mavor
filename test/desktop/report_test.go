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
	want := []string{"hidden", "recording-00", "recording-15", "recording-35", "recording-55", "recording-75", "recording-100", "transcribing", "error", "preview-short", "preview-long", "preview-cleared", "initializing", "ready-after-initializing", "initializing-before-error", "initialization-error", "gpu-request-error", "degraded", "motion-quiet", "motion-speech", "motion-pause", "motion-recovery", "motion-decayed"}
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
