package speech

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// The layouts below are the real ones, transcribed from the sherpa-onnx
// release artifacts the catalog downloads. Ten of the thirteen catalogued
// sherpa models could not be loaded at all; each case here is one of those
// failures, pinned so it cannot come back.
//
// Every expectation was verified against sherpa-onnx itself before being
// written down: each model was loaded with the configuration asserted here
// and made to transcribe test/fixtures/real_speech.wav. These are recorded
// observations, not inferences from the model names.

// writeLayout creates a model directory containing empty files with the given
// names. Detection reads the file layout, never the contents, so empty files
// are a faithful stand-in for a 600 MB model.
func writeLayout(t *testing.T, name string, files ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectRecognizesEveryCataloguedLayout(t *testing.T) {
	cases := []struct {
		name          string
		modelName     string
		files         []string
		wantType      SherpaModelType
		wantStreaming bool
	}{
		{
			name:      "parakeet-tdt-0.6b is an offline transducer",
			modelName: "parakeet-tdt-0.6b",
			files:     []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"},
			wantType:  ModelTypeTransducer,
		},
		{
			// The bug that started this: the name matched "parakeet" and the
			// detector called it a transducer even though the directory has
			// no joiner and only a single model.onnx. File layout has to win.
			name:      "parakeet-ctc is CTC despite the parakeet in its name",
			modelName: "parakeet-ctc",
			files:     []string{"model.onnx", "model.int8.onnx", "tokens.txt"},
			wantType:  ModelTypeNemoCTC,
		},
		{
			// Was detected as paraformer, which is not close: canary is an
			// attention encoder-decoder and needs its own config.
			name:      "canary is an encoder-decoder with no joiner",
			modelName: "canary-180m",
			files:     []string{"encoder.int8.onnx", "decoder.int8.onnx", "tokens.txt"},
			wantType:  ModelTypeCanary,
		},
		{
			// Was detected as NeMo CTC. The config.yaml plus tokens.json pair
			// is what distinguishes a real paraformer from the other models
			// that ship a lone model.onnx.
			name:      "paraformer is identified by its config.yaml and tokens.json",
			modelName: "paraformer",
			files:     []string{"model.onnx", "model.int8.onnx", "config.yaml", "tokens.json", "tokens.txt"},
			wantType:  ModelTypeParaformer,
		},
		{
			// words.txt is the zipformer CTC marker; without it this layout
			// is indistinguishable from a NeMo CTC model.
			name:      "zipformer-ctc is identified by words.txt",
			modelName: "zipformer-ctc",
			files:     []string{"model.onnx", "model.int8.onnx", "tokens.txt", "words.txt"},
			wantType:  ModelTypeZipformerCTC,
		},
		{
			// Epoch-suffixed filenames: the old exact-match findFile saw no
			// encoder, no decoder and no joiner here, so a transducer was
			// misread as a CTC model with a missing model.onnx.
			name:      "zipformer-offline uses epoch-suffixed filenames",
			modelName: "zipformer-offline",
			files: []string{
				"encoder-epoch-99-avg-1.onnx", "decoder-epoch-99-avg-1.onnx",
				"joiner-epoch-99-avg-1.onnx", "tokens.txt",
			},
			wantType: ModelTypeTransducer,
		},
		{
			name:      "zipformer-streaming is a streaming transducer",
			modelName: "zipformer-streaming",
			files: []string{
				"encoder-epoch-99-avg-1-chunk-16-left-128.onnx",
				"decoder-epoch-99-avg-1-chunk-16-left-128.onnx",
				"joiner-epoch-99-avg-1-chunk-16-left-128.onnx",
				"tokens.txt",
			},
			wantType:      ModelTypeTransducer,
			wantStreaming: true,
		},
		{
			name:      "moonshine is identified by its preprocessor",
			modelName: "moonshine-tiny",
			files: []string{
				"preprocess.onnx", "encode.int8.onnx",
				"uncached_decode.int8.onnx", "cached_decode.int8.onnx", "tokens.txt",
			},
			wantType: ModelTypeMoonshine,
		},
		{
			name:      "sensevoice is a lone model.onnx named sensevoice",
			modelName: "sensevoice-small",
			files:     []string{"model.onnx", "model.int8.onnx", "tokens.txt"},
			wantType:  ModelTypeSenseVoice,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeLayout(t, c.modelName, c.files...)
			got, err := DetectSherpaModel(dir, c.modelName)
			if err != nil {
				t.Fatalf("DetectModel: %v", err)
			}
			if got.Type != c.wantType {
				t.Errorf("type = %q, want %q", got.Type, c.wantType)
			}
			if got.Streaming != c.wantStreaming {
				t.Errorf("streaming = %v, want %v", got.Streaming, c.wantStreaming)
			}
		})
	}
}

func TestDetectPrefersFileLayoutOverTheModelName(t *testing.T) {
	// The general form of the parakeet-ctc bug. A directory that plainly
	// contains a transducer is a transducer whatever it is called, and a
	// directory with a lone model.onnx is not a transducer however
	// suggestive the name.
	dir := writeLayout(t, "parakeet-ctc",
		"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt")
	got, err := DetectSherpaModel(dir, "parakeet-ctc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != ModelTypeTransducer {
		t.Errorf("a directory with encoder+decoder+joiner detected as %q, want transducer", got.Type)
	}

	dir = writeLayout(t, "some-transducer", "model.onnx", "tokens.txt")
	got, err = DetectSherpaModel(dir, "my-transducer-model")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type == ModelTypeTransducer {
		t.Error("a directory with only model.onnx detected as a transducer because of its name")
	}
}

func TestFindFileMatchesGlobsAndPrefersExactNames(t *testing.T) {
	dir := writeLayout(t, "m", "encoder-epoch-99-avg-1.onnx", "encoder.int8.onnx")

	// An exact candidate listed first still wins, so existing layouts keep
	// resolving to exactly the file they always did.
	if got := findFile(dir, "encoder.int8.onnx", "encoder*.onnx"); filepath.Base(got) != "encoder.int8.onnx" {
		t.Errorf("findFile = %q, want the exact match encoder.int8.onnx", got)
	}
	// A glob reaches the epoch-suffixed name that no exact candidate can.
	if got := findFile(dir, "encoder-epoch*.onnx"); filepath.Base(got) != "encoder-epoch-99-avg-1.onnx" {
		t.Errorf("findFile with a glob = %q, want encoder-epoch-99-avg-1.onnx", got)
	}
	if got := findFile(dir, "nothing*.onnx"); got != "" {
		t.Errorf("findFile for an absent pattern = %q, want empty", got)
	}
}

func TestFindFileIsDeterministicAcrossMultipleGlobMatches(t *testing.T) {
	// Several files match; the same one must be chosen every run, or a
	// benchmark comparing two runs would silently be comparing two models.
	dir := writeLayout(t, "m", "encoder-b.onnx", "encoder-a.onnx", "encoder-c.onnx")
	first := findFile(dir, "encoder*.onnx")
	for i := 0; i < 8; i++ {
		if got := findFile(dir, "encoder*.onnx"); got != first {
			t.Fatalf("findFile returned %q then %q for the same directory", first, got)
		}
	}
	if filepath.Base(first) != "encoder-a.onnx" {
		t.Errorf("findFile chose %q; want the lexicographically first match", filepath.Base(first))
	}
}

func TestBuildOfflineConfigForCanaryPopulatesTheCanaryFields(t *testing.T) {
	dir := writeLayout(t, "canary-180m", "encoder.int8.onnx", "decoder.int8.onnx", "tokens.txt")
	sc, err := BuildSherpaOfflineConfig(config.Config{Model: dir})
	if err != nil {
		t.Fatalf("BuildSherpaOfflineConfig: %v", err)
	}
	if sc.ModelType != ModelTypeCanary {
		t.Fatalf("model type = %q, want canary", sc.ModelType)
	}
	if sc.Canary.Encoder == "" || sc.Canary.Decoder == "" {
		t.Errorf("canary config is empty: %+v", sc.Canary)
	}
	// Canary must not also be presented as a paraformer, which is what the
	// old detector did and what made it fail on lfr_window_size.
	if sc.Paraformer.Model != "" || sc.Paraformer.Encoder != "" {
		t.Errorf("canary model also populated the paraformer config: %+v", sc.Paraformer)
	}
}

func TestBuildOfflineConfigSetsOnlyTheMatchingCTCConfig(t *testing.T) {
	// The old code set NemoCTC and ZipformerCTC to the same file for either
	// type, leaving sherpa-onnx to guess which reader to use — and it guessed
	// NeMo for a zipformer model, which is where 'vocab_size does not exist'
	// came from.
	zip := writeLayout(t, "zipformer-ctc", "model.onnx", "tokens.txt", "words.txt")
	sc, err := BuildSherpaOfflineConfig(config.Config{Model: zip})
	if err != nil {
		t.Fatal(err)
	}
	if sc.ZipformerCTC.Model == "" {
		t.Error("zipformer CTC model did not populate the zipformer config")
	}
	if sc.NemoCTC.Model != "" {
		t.Errorf("zipformer CTC model also populated the NeMo CTC config (%q)", sc.NemoCTC.Model)
	}

	nemo := writeLayout(t, "parakeet-ctc", "model.onnx", "tokens.txt")
	sc, err = BuildSherpaOfflineConfig(config.Config{Model: nemo})
	if err != nil {
		t.Fatal(err)
	}
	if sc.NemoCTC.Model == "" {
		t.Error("NeMo CTC model did not populate the NeMo config")
	}
	if sc.ZipformerCTC.Model != "" {
		t.Errorf("NeMo CTC model also populated the zipformer config (%q)", sc.ZipformerCTC.Model)
	}
}

func TestBuildOnlineConfigFindsEpochSuffixedStreamingFiles(t *testing.T) {
	dir := writeLayout(t, "zipformer-streaming",
		"encoder-epoch-99-avg-1-chunk-16-left-128.onnx",
		"decoder-epoch-99-avg-1-chunk-16-left-128.onnx",
		"joiner-epoch-99-avg-1-chunk-16-left-128.onnx",
		"tokens.txt")

	sc, err := BuildSherpaOnlineConfig(config.Config{Model: dir})
	if err != nil {
		t.Fatalf("BuildSherpaOnlineConfig: %v", err)
	}
	if sc.Transducer.Encoder == "" || sc.Transducer.Decoder == "" || sc.Transducer.Joiner == "" {
		t.Errorf("streaming transducer files not found: %+v", sc.Transducer)
	}
}

func TestStreamingModelsAreRoutedToTheOnlineRecognizer(t *testing.T) {
	// BuildSherpaOnlineConfig and the online recognizer existed but nothing
	// ever called them, so no streaming model had ever been loaded. This is
	// the wiring that was missing.
	dir := writeLayout(t, "zipformer-streaming",
		"encoder-epoch-99-avg-1-chunk-16-left-128.onnx",
		"decoder-epoch-99-avg-1-chunk-16-left-128.onnx",
		"joiner-epoch-99-avg-1-chunk-16-left-128.onnx",
		"tokens.txt")

	info, err := DetectSherpaModel(dir, "zipformer-streaming")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Streaming {
		t.Fatal("a chunked streaming layout was not detected as streaming")
	}

	offlineDir := writeLayout(t, "zipformer-offline",
		"encoder-epoch-99-avg-1.onnx", "decoder-epoch-99-avg-1.onnx",
		"joiner-epoch-99-avg-1.onnx", "tokens.txt")
	info, err = DetectSherpaModel(offlineDir, "zipformer-offline")
	if err != nil {
		t.Fatal(err)
	}
	if info.Streaming {
		t.Error("a non-chunked transducer was detected as streaming")
	}
}

func TestDetectMatchesTheBaseNameNotTheWholePath(t *testing.T) {
	// Configuration accepts either a catalog name or a path, so name matching
	// sees paths. Matching the whole string lets any parent directory decide
	// the architecture — a perfectly ordinary offline model kept under
	// ~/streaming-models/ was routed to the streaming recognizer, which
	// aborts the process rather than failing politely.
	base := t.TempDir()
	dir := filepath.Join(base, "streaming-models", "zipformer-offline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"encoder-epoch-99-avg-1.onnx", "decoder-epoch-99-avg-1.onnx",
		"joiner-epoch-99-avg-1.onnx", "tokens.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	info, err := DetectSherpaModel(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Streaming {
		t.Error("an offline model was detected as streaming because a parent directory is named streaming-models")
	}

	// And the same for an architecture decided by name.
	svDir := filepath.Join(base, "zipformer-cache", "sensevoice-small")
	if err := os.MkdirAll(svDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"model.onnx", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(svDir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	info, err = DetectSherpaModel(svDir, svDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Type != ModelTypeSenseVoice {
		t.Errorf("type = %q, want sensevoice; a parent directory named zipformer-cache decided it", info.Type)
	}
}

func TestDetectRefusesADirectoryItCannotIdentify(t *testing.T) {
	// Better a clear error naming the directory than a guess that aborts the
	// process inside sherpa-onnx. There is no config key to point at any
	// more — describing a model by hand was five keys that all left the
	// config — so the message says what it looked for and where.
	dir := writeLayout(t, "mystery", "readme.txt")
	_, err := DetectSherpaModel(dir, "mystery")
	if err == nil {
		t.Fatal("an unidentifiable directory was accepted; want an error")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error %q does not name the directory it could not identify", err)
	}
	if !strings.Contains(err.Error(), "mavor models pull") {
		t.Errorf("error %q does not say how to get a model mavor can identify", err)
	}
	if strings.Contains(err.Error(), "sherpa_model_type") {
		t.Errorf("error %q still points at a config key that no longer exists", err)
	}
}

func TestStreamingDetectionIsNotFooledByNonStreaming(t *testing.T) {
	// parakeet-unified-en extracts alongside a directory named
	// sherpa-onnx-nemo-parakeet-unified-en-0.6b-int8-non-streaming. A
	// substring test for "streaming" matches "non-streaming" and routes an
	// offline model to the streaming recognizer, which then fails on a
	// missing window_size. The word that is present says the opposite of
	// what the naive match concludes.
	dir := writeLayout(t, "parakeet-unified-en",
		"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt",
		"sherpa-onnx-nemo-parakeet-unified-en-0.6b-int8-non-streaming/.keep")

	info, err := DetectSherpaModel(dir, "parakeet-unified-en")
	if err != nil {
		t.Fatal(err)
	}
	if info.Streaming {
		t.Error("a model whose artifact is named non-streaming was detected as streaming")
	}

	// The genuinely streaming sibling still resolves the other way.
	streamingDir := writeLayout(t, "parakeet",
		"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt",
		"sherpa-onnx-nemo-streaming-fast-conformer-transducer-en-80ms/.keep")
	info, err = DetectSherpaModel(streamingDir, "parakeet")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Streaming {
		t.Error("a model whose artifact is named streaming was not detected as streaming")
	}
}

func TestStreamingDetectionRejectsNonStreamingInTheModelName(t *testing.T) {
	dir := writeLayout(t, "my-non-streaming-model",
		"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt")
	info, err := DetectSherpaModel(dir, "my-non-streaming-model")
	if err != nil {
		t.Fatal(err)
	}
	if info.Streaming {
		t.Error("a model named non-streaming was detected as streaming")
	}
}
