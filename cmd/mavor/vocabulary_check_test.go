package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeVocabConfig puts a config file where config.Load will find it and
// returns nothing: every assertion below is on what checkVocabulary says
// about it.
func writeVocabConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "mavor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mavor", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVocabularyCheckSaysNothingIsConfigured(t *testing.T) {
	writeVocabConfig(t, "model = \"whisper-base.en\"\n")

	ok, msg := checkVocabulary()
	if !ok {
		t.Errorf("an absent [vocabulary] failed the check: %s", msg)
	}
	if !strings.Contains(msg, "no [vocabulary] configured") {
		t.Errorf("msg = %q", msg)
	}
}

// §7: a whisper model takes the vocabulary as an initial prompt, and doctor
// says so — mavor passed none at all before, so this is the line that tells a
// user the gap is closed.
func TestVocabularyCheckReportsTheWhisperPrompt(t *testing.T) {
	writeVocabConfig(t, "model = \"whisper-base.en\"\n\n[vocabulary]\nwords = [\"mavor\", \"wlroots\"]\n")

	ok, msg := checkVocabulary()
	if !ok {
		t.Errorf("check failed: %s", msg)
	}
	if !strings.Contains(msg, "2 phrase(s)") || !strings.Contains(msg, "--prompt") {
		t.Errorf("msg = %q, want the phrase count and the whisper mechanism", msg)
	}
}

// A transducer is the one architecture sherpa-onnx can bias, and doctor names
// the file and the search it will use.
func TestVocabularyCheckReportsHotwordsForATransducer(t *testing.T) {
	writeVocabConfig(t, "model = \"parakeet-tdt-0.6b\"\n\n[vocabulary]\nwords = [\"mavor\"]\n")

	ok, msg := checkVocabulary()
	if !ok {
		t.Errorf("check failed: %s", msg)
	}
	if !strings.Contains(msg, "hotwords file") || !strings.Contains(msg, "beam search") {
		t.Errorf("msg = %q, want the hotwords mechanism", msg)
	}
}

// The case §7 is explicit about: a model that cannot be biased is REPORTED,
// not failed. sherpa-onnx boosts phrases inside transducer beam search and
// nowhere else, so the model transcribes exactly as it otherwise would — a
// fact worth saying out loud and not worth refusing to start over.
func TestVocabularyCheckReportsRatherThanFailsForANonTransducer(t *testing.T) {
	writeVocabConfig(t, "model = \"sensevoice-small\"\n\n[vocabulary]\nwords = [\"mavor\"]\n")

	ok, msg := checkVocabulary()
	if !ok {
		t.Errorf("a model that cannot use vocabulary failed the check; §7 says report: %s", msg)
	}
	if !strings.Contains(msg, "not a transducer") || !strings.Contains(msg, "ignored") {
		t.Errorf("msg = %q, want it said plainly that the phrases do nothing", msg)
	}
}

// §10.1: a boost above 5.0 is honored, and reported as likely to insert words
// that were not said.
func TestVocabularyCheckReportsAnExcessiveBoost(t *testing.T) {
	writeVocabConfig(t, "model = \"parakeet-tdt-0.6b\"\n\n[vocabulary]\nwords = [\"mavor\"]\nboost = 9.0\n")

	ok, msg := checkVocabulary()
	if !ok {
		t.Errorf("a high boost failed the check; §10.1 says honor it: %s", msg)
	}
	if !strings.Contains(msg, "insert listed words") {
		t.Errorf("msg = %q, want the insertion warning", msg)
	}
}

// A vocabulary.file that does not resolve is the one failure here: it is a
// typo rather than a decision, and the words alone still apply (§10.2).
func TestVocabularyCheckFailsOnAnUnreadableFile(t *testing.T) {
	writeVocabConfig(t, "model = \"whisper-base.en\"\n\n[vocabulary]\nwords = [\"mavor\"]\nfile = \"/nonexistent/vocabulary.txt\"\n")

	ok, msg := checkVocabulary()
	if ok {
		t.Errorf("an unreadable vocabulary.file passed the check: %s", msg)
	}
	if !strings.Contains(msg, "/nonexistent/vocabulary.txt") {
		t.Errorf("msg = %q, want the path named", msg)
	}
	if !strings.Contains(msg, "vocabulary.words alone") {
		t.Errorf("msg = %q, want it said the words still apply", msg)
	}
}
