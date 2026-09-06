package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

func TestKnownModelsCatalog(t *testing.T) {
	requiredFamilies := map[string][]string{
		"Whisper": {
			"whisper-tiny", "whisper-tiny.en", "whisper-base", "whisper-base.en",
			"whisper-small", "whisper-small.en", "whisper-medium", "whisper-medium.en",
			"whisper-large-v3", "whisper-large-v3-turbo", "whisper-distil-large-v3",
		},
		"NeMo": {
			"fastconformer-streaming", "parakeet-tdt-0.6b", "parakeet-unified-en",
			"parakeet-ctc", "canary-1b", "canary-180m",
		},
		"Moonshine": {
			"moonshine-tiny", "moonshine-base",
		},
		"SenseVoice": {
			"sensevoice-small",
		},
		"Zipformer": {
			"zipformer-streaming", "zipformer-streaming-20m", "zipformer-offline", "zipformer-ctc",
		},
	}

	for family, names := range requiredFamilies {
		for _, m := range names {
			spec, ok := models.Lookup(m)
			if !ok {
				t.Errorf("missing model %q in the catalog (family: %s)", m, family)
				continue
			}
			if spec.URL == "" {
				t.Errorf("model %q has empty URL", m)
			}
			if !strings.HasPrefix(spec.URL, "https://") {
				t.Errorf("model %q URL is not HTTPS: %s", m, spec.URL)
			}
			if spec.Engine != "whisper" && spec.Engine != "sherpa" {
				t.Errorf("model %q has invalid engine: %s", m, spec.Engine)
			}
			if spec.Engine == "sherpa" && spec.TargetDir == "" {
				t.Errorf("sherpa model %q has empty TargetDir", m)
			}
			if spec.Description == "" {
				t.Errorf("model %q has empty Description", m)
			}
			if spec.Format == "" {
				t.Errorf("model %q has empty Format", m)
			}
			if spec.Engine == "whisper" && spec.Filename == "" {
				t.Errorf("whisper model %q has empty Filename; nothing knows what it is called on disk", m)
			}
		}
	}
}

// The catalog name and the on-disk name deliberately differ, and every code
// path that needs a whisper model's path goes through speech.WhisperModelPath.
// This is the regression guard for that: change the catalog without changing
// the resolver and mavor still compiles, still passes its other tests, and
// reports "model not found" from some code paths only.
func TestWhisperCatalogNameResolvesToTheUpstreamFilename(t *testing.T) {
	got := speech.WhisperModelPath("/models", "whisper-base.en")
	if want := filepath.Join("/models", "ggml-base.en.bin"); got != want {
		t.Errorf("speech.WhisperModelPath for whisper-base.en = %q, want %q", got, want)
	}
}

func TestEveryWhisperEntryResolvesToItsOwnFilename(t *testing.T) {
	dir := t.TempDir()
	for _, m := range models.Catalog {
		if m.Engine != "whisper" {
			continue
		}
		// The filename is what upstream serves, so the URL is the authority.
		if want := path.Base(m.URL); m.Filename != want {
			t.Errorf("model %q has Filename %q but its URL serves %q", m.Name, m.Filename, want)
		}
		if got, want := speech.WhisperModelPath(dir, m.Name), filepath.Join(dir, m.Filename); got != want {
			t.Errorf("speech.WhisperModelPath(%q) = %q, want %q — the resolver and the catalog disagree",
				m.Name, got, want)
		}
	}
}

// A sherpa entry has no Filename: it unpacks into a directory, not a file.
func TestSherpaEntriesCarryNoFilename(t *testing.T) {
	for _, m := range models.Catalog {
		if m.Engine == "sherpa" && m.Filename != "" {
			t.Errorf("sherpa model %q carries Filename %q; it unpacks into a directory", m.Name, m.Filename)
		}
	}
}

// The rule §5 of the config-surface design settled: a catalog name says which
// family of model it is before it says anything else. Encoded as a test so it
// cannot regress the next time an entry is added.
func TestEveryCatalogNameBeginsWithItsFamily(t *testing.T) {
	// The Family column is the vendor for the sherpa entries, and NVIDIA
	// ships three unrelated model families under the NeMo name, so a family
	// gets a set of prefixes rather than one. Every entry must match one of
	// its family's, and a family with no prefixes listed is a new family
	// nobody decided a name shape for.
	prefixes := map[string][]string{
		"Whisper":    {"whisper-"},
		"NeMo":       {"parakeet-", "canary-", "fastconformer-"},
		"Moonshine":  {"moonshine-"},
		"SenseVoice": {"sensevoice-"},
		"Paraformer": {"paraformer"},
		"Zipformer":  {"zipformer-"},
	}

	for _, m := range models.Catalog {
		allowed, known := prefixes[m.Family]
		if !known {
			t.Errorf("model %q is in family %q, which has no agreed name prefix", m.Name, m.Family)
			continue
		}
		matched := false
		for _, p := range allowed {
			matched = matched || strings.HasPrefix(m.Name, p)
		}
		if !matched {
			t.Errorf("model %q is in family %q but its name begins with none of %v",
				m.Name, m.Family, allowed)
		}
	}
}

func TestFormatFileSize(t *testing.T) {
	if got := formatFileSize(500 * 1024 * 1024); got != "500.0 MB" {
		t.Errorf("formatFileSize(500MB) = %q, want 500.0 MB", got)
	}
	if got := formatFileSize(1536 * 1024 * 1024); got != "1.50 GB" {
		t.Errorf("formatFileSize(1.5GB) = %q, want 1.50 GB", got)
	}
}

func TestDirSize(t *testing.T) {
	tmp := t.TempDir()
	file1 := filepath.Join(tmp, "a.bin")
	file2 := filepath.Join(tmp, "sub", "b.bin")
	_ = os.MkdirAll(filepath.Dir(file2), 0o755)

	_ = os.WriteFile(file1, make([]byte, 1024), 0o644)
	_ = os.WriteFile(file2, make([]byte, 2048), 0o644)

	size := dirSize(tmp)
	if size != 3072 {
		t.Errorf("dirSize(%s) = %d, want 3072", tmp, size)
	}
}

func TestDownloadAndExtractArchiveTarGz(t *testing.T) {
	// Create an in-memory tar.gz archive
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	files := map[string]string{
		"root_dir/tokens.txt":   "hello\nworld\n",
		"root_dir/encoder.onnx": "fake-encoder-data",
		"root_dir/sub/test.wav": "fake-wav-data",
	}

	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	destDir := filepath.Join(t.TempDir(), "extracted")
	if err := downloadAndExtractArchive(srv.URL, "tar.gz", destDir); err != nil {
		t.Fatalf("downloadAndExtractArchive error: %v", err)
	}

	// Verify files were extracted and top-level directory stripped
	tokensPath := filepath.Join(destDir, "tokens.txt")
	if data, err := os.ReadFile(tokensPath); err != nil || string(data) != "hello\nworld\n" {
		t.Errorf("tokens.txt not extracted properly: err=%v, data=%q", err, string(data))
	}

	encoderPath := filepath.Join(destDir, "encoder.onnx")
	if data, err := os.ReadFile(encoderPath); err != nil || string(data) != "fake-encoder-data" {
		t.Errorf("encoder.onnx not extracted properly: err=%v, data=%q", err, string(data))
	}

	subPath := filepath.Join(destDir, "sub", "test.wav")
	if data, err := os.ReadFile(subPath); err != nil || string(data) != "fake-wav-data" {
		t.Errorf("sub/test.wav not extracted properly: err=%v, data=%q", err, string(data))
	}
}

func TestRunModelsListDetailed(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	modelDir := filepath.Join(tmpDir, "mavor", "models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. Whisper model
	_ = os.WriteFile(filepath.Join(modelDir, "ggml-base.en.bin"), make([]byte, 1024*1024), 0o644)

	// 2. Sherpa model in sherpa/<target>
	parakeetDir := filepath.Join(modelDir, "sherpa", "parakeet")
	_ = os.MkdirAll(parakeetDir, 0o755)
	_ = os.WriteFile(filepath.Join(parakeetDir, "encoder.onnx"), make([]byte, 2*1024*1024), 0o644)
	_ = os.WriteFile(filepath.Join(parakeetDir, "tokens.txt"), []byte("tokens"), 0o644)

	// 3. Moonshine model in sherpa/moonshine
	moonshineDir := filepath.Join(modelDir, "sherpa", "moonshine")
	_ = os.MkdirAll(moonshineDir, 0o755)
	_ = os.WriteFile(filepath.Join(moonshineDir, "encode.int8.onnx"), make([]byte, 3*1024*1024), 0o644)
	_ = os.WriteFile(filepath.Join(moonshineDir, "tokens.txt"), []byte("tokens"), 0o644)

	if err := runModels([]string{"list"}); err != nil {
		t.Fatalf("runModels(list) error: %v", err)
	}

	if err := runModels([]string{"ls"}); err != nil {
		t.Fatalf("runModels(ls) error: %v", err)
	}
}

func TestRunModelsCommands(t *testing.T) {
	if err := runModels([]string{"help"}); err != nil {
		t.Fatalf("runModels(help) error: %v", err)
	}

	if err := runModels([]string{"pull"}); err == nil {
		t.Fatalf("expected error for 'mavor models pull' without arguments")
	}

	if err := runModels([]string{"invalid-command"}); err == nil {
		t.Fatalf("expected error for unknown command")
	}
}

// The catalog is the list users see when they ask what they can download, so
// every entry must be a distinct artifact.
func TestCatalogEntriesAreDistinctDownloads(t *testing.T) {
	seenURL := map[string]string{}
	for _, m := range models.Catalog {
		if prev, dup := seenURL[m.URL]; dup {
			t.Errorf("models %q and %q share a URL — they are one model, not two:\n  %s",
				prev, m.Name, m.URL)
		}
		seenURL[m.URL] = m.Name
	}
}

// One name per model, and one model per name. There are no aliases to fall
// back on, so a collision would silently hide an entry from `models pull`.
func TestNoTwoCatalogEntriesShareAName(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range models.Catalog {
		if seen[m.Name] {
			t.Errorf("duplicate catalog name %q", m.Name)
		}
		seen[m.Name] = true
	}
	if len(seen) != len(models.Catalog) {
		t.Errorf("the catalog index has %d names for %d catalog entries", len(seen), len(models.Catalog))
	}
	if models.Count() != len(models.Catalog) {
		t.Errorf("the catalog index has %d entries, want one per catalog entry (%d)",
			models.Count(), len(models.Catalog))
	}
}

// A name the catalog does not carry is an error, not a guess, and the error
// has to name real entries or it is no better than "not found".
func TestUnknownModelNameNamesTheClosestEntries(t *testing.T) {
	// The exact mistake the rename creates: the name that used to work.
	err := models.UnknownModelError("base.en")
	if err == nil {
		t.Fatal("an uncatalogued name was accepted")
	}
	if !strings.Contains(err.Error(), "whisper-base.en") {
		t.Errorf("error for %q does not point at whisper-base.en:\n%s", "base.en", err)
	}

	err = models.UnknownModelError("zipformer")
	if err == nil {
		t.Fatal("an uncatalogued name was accepted")
	}
	if !strings.Contains(err.Error(), "zipformer-streaming") {
		t.Errorf("error for %q does not point at a real zipformer entry:\n%s", "zipformer", err)
	}

	// Every candidate offered has to be pullable, or the suggestion is a
	// second dead end.
	for _, n := range models.Nearest("moonshin", 3) {
		if _, ok := models.Lookup(n); !ok {
			t.Errorf("suggested %q, which `mavor models pull` would reject", n)
		}
	}
}

// The old names are gone, not deprecated: resolving one would be the
// compatibility fallback the design rejected.
func TestRetiredNamesDoNotResolve(t *testing.T) {
	for _, gone := range []string{
		"tiny", "tiny.en", "base", "base.en", "small.en", "large-v3",
		"distil-whisper-large-v3", "parakeet", "parakeet-tdt",
		"parakeet-tdt-1.1b", "zipformer", "moonshine", "sensevoice", "canary",
	} {
		if _, ok := models.Lookup(gone); ok {
			t.Errorf("retired name %q still resolves; aliases were deleted, not flipped", gone)
		}
	}
}

// Every property the listing prints has to exist for every model, or the
// table renders holes.
func TestCatalogEntriesCarryTheirProperties(t *testing.T) {
	for _, m := range models.Catalog {
		if m.Name == "" {
			t.Error("catalog entry with empty Name")
			continue
		}
		if m.DownloadSize <= 0 {
			t.Errorf("model %q has no DownloadSize; the size column would be blank", m.Name)
		}
		if m.Languages == "" {
			t.Errorf("model %q has no Languages", m.Name)
		}
		if m.Description == "" {
			t.Errorf("model %q has no Description", m.Name)
		}
		if m.Engine != "whisper" && m.Engine != "sherpa" {
			t.Errorf("model %q has invalid engine %q", m.Name, m.Engine)
		}
		if !strings.HasPrefix(m.URL, "https://") {
			t.Errorf("model %q URL is not HTTPS: %s", m.Name, m.URL)
		}
	}
}

// The index is generated from the catalog: every name resolves, and a
// sherpa model lands in a directory named after its entry — that is what
// ResolveSherpaModelDir looks for.
func TestCatalogIndexIsGeneratedFromTheCatalog(t *testing.T) {
	for _, m := range models.Catalog {
		spec, ok := models.Lookup(m.Name)
		if !ok {
			t.Errorf("catalog name %q missing from the catalog index", m.Name)
			continue
		}
		if spec.URL != m.URL {
			t.Errorf("the catalog index for %q has URL %q, want %q", m.Name, spec.URL, m.URL)
		}
		if spec.Engine != "sherpa" {
			continue
		}
		want := m.TargetDir
		if want == "" {
			want = m.Name
		}
		if spec.TargetDir != want {
			t.Errorf("the catalog index for %q has TargetDir %q, want %q", m.Name, spec.TargetDir, want)
		}
	}
}

// fastconformer-streaming was called "parakeet" when it was downloaded, and
// TargetDir defaults to the catalog name. Without the pin the rename would
// move the directory the model is expected at and orphan a 450 MB download.
func TestRenamedSherpaEntryKeepsItsExistingDirectory(t *testing.T) {
	spec, ok := models.Lookup("fastconformer-streaming")
	if !ok {
		t.Fatal("fastconformer-streaming is not in the catalog")
	}
	if spec.TargetDir != "parakeet" {
		t.Errorf("TargetDir = %q, want %q so an existing download still resolves", spec.TargetDir, "parakeet")
	}
}

// Regression guard. Both of these shipped in the catalog as dictation models
// and neither can transcribe: the MMS entry pointed at a VITS text-to-speech
// voice, and the Seamless entry at a PyTorch checkpoint that sherpa-onnx
// (an ONNX runtime) cannot load.
func TestCatalogListsOnlyLoadableASRModels(t *testing.T) {
	for _, banned := range []string{"mms", "mms-1b", "seamless", "seamless-streaming"} {
		if _, ok := models.Lookup(banned); ok {
			t.Errorf("model %q is back in the catalog; it cannot be loaded for transcription", banned)
		}
	}
	for _, m := range models.Catalog {
		if strings.Contains(m.URL, "/tts-models/") {
			t.Errorf("model %q points at a text-to-speech artifact: %s", m.Name, m.URL)
		}
		if strings.HasSuffix(m.URL, ".pt") {
			t.Errorf("model %q points at a PyTorch checkpoint, which sherpa-onnx cannot load: %s", m.Name, m.URL)
		}
	}
}

func TestModelsListShowsTheCatalogWithStatus(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	modelDir := filepath.Join(tmpDir, "mavor", "models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// whisper-base.en is downloaded and is the configured model; the file on
	// disk keeps upstream's name. whisper-tiny.en is not downloaded.
	if err := os.WriteFile(filepath.Join(modelDir, "ggml-base.en.bin"), make([]byte, 1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	if err := listCatalog(buf, cfg, false); err != nil {
		t.Fatalf("listCatalog: %v", err)
	}
	out := buf.String()

	// A model that is not downloaded still appears — that is the point of
	// the catalog view.
	if !strings.Contains(out, "whisper-tiny.en") {
		t.Error("catalog listing omits whisper-tiny.en, which is supported but not downloaded")
	}
	if !strings.Contains(out, "whisper-large-v3-turbo") {
		t.Error("catalog listing omits whisper-large-v3-turbo")
	}
	// Properties are columns, not prose.
	for _, header := range []string{"NAME", "ENGINE", "SIZE", "LANGUAGES", "STREAM", "STATUS"} {
		if !strings.Contains(out, header) {
			t.Errorf("listing missing %q column header", header)
		}
	}
	// There is one name per model now, so there is nothing for an alias
	// column to hold.
	if strings.Contains(out, "ALIASES") {
		t.Error("listing still has an ALIASES column; aliases were deleted")
	}
	// Downloaded and active markers.
	if !strings.Contains(out, markerDownloaded) {
		t.Errorf("listing never shows the downloaded marker %q for whisper-base.en", markerDownloaded)
	}
	if !strings.Contains(out, markerActive) {
		t.Errorf("listing never shows the active marker %q for the configured model", markerActive)
	}
}

func TestModelsListInstalledShowsOnlyDownloaded(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	modelDir := filepath.Join(tmpDir, "mavor", "models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "ggml-base.en.bin"), make([]byte, 1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	if err := listCatalog(buf, cfg, true); err != nil {
		t.Fatalf("listCatalog(installed): %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "whisper-base.en") {
		t.Error("--installed listing omits the downloaded model")
	}
	if strings.Contains(out, "whisper-large-v3-turbo") {
		t.Error("--installed listing includes whisper-large-v3-turbo, which is not downloaded")
	}
}

func TestModelsListInstalledIsEmptyWithNoModels(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	if err := listCatalog(buf, cfg, true); err != nil {
		t.Fatalf("listCatalog on an empty cache should not error: %v", err)
	}
	if !strings.Contains(buf.String(), "models pull") {
		t.Error("empty listing should point the user at 'mavor models pull'")
	}
}

func TestModelsListAcceptsTheInstalledFlag(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	if err := runModels([]string{"list", "--installed"}); err != nil {
		t.Fatalf("runModels(list --installed): %v", err)
	}
}

// The active marker follows the config to the entry that owns the directory
// on disk. fastconformer-streaming is the case that would break a naive
// implementation: its catalog name and its directory differ.
func TestActiveMarkerFindsTheEntryThatOwnsTheDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "cfg"))

	dir := filepath.Join(tmpDir, "mavor", "models", "sherpa", "parakeet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "encoder.onnx"), make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model = "fastconformer-streaming"

	buf := new(bytes.Buffer)
	if err := listCatalog(buf, cfg, true); err != nil {
		t.Fatalf("listCatalog: %v", err)
	}

	var starred string
	for _, l := range strings.Split(buf.String(), "\n") {
		// The legend at the foot of the table carries every marker; only
		// table rows start with a model name.
		if strings.Contains(l, markerActive) && !strings.HasPrefix(l, markerActive) {
			starred = l
		}
	}
	if starred == "" {
		t.Fatalf("no row carries the active marker:\n%s", buf.String())
	}
	if !strings.HasPrefix(starred, "fastconformer-streaming ") {
		t.Errorf("expected the fastconformer-streaming row to be starred, got: %s", starred)
	}
	if !strings.Contains(starred, markerDownloaded) {
		t.Errorf("the row is not marked downloaded, so the pinned TargetDir was not consulted: %s", starred)
	}
}

// A model the user converted or placed by hand is still a model they have.
// The catalog listing must not silently drop it.
func TestListingSurfacesModelsOutsideTheCatalog(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	custom := filepath.Join(tmpDir, "mavor", "models", "sherpa", "my-own-model")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(custom, "encoder.onnx"), make([]byte, 256), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	if err := listCatalog(buf, cfg, false); err != nil {
		t.Fatalf("listCatalog: %v", err)
	}
	if !strings.Contains(buf.String(), "my-own-model") {
		t.Errorf("listing dropped a cached model that is not in the catalog:\n%s", buf.String())
	}
}

// The help text used to be a hand-maintained prose list and had drifted from
// the catalog. It is generated now, so every advertised name must be pullable.
func TestCatalogSummaryNamesAreAllPullable(t *testing.T) {
	summary := models.Summary()
	for _, m := range models.Catalog {
		if !strings.Contains(summary, m.Name) {
			t.Errorf("catalog summary omits %q", m.Name)
		}
	}
	for _, line := range strings.Split(summary, "\n") {
		_, names, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		for _, n := range strings.Split(names, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if _, ok := models.Lookup(n); !ok {
				t.Errorf("summary advertises %q but `mavor models pull %s` would not find it", n, n)
			}
		}
	}
}

func TestVerboseListingShowsTheExtraProperties(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	if err := listCatalogVerbose(buf, cfg, false); err != nil {
		t.Fatalf("listCatalogVerbose: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"speed", "vocabulary", "gpu", "streaming", "languages", "source"} {
		if !strings.Contains(out, want) {
			t.Errorf("verbose listing missing the %q property", want)
		}
	}
	// Every model must appear, with its download URL so the source is checkable.
	for _, m := range models.Catalog {
		if !strings.Contains(out, m.Name) {
			t.Errorf("verbose listing omits %q", m.Name)
		}
		if !strings.Contains(out, m.URL) {
			t.Errorf("verbose listing omits the source URL for %q", m.Name)
		}
	}
}

// Speed is a relative ordering, not a measurement, except where it is backed
// by a benchmark — and then the listing must say so rather than implying every
// number was measured.
func TestVerboseDistinguishesMeasuredFromEstimatedSpeed(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	if err := listCatalogVerbose(buf, cfg, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, "measured") {
		t.Error("verbose listing never marks a measured figure")
	}
	if !strings.Contains(strings.ToLower(out), "relative") {
		t.Error("verbose listing must say the speed tiers are relative, not measured")
	}

	var measured int
	for _, m := range models.Catalog {
		if m.MeasuredRTF > 0 {
			measured++
		}
	}
	if measured == 0 {
		t.Fatal("no model carries a measured RTF; the distinction is untestable")
	}
	if measured == len(models.Catalog) {
		t.Error("every model claims a measured RTF, but only a few were benchmarked")
	}
}

func TestEveryModelDeclaresItsProperties(t *testing.T) {
	valid := map[string]bool{
		"very fast": true, "fast": true, "moderate": true, "slow": true, "very slow": true,
	}
	for _, m := range models.Catalog {
		if !valid[m.Speed] {
			t.Errorf("model %q has speed %q, not one of the defined tiers", m.Name, m.Speed)
		}
		if m.Vocabulary == "" {
			t.Errorf("model %q does not say whether it supports vocabulary biasing", m.Name)
		}
	}
}

// Vocabulary biasing in sherpa-onnx is a transducer feature; the CTC and
// encoder-decoder models cannot take hotwords however they are configured.
func TestOnlyTransducersClaimHotwordSupport(t *testing.T) {
	for _, m := range models.Catalog {
		claims := strings.Contains(m.Vocabulary, "hotwords")
		if claims && m.Engine != "sherpa" {
			t.Errorf("model %q claims hotwords, which only the sherpa engine supports", m.Name)
		}
		if claims && !m.Transducer {
			t.Errorf("model %q claims hotwords but is not a transducer", m.Name)
		}
		if !claims && m.Transducer && m.Engine == "sherpa" {
			t.Errorf("transducer %q should support hotwords but does not claim it", m.Name)
		}
	}
}

func TestModelsListAcceptsVerbose(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, args := range [][]string{
		{"list", "--verbose"},
		{"list", "-v"},
		{"list", "--verbose", "--installed"},
	} {
		if err := runModels(args); err != nil {
			t.Errorf("runModels(%v): %v", args, err)
		}
	}
}

func TestModelsListJSONCarriesEveryCatalogProperty(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	modelDir := filepath.Join(tmpDir, "mavor", "models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "ggml-base.en.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	if err := listCatalogJSON(buf, cfg, false); err != nil {
		t.Fatalf("listCatalogJSON: %v", err)
	}

	var got catalogJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if got.ModelDir == "" {
		t.Error("JSON output omits the model directory")
	}
	if len(got.Models) != len(models.Catalog) {
		t.Errorf("JSON lists %d models, want the whole catalog (%d)", len(got.Models), len(models.Catalog))
	}

	byName := map[string]catalogModelJSON{}
	for _, m := range got.Models {
		byName[m.Name] = m
	}

	base, ok := byName["whisper-base.en"]
	if !ok {
		t.Fatal("JSON output omits whisper-base.en")
	}
	if !base.Installed {
		t.Error("whisper-base.en is on disk but the JSON reports it as not installed")
	}
	if base.InstalledSize != 4096 {
		t.Errorf("whisper-base.en installed_size = %d, want 4096", base.InstalledSize)
	}
	if base.Engine != "whisper" || base.DownloadS == 0 || base.Languages == "" {
		t.Errorf("whisper-base.en is missing catalog properties: %+v", base)
	}
	// A consumer building a path needs the on-disk name, which is not the
	// catalog name.
	if base.Filename != "ggml-base.en.bin" {
		t.Errorf("whisper-base.en filename = %q, want ggml-base.en.bin", base.Filename)
	}

	// A model that is not downloaded is present in the catalog listing, with
	// installed false — the benchmark harness needs the difference.
	turbo := byName["whisper-large-v3-turbo"]
	if turbo.Installed {
		t.Error("whisper-large-v3-turbo is not on disk but the JSON reports it as installed")
	}
	if turbo.InstalledSize != 0 {
		t.Errorf("an uninstalled model reports installed_size %d, want 0", turbo.InstalledSize)
	}
}

func TestModelsListJSONSeparatesMeasuredFromEstimatedSpeed(t *testing.T) {
	// The distinction the fabricated reports collapsed: a relative tier is an
	// architectural guess, and a consumer must not be able to read it as a
	// benchmark.
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	if err := listCatalogJSON(buf, cfg, false); err != nil {
		t.Fatal(err)
	}
	var got catalogJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	sawEstimated, sawMeasured := false, false
	for _, m := range got.Models {
		if m.MeasuredRTF > 0 {
			sawMeasured = true
			if m.SpeedIsEst {
				t.Errorf("%s has a measured RTF but is flagged as estimated", m.Name)
			}
		} else {
			sawEstimated = true
			if !m.SpeedIsEst {
				t.Errorf("%s has no measured RTF but is not flagged as estimated", m.Name)
			}
		}
	}
	if !sawMeasured {
		t.Error("no model in the JSON carries a measured RTF")
	}
	if !sawEstimated {
		t.Error("no model in the JSON is flagged as having an estimated speed tier")
	}
}

func TestModelsListJSONInstalledOnlyNarrowsTheListing(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	modelDir := filepath.Join(tmpDir, "mavor", "models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "ggml-tiny.en.bin"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	if err := listCatalogJSON(buf, cfg, true); err != nil {
		t.Fatal(err)
	}
	var got catalogJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "whisper-tiny.en" {
		t.Errorf("--installed --json listed %d models, want only whisper-tiny.en", len(got.Models))
	}
}

func TestModelsListJSONAlwaysEmitsArraysNotNull(t *testing.T) {
	// A consumer iterating models or aliases should never have to handle
	// null. This is the difference between a harness that works and one that
	// panics on the first model with no alias.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	if err := listCatalogJSON(buf, cfg, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "null") {
		t.Errorf("JSON output contains null where an array was expected:\n%s", buf.String())
	}
}

func TestModelsListRejectsJSONWithVerbose(t *testing.T) {
	// They are two renderings of the same data; silently honouring one would
	// leave a script parsing prose.
	err := runModels([]string{"list", "--json", "--verbose"})
	if err == nil {
		t.Fatal("`models list --json --verbose` was accepted; want an error")
	}
	if !strings.Contains(err.Error(), "pick one") {
		t.Errorf("error %q does not explain that the flags are alternatives", err)
	}
}
