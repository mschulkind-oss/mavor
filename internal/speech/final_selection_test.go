package speech

import (
	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveFinalSelectionRejectsUnsupportedBeforeLoading(t *testing.T) {
	for _, model := range []string{"whisper-large-v3", "parakeet-tdt-0.6b-v2", "/custom/path"} {
		cfg := config.Default()
		cfg.Model = model
		cfg.Advanced.FinalMode = "streaming"
		if _, e := Resolve(cfg); e == nil || !strings.Contains(e.Error(), "final") {
			t.Fatalf("%s accepted: %v", model, e)
		}
	}
	cfg := config.Default()
	cfg.Model = "nemotron-streaming-en-560ms"
	cfg.Advanced.FinalMode = "segments"
	if _, e := Resolve(cfg); e == nil {
		t.Fatal("online segments accepted")
	}
}
func TestResolveFinalSelectionChecksActualLayout(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Paths.Models = root
	cfg.Model = "nemotron-streaming-en-560ms"
	cfg.Advanced.FinalMode = "streaming"
	m, _ := models.Lookup(cfg.Model)
	dir := filepath.Join(root, "sherpa", m.TargetDir)
	if m.TargetDir == "" {
		dir = filepath.Join(root, "sherpa", cfg.Model)
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	// A distinct offline layout overrides a streaming catalog claim.
	for _, name := range []string{"preprocess.onnx", "tokens.txt"} {
		if e := os.WriteFile(filepath.Join(dir, name), nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := Resolve(cfg); e == nil || !strings.Contains(e.Error(), "layout") {
		t.Fatalf("wrong actual layout: %v", e)
	}
}
func TestSegmentsAutoPreviewSharesAggregate(t *testing.T) {
	cfg := config.Default()
	cfg.Model = "parakeet-tdt-0.6b-v2"
	cfg.Advanced.FinalMode = "segments"
	cfg.Preview.Source = "auto"
	cfg.Preview.Enabled = true
	plan, e := ResolvePreview(cfg)
	if e != nil || plan.Mode != PreviewPhrases || plan.Companion != "" || len(PreviewModels(cfg)) != 0 {
		t.Fatalf("%+v %v", plan, e)
	}
}
func TestChunkingForwardsFinalAbort(t *testing.T) {
	f := &finalFake{final: "wrapped"}
	wrapped := WrapChunking(f, "auto", nil)
	path, b := finalWAV(t, []int16{1, 2})
	s := newFinal(t, wrapped, models.FinalStreaming)
	_ = s.Feed(b)
	s.FailLive("deliberate failure")
	r, e := s.Finish(t.Context(), path)
	if e != nil || r.Text != "complete replay" || f.aborts != 1 {
		t.Fatalf("%+v %v aborts=%d", r, e, f.aborts)
	}
}

func TestCompanionDoesNotInheritMainFinalMode(t *testing.T) {
	for _, mode := range []models.FinalMode{models.FinalStreaming, models.FinalSegments} {
		cfg := config.Default()
		cfg.Advanced.FinalMode = string(mode)
		got := companionConfig(cfg, DefaultCompanionModel)
		if got.Advanced.FinalMode != string(models.FinalAfterStop) {
			t.Fatalf("companion inherited main final mode %q", got.Advanced.FinalMode)
		}
	}
}

func TestExplicitPhrasePreviewReportsSharedLiveMain(t *testing.T) {
	for _, tc := range []struct {
		mode   models.FinalMode
		want   PreviewMode
		reason string
	}{
		{models.FinalStreaming, PreviewMainModel, "partial"},
		{models.FinalSegments, PreviewPhrases, "segment"},
	} {
		cfg := config.Default()
		cfg.Preview.Enabled = true
		cfg.Preview.Source = "phrases"
		cfg.Advanced.FinalMode = string(tc.mode)
		plan, err := ResolvePreview(cfg)
		if err != nil || plan.Mode != tc.want || !strings.Contains(plan.Reason, tc.reason) || !strings.Contains(plan.Reason, "no separate") {
			t.Fatalf("%s: %+v %v", tc.mode, plan, err)
		}
	}
}
