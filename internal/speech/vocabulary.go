package speech

// This file is the one place the runtime-neutral `[vocabulary]` table becomes
// something a runtime understands. Whisper takes an initial prompt; a
// transducer takes a hotwords file and beam search; every other architecture
// takes nothing at all. See docs/design/configuration-surface.md §7.

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// WhisperPromptTokenCap is how much of an initial prompt whisper.cpp keeps.
// It is upstream's limit, not mavor's: whisper's decoder is given at most 224
// tokens of text context ahead of the audio, and anything past that is
// dropped inside whisper.cpp with no diagnostic. mavor truncates first so the
// user is told.
const WhisperPromptTokenCap = 224

// promptCharsPerToken is how many characters of a phrase mavor charges to one
// token when measuring a prompt against WhisperPromptTokenCap.
//
// It is deliberately an under-estimate of whisper's real ratio, which runs
// nearer four characters per token on ordinary English. A vocabulary list is
// the opposite of ordinary English — it is exactly the rare names and jargon
// that whisper's BPE splits into several pieces each — so charging three
// characters per token over-counts, and over-counting truncates early rather
// than handing whisper a prompt it silently clips. mavor does not link
// whisper's tokenizer, so an approximation is the only measure available.
const promptCharsPerToken = 3

// Vocabulary is the resolved phrase list: `vocabulary.words` and the lines of
// `vocabulary.file` unioned, in that order, with duplicates dropped. It is
// runtime-neutral — what it becomes is decided by the model that will use it.
type Vocabulary struct {
	// Phrases is the biasing list, first occurrence order preserved.
	Phrases []string

	// Boost is the per-token score added while decoding whenever a
	// hypothesis extends a listed phrase. Transducer models only.
	Boost float32
}

// Empty reports a vocabulary with nothing to bias with. It is the common
// case, and it is not an error: an empty table means no prompt, no hotwords
// and greedy decoding (§10.1).
func (v Vocabulary) Empty() bool { return len(v.Phrases) == 0 }

// LoadVocabulary reads the `[vocabulary]` table into the phrase list mavor
// will bias with.
//
// `words` come first and the file's lines follow, because the config file is
// the more specific statement of the two; a phrase appearing in both is kept
// once, at its first position (§10.1). An unreadable file is a warning and
// not an error — the words alone are still worth having (§10.2).
func LoadVocabulary(cfg config.Config, logger *slog.Logger) Vocabulary {
	if logger == nil {
		logger = slog.Default()
	}

	boost := cfg.Vocabulary.Boost
	if boost <= 0 {
		boost = config.DefaultBoost
	}
	v := Vocabulary{Boost: boost}

	seen := make(map[string]bool)
	add := func(phrase string) {
		phrase = strings.TrimSpace(phrase)
		if phrase == "" || seen[phrase] {
			return
		}
		seen[phrase] = true
		v.Phrases = append(v.Phrases, phrase)
	}

	for _, w := range cfg.Vocabulary.Words {
		add(w)
	}

	if cfg.Vocabulary.File != "" {
		body, err := os.ReadFile(cfg.Vocabulary.File)
		if err != nil {
			logger.Warn("speech: vocabulary file unreadable; continuing with vocabulary.words alone",
				"file", cfg.Vocabulary.File, "words", len(v.Phrases), "err", err)
			return v
		}
		for _, line := range strings.Split(string(body), "\n") {
			add(strings.TrimSuffix(line, "\r"))
		}
	}
	return v
}

// promptTokens estimates how many prompt tokens a phrase costs, including the
// separator that joins it to the phrase before it. See promptCharsPerToken
// for why the estimate is high rather than accurate.
func promptTokens(phrase string) int {
	n := len([]rune(phrase))
	return (n+promptCharsPerToken-1)/promptCharsPerToken + 1
}

// CapPhrasesForPrompt returns as many leading phrases as fit inside a
// whisper prompt of at most limit tokens, and how many were dropped.
//
// Truncation is at a phrase boundary: half a name biases nothing and can bias
// the wrong thing, so a phrase that does not fit whole does not go in.
func CapPhrasesForPrompt(phrases []string, limit int) (kept []string, dropped int) {
	used := 0
	for i, p := range phrases {
		cost := promptTokens(p)
		if used+cost > limit {
			return phrases[:i], len(phrases) - i
		}
		used += cost
	}
	return phrases, 0
}

// WhisperPrompt renders the vocabulary as whisper.cpp's initial prompt, the
// `--prompt` string both whisper-cli and whisper-server accept.
//
// mavor passed no prompt at all before this, so an empty vocabulary must
// produce an empty string and no flag — a prompt of punctuation would bias
// the model towards punctuation.
func WhisperPrompt(v Vocabulary, logger *slog.Logger) string {
	if logger == nil {
		logger = slog.Default()
	}
	if v.Empty() {
		return ""
	}
	kept, dropped := CapPhrasesForPrompt(v.Phrases, WhisperPromptTokenCap)
	if dropped > 0 {
		logger.Warn("speech: vocabulary is longer than whisper's initial prompt holds; the tail was dropped",
			"dropped", dropped, "kept", len(kept), "token_cap", WhisperPromptTokenCap)
	}
	return strings.Join(kept, ", ")
}

// HotwordsPath is where mavor writes the hotwords file sherpa-onnx reads.
//
// It sits beside the daemon's socket, in the runtime directory: the file is
// derived from the config and regenerated at every daemon start, so it is
// state with the lifetime of a login session and belongs nowhere more
// permanent. Deriving it from Paths.Socket also means a test that redirects
// the socket redirects this too.
func HotwordsPath(cfg config.Config) string {
	socket := cfg.Paths.Socket
	if socket == "" {
		socket = config.DefaultSocket()
	}
	return filepath.Join(filepath.Dir(socket), "mavor-hotwords.txt")
}

// WriteHotwordsFile writes the vocabulary in the shape sherpa-onnx wants —
// one phrase per line — and returns the path it wrote.
//
// sherpa-onnx takes hotwords only as a file path; there is no in-memory list
// to hand it. A user who wrote `vocabulary.words` never asked for a file, so
// mavor makes one.
func WriteHotwordsFile(cfg config.Config, v Vocabulary) (string, error) {
	path := HotwordsPath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("speech: create hotwords directory %s: %w", filepath.Dir(path), err)
	}
	body := strings.Join(v.Phrases, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("speech: write hotwords file %s: %w", path, err)
	}
	return path, nil
}
