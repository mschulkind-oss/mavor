package speech

import (
	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
	"os"
	"path/filepath"
	"testing"
)

// The whole point of the resolver: the name the catalog uses and the name the
// file has are different, and every caller gets the same answer.
func TestWhisperModelPathUsesTheUpstreamFilename(t *testing.T) {
	got := WhisperModelPath("/cache", "whisper-base.en")
	want := filepath.Join("/cache", "ggml-base.en.bin")
	if got != want {
		t.Errorf("WhisperModelPath = %q, want %q", got, want)
	}
}

func TestWhisperModelFileCoversEveryCatalogueEntry(t *testing.T) {
	cases := map[string]string{
		"whisper-tiny":            "ggml-tiny.bin",
		"whisper-tiny.en":         "ggml-tiny.en.bin",
		"whisper-base":            "ggml-base.bin",
		"whisper-base.en":         "ggml-base.en.bin",
		"whisper-small":           "ggml-small.bin",
		"whisper-small.en":        "ggml-small.en.bin",
		"whisper-medium":          "ggml-medium.bin",
		"whisper-medium.en":       "ggml-medium.en.bin",
		"whisper-large-v3":        "ggml-large-v3.bin",
		"whisper-large-v3-turbo":  "ggml-large-v3-turbo.bin",
		"whisper-distil-large-v3": "ggml-distil-large-v3.bin",
	}
	for name, want := range cases {
		if got := WhisperModelFile(name); got != want {
			t.Errorf("WhisperModelFile(%q) = %q, want %q", name, got, want)
		}
	}
	if len(whisperModelFiles) != len(cases) {
		t.Errorf("the resolver knows %d whisper names, this test checks %d",
			len(whisperModelFiles), len(cases))
	}
}

// A model the user placed or converted themselves has no catalog entry, and
// its name is taken as the GGML stem so it still resolves to something.
func TestWhisperModelFileFallsBackForAnUncataloguedName(t *testing.T) {
	if got := WhisperModelFile("my-own-tune"); got != "ggml-my-own-tune.bin" {
		t.Errorf("WhisperModelFile(my-own-tune) = %q, want ggml-my-own-tune.bin", got)
	}
	if got := WhisperModelFile("whisper-my-own-tune"); got != "ggml-my-own-tune.bin" {
		t.Errorf("a whisper- prefixed custom name should drop the prefix, got %q", got)
	}
}

func TestWhisperCatalogNameIsTheInverse(t *testing.T) {
	name, known := WhisperCatalogName("ggml-base.en.bin")
	if !known || name != "whisper-base.en" {
		t.Errorf("WhisperCatalogName(ggml-base.en.bin) = %q, %v; want whisper-base.en, true", name, known)
	}
	name, known = WhisperCatalogName("ggml-my-own-tune.bin")
	if known {
		t.Error("an uncatalogued file was reported as a catalog entry")
	}
	if name != "my-own-tune" {
		t.Errorf("an uncatalogued file should keep its stem, got %q", name)
	}
}

func TestIsWhisperModelFile(t *testing.T) {
	if !IsWhisperModelFile("ggml-tiny.en.bin") {
		t.Error("ggml-tiny.en.bin is a whisper model file")
	}
	if IsWhisperModelFile("tokens.txt") {
		t.Error("tokens.txt is not a whisper model file")
	}
	if IsWhisperModelFile("ggml-partial.bin.part") {
		t.Error("an in-progress download is not a whisper model file")
	}
}

// A catalog entry may live on disk under a directory that is not its name.
// fastconformer-streaming pins TargetDir to "parakeet" so renaming it did not
// orphan an existing multi-hundred-megabyte download — and the resolver looked
// models up by NAME, so it could not find what `models pull` had just written.
// The model was unusable from a clean install, and the error said the model
// was "not found" right after downloading it.
func TestSherpaResolverFindsAModelUnderItsTargetDir(t *testing.T) {
	var pinned string
	for _, m := range models.Catalog {
		if m.TargetDir != "" && m.TargetDir != m.Name {
			pinned = m.Name
			break
		}
	}
	if pinned == "" {
		t.Skip("no catalog entry pins a TargetDir different from its name")
	}
	entry, _ := models.Lookup(pinned)

	root := t.TempDir()
	dir := filepath.Join(root, "sherpa", entry.TargetDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"encoder.onnx", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	cfg.Model = pinned
	cfg.Paths.Models = root

	got, err := ResolveSherpaModelDir(cfg)
	if err != nil {
		t.Fatalf("ResolveSherpaModelDir(%q): %v — `models pull` writes to %q, so the resolver must look there",
			pinned, err, entry.TargetDir)
	}
	if got != dir {
		t.Errorf("resolved %q, want %q", got, dir)
	}
}
