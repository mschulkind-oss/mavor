package speech

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
)

// captureLogger returns a logger writing into buf, so a test can assert on
// what a warning said rather than only that a code path was taken.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// An unconfigured [vocabulary] is the common case and must cost nothing: no
// prompt, no hotwords file, greedy decoding, and above all not an error
// (§10.1, first row).
func TestEmptyVocabularyIsNotAnError(t *testing.T) {
	var buf bytes.Buffer
	v := LoadVocabulary(config.Config{}, captureLogger(&buf))

	if !v.Empty() {
		t.Errorf("Phrases = %v, want none", v.Phrases)
	}
	if prompt := WhisperPrompt(v, captureLogger(&buf)); prompt != "" {
		t.Errorf("WhisperPrompt = %q, want empty", prompt)
	}
	if buf.Len() != 0 {
		t.Errorf("an empty vocabulary logged something: %s", buf.String())
	}
}

// §10.1: both `words` and `file` set is a union, `words` first, duplicates
// dropped keeping the first occurrence. The order is not cosmetic — it is
// what decides which phrases survive whisper's prompt cap.
func TestWordsAndFileUnionKeepsFirstOccurrence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vocabulary.txt")
	if err := os.WriteFile(path, []byte("wlroots\nSchulkind\n\n  mavor  \nniri\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Vocabulary: config.Vocabulary{
		Words: []string{"mavor", "sherpa-onnx", "mavor"},
		File:  path,
	}}
	v := LoadVocabulary(cfg, captureLogger(&bytes.Buffer{}))

	want := []string{"mavor", "sherpa-onnx", "wlroots", "Schulkind", "niri"}
	if len(v.Phrases) != len(want) {
		t.Fatalf("Phrases = %v, want %v", v.Phrases, want)
	}
	for i := range want {
		if v.Phrases[i] != want[i] {
			t.Errorf("Phrases[%d] = %q, want %q (full: %v)", i, v.Phrases[i], want[i], v.Phrases)
		}
	}
}

// §10.2: an unreadable vocabulary.file warns and proceeds with `words` alone.
// Losing the file must not lose the words, and must not stop the daemon.
func TestUnreadableVocabularyFileWarnsAndKeepsWords(t *testing.T) {
	var buf bytes.Buffer
	cfg := config.Config{Vocabulary: config.Vocabulary{
		Words: []string{"mavor", "wlroots"},
		File:  filepath.Join(t.TempDir(), "does-not-exist.txt"),
	}}

	v := LoadVocabulary(cfg, captureLogger(&buf))

	if len(v.Phrases) != 2 || v.Phrases[0] != "mavor" {
		t.Errorf("Phrases = %v, want the two words alone", v.Phrases)
	}
	logged := buf.String()
	if !strings.Contains(logged, "vocabulary file unreadable") {
		t.Errorf("no warning about the unreadable file; logged: %s", logged)
	}
	if !strings.Contains(logged, "does-not-exist.txt") {
		t.Errorf("the warning does not name the file; logged: %s", logged)
	}
}

// §10.1: a vocabulary longer than whisper's 224-token prompt is truncated at
// a phrase boundary, and warned about once, naming the count dropped.
//
// The boundary is the point: half a name biases nothing, and whisper drops
// the overflow itself with no diagnostic at all.
func TestVocabularyOverPromptCapTruncatesAtPhraseBoundary(t *testing.T) {
	var phrases []string
	for i := 0; i < 400; i++ {
		phrases = append(phrases, "phrase")
	}
	// Each is distinct, or the union would collapse them.
	for i := range phrases {
		phrases[i] = phrases[i] + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}

	var buf bytes.Buffer
	v := LoadVocabulary(config.Config{Vocabulary: config.Vocabulary{Words: phrases}}, captureLogger(&buf))
	prompt := WhisperPrompt(v, captureLogger(&buf))

	kept, dropped := CapPhrasesForPrompt(v.Phrases, WhisperPromptTokenCap)
	if dropped == 0 {
		t.Fatalf("%d phrases were expected to overflow a %d-token prompt", len(v.Phrases), WhisperPromptTokenCap)
	}
	if len(kept)+dropped != len(v.Phrases) {
		t.Errorf("kept %d + dropped %d != %d phrases", len(kept), dropped, len(v.Phrases))
	}

	// Truncation at a phrase boundary: every phrase in the prompt is whole,
	// and the prompt ends with the last one it kept.
	if !strings.HasSuffix(prompt, kept[len(kept)-1]) {
		t.Errorf("prompt does not end on a whole phrase: %q", prompt)
	}
	if got := strings.Count(prompt, ", ") + 1; got != len(kept) {
		t.Errorf("prompt holds %d phrases, want %d", got, len(kept))
	}

	logged := buf.String()
	if !strings.Contains(logged, "dropped="+strconv.Itoa(dropped)) {
		t.Errorf("the warning does not name the dropped count %d; logged: %s", dropped, logged)
	}
}

// A vocabulary that fits is not truncated and raises no warning: the warning
// has to mean something when it appears.
func TestVocabularyUnderPromptCapIsNotTruncated(t *testing.T) {
	var buf bytes.Buffer
	v := Vocabulary{Phrases: []string{"mavor", "wlroots", "Schulkind"}}
	prompt := WhisperPrompt(v, captureLogger(&buf))

	if prompt != "mavor, wlroots, Schulkind" {
		t.Errorf("prompt = %q", prompt)
	}
	if strings.Contains(buf.String(), "dropped") {
		t.Errorf("a vocabulary that fits warned anyway: %s", buf.String())
	}
}

// boost is 1.5 when vocabulary is configured and boost is not: upstream's
// default, and the bottom of the useful range. Both the loader and
// config.Resolve have to agree on it, because either can be the one a caller
// went through.
func TestBoostDefaultsToUpstreamDefault(t *testing.T) {
	cfg := config.Config{Vocabulary: config.Vocabulary{Words: []string{"mavor"}}}

	if got := LoadVocabulary(cfg, captureLogger(&bytes.Buffer{})).Boost; got != 1.5 {
		t.Errorf("LoadVocabulary boost = %v, want 1.5", got)
	}

	cfg.Resolve()
	if cfg.Vocabulary.Boost != 1.5 {
		t.Errorf("Resolve boost = %v, want 1.5", cfg.Vocabulary.Boost)
	}
	if got := LoadVocabulary(cfg, captureLogger(&bytes.Buffer{})).Boost; got != 1.5 {
		t.Errorf("boost after Resolve = %v, want 1.5", got)
	}
}

// An explicit boost is honored rather than replaced by the default, including
// one above the useful range — `doctor` reports that, the loader does not
// second-guess it (§10.1).
func TestExplicitBoostIsHonored(t *testing.T) {
	cfg := config.Config{Vocabulary: config.Vocabulary{Words: []string{"mavor"}, Boost: 6}}
	if got := LoadVocabulary(cfg, captureLogger(&bytes.Buffer{})).Boost; got != 6 {
		t.Errorf("boost = %v, want 6", got)
	}
}

// The hotwords file is written where the daemon's runtime state lives, beside
// its socket, in the shape sherpa-onnx reads: one phrase per line.
func TestWriteHotwordsFileIsOnePhrasePerLine(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := config.Config{
		Paths:      config.Paths{Socket: filepath.Join(runtimeDir, "mavor.sock")},
		Vocabulary: config.Vocabulary{Words: []string{"mavor", "wlroots"}},
	}

	path, err := WriteHotwordsFile(cfg, LoadVocabulary(cfg, captureLogger(&bytes.Buffer{})))
	if err != nil {
		t.Fatalf("WriteHotwordsFile: %v", err)
	}
	if want := HotwordsPath(cfg); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if filepath.Dir(path) != runtimeDir {
		t.Errorf("hotwords file %q is not beside the socket in %q", path, runtimeDir)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "mavor\nwlroots\n" {
		t.Errorf("hotwords file = %q, want one phrase per line", body)
	}
}

// ---- decoding follows from vocabulary, per model kind ----------------------

// transducerDir builds the encoder/decoder/joiner triple that identifies an
// offline transducer, which is the one architecture sherpa-onnx can bias.
func transducerDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "zipformer-offline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func vocabConfig(model, runtimeDir string, words ...string) config.Config {
	return config.Config{
		Model:      model,
		Paths:      config.Paths{Socket: filepath.Join(runtimeDir, "mavor.sock")},
		Vocabulary: config.Vocabulary{Words: words},
	}
}

// A transducer with a vocabulary decodes with modified beam search, because
// that is the only search sherpa-onnx applies hotwords in. The user did not
// ask for beam search and cannot: configuring vocabulary is the request.
func TestTransducerWithVocabularySelectsBeamSearch(t *testing.T) {
	runtimeDir := t.TempDir()
	cfg := vocabConfig(transducerDir(t), runtimeDir, "mavor", "wlroots")

	sc, err := BuildSherpaOfflineConfig(cfg)
	if err != nil {
		t.Fatalf("BuildSherpaOfflineConfig: %v", err)
	}
	if sc.DecodingMethod != "modified_beam_search" {
		t.Errorf("DecodingMethod = %q, want modified_beam_search", sc.DecodingMethod)
	}
	if sc.HotwordsFile != HotwordsPath(cfg) {
		t.Errorf("HotwordsFile = %q, want %q", sc.HotwordsFile, HotwordsPath(cfg))
	}
	if sc.HotwordsScore != 1.5 {
		t.Errorf("HotwordsScore = %v, want the 1.5 default", sc.HotwordsScore)
	}
	// A beam of zero paths decodes nothing; the C API takes the number as
	// given, so beam search must come with a width.
	if sc.MaxActivePaths <= 0 {
		t.Errorf("MaxActivePaths = %d under beam search", sc.MaxActivePaths)
	}
	body, err := os.ReadFile(sc.HotwordsFile)
	if err != nil {
		t.Fatalf("the hotwords file sherpa was pointed at is not readable: %v", err)
	}
	if !strings.Contains(string(body), "wlroots") {
		t.Errorf("hotwords file = %q, want the configured phrases", body)
	}
}

// The same transducer with no vocabulary stays greedy. Beam search buys 0.02
// points of word error rate for several times the decoder work; hotwords are
// the only reason to pay for it.
func TestTransducerWithoutVocabularyStaysGreedy(t *testing.T) {
	cfg := config.Config{Model: transducerDir(t), Paths: config.Paths{Socket: filepath.Join(t.TempDir(), "mavor.sock")}}

	sc, err := BuildSherpaOfflineConfig(cfg)
	if err != nil {
		t.Fatalf("BuildSherpaOfflineConfig: %v", err)
	}
	if sc.DecodingMethod != "greedy_search" {
		t.Errorf("DecodingMethod = %q, want greedy_search", sc.DecodingMethod)
	}
	if sc.HotwordsFile != "" {
		t.Errorf("HotwordsFile = %q, want none", sc.HotwordsFile)
	}
	if _, err := os.Stat(HotwordsPath(cfg)); err == nil {
		t.Errorf("a hotwords file was written for a config with no vocabulary: %s", HotwordsPath(cfg))
	}
}

// sherpa-onnx implements biasing inside transducer beam search and nowhere
// else, so a CTC, moonshine or sensevoice model with a vocabulary configured
// decodes greedily and is not an error. §7 is explicit: doctor reports this,
// the daemon does not fail on it.
func TestNonTransducerWithVocabularyStaysGreedyWithoutError(t *testing.T) {
	base := t.TempDir()
	layouts := map[string][]string{
		"zipformer-ctc":    {"model.onnx", "tokens.txt", "words.txt"},
		"moonshine-tiny":   {"preprocess.onnx", "encode.onnx", "merged_decoder.onnx", "tokens.txt"},
		"sensevoice-small": {"model.onnx", "tokens.txt"},
	}

	for name, files := range layouts {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(base, name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cfg := vocabConfig(dir, t.TempDir(), "mavor", "wlroots")

			sc, err := BuildSherpaOfflineConfig(cfg)
			if err != nil {
				t.Fatalf("a vocabulary on a %s model was an error: %v", name, err)
			}
			if sc.ModelType == ModelTypeTransducer {
				t.Fatalf("test layout for %s was detected as a transducer", name)
			}
			if sc.DecodingMethod != "greedy_search" {
				t.Errorf("DecodingMethod = %q, want greedy_search", sc.DecodingMethod)
			}
			if sc.HotwordsFile != "" {
				t.Errorf("HotwordsFile = %q, want none — %s cannot use one", sc.HotwordsFile, name)
			}
		})
	}
}

// ---- whisper takes the same table as an initial prompt ---------------------

// whisperVocabConfig is whisperConfig with a vocabulary and the subprocess
// placement, which is the one placement that yields a command line a test can
// read without starting a child.
func whisperVocabConfig(t *testing.T, words ...string) config.Config {
	t.Helper()
	cfg := whisperConfig(t, "whisper-tiny.en")
	cfg.Advanced.Placement = "subprocess"
	cfg.Paths.Socket = filepath.Join(t.TempDir(), "mavor.sock")
	cfg.Vocabulary = config.Vocabulary{Words: words}
	return cfg
}

func whisperArgv(t *testing.T, cfg config.Config) []string {
	t.Helper()
	tr, err := Factory(cfg, captureLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	cli, ok := tr.(*WhisperCli)
	if !ok {
		t.Fatalf("Factory returned %T, want *WhisperCli", tr)
	}
	return cli.command(t.Context(), "/tmp/audio.wav").Args
}

// The gap this closes: mavor passed whisper no prompt at all, so a whisper
// user's [vocabulary] did nothing. It now reaches whisper-cli as --prompt.
func TestWhisperGetsPromptWithThePhrases(t *testing.T) {
	argv := whisperArgv(t, whisperVocabConfig(t, "mavor", "wlroots", "Schulkind"))

	i := indexOf(argv, "--prompt")
	if i < 0 {
		t.Fatalf("no --prompt in argv: %v", argv)
	}
	if i+1 >= len(argv) {
		t.Fatalf("--prompt has no value: %v", argv)
	}
	if got := argv[i+1]; got != "mavor, wlroots, Schulkind" {
		t.Errorf("--prompt = %q, want the phrases", got)
	}
}

// With nothing configured there must be no --prompt flag at all, not an empty
// one: an empty initial prompt is still a prompt, and whisper.cpp's behavior
// with one is not the behavior mavor shipped before this.
func TestWhisperWithNoVocabularyGetsNoPromptFlag(t *testing.T) {
	argv := whisperArgv(t, whisperVocabConfig(t))

	for _, a := range argv {
		if a == "--prompt" {
			t.Fatalf("--prompt was passed for a config with no vocabulary: %v", argv)
		}
	}
}

// The supervised whisper-server takes the same prompt as a flag, so the warm
// path and the per-utterance path bias identically.
func TestSupervisedServerCarriesThePrompt(t *testing.T) {
	cfg := whisperVocabConfig(t, "mavor", "wlroots")
	cfg.Advanced.Placement = "auto"

	tr, err := Factory(cfg, captureLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	st, ok := tr.(*ServerTranscriber)
	if !ok {
		t.Fatalf("Factory returned %T, want *ServerTranscriber", tr)
	}
	if st.Prompt != "mavor, wlroots" {
		t.Errorf("ServerTranscriber.Prompt = %q", st.Prompt)
	}
	if st.Supervisor == nil {
		t.Fatal("no supervisor for the local-server placement")
	}
	argv := DefaultServerCommand(t.Context(), st.Supervisor.cfg).Args
	i := indexOf(argv, "--prompt")
	if i < 0 || argv[i+1] != "mavor, wlroots" {
		t.Errorf("whisper-server argv does not carry the prompt: %v", argv)
	}
}

// The whole path, through the factory the daemon actually calls: a sherpa
// transducer with a vocabulary comes back biased, and comes back as a sherpa
// transcriber — the two mechanisms are alternatives, not a pair, and no
// whisper prompt is built for a model that runs on ONNX Runtime.
func TestFactoryBiasesASherpaTransducer(t *testing.T) {
	cfg := vocabConfig(transducerDir(t), t.TempDir(), "mavor", "wlroots")
	if models.RuntimeFor(cfg.Model) == models.RuntimeWhisper {
		t.Fatal("a directory path resolved to the whisper runtime")
	}

	tr, err := Factory(cfg, captureLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	st, ok := tr.(*SherpaTranscriber)
	if !ok {
		t.Fatalf("Factory returned %T, want *SherpaTranscriber", tr)
	}
	if st.SherpaConfig.DecodingMethod != "modified_beam_search" {
		t.Errorf("DecodingMethod = %q, want modified_beam_search", st.SherpaConfig.DecodingMethod)
	}
	if st.SherpaConfig.HotwordsFile == "" {
		t.Error("no hotwords file reached the recognizer configuration")
	}
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}
