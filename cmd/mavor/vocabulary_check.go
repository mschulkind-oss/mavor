package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// boostLikelyToInsert is where a hotwords boost stops helping. Upstream's
// default is 1.5 and the useful range runs to about 3.0; past 5.0 a listed
// phrase wins against the acoustics, and the model writes words that were not
// said. `doctor` reports it rather than clamping, because it is a legitimate
// thing to try — see docs/design/configuration-surface.md §10.1.
const boostLikelyToInsert = 5.0

// checkVocabulary reports what the `[vocabulary]` table will actually do to
// the configured model.
//
// It reports rather than fails. The one outcome a user would call a failure —
// a vocabulary configured against a model that cannot be biased — is
// deliberately not an error: sherpa-onnx implements contextual biasing inside
// transducer beam search and nowhere else, so a CTC, paraformer, moonshine or
// sensevoice model ignores the list and transcribes exactly as it would have
// (§7). Saying so is the whole job; refusing to start over it would be worse
// than the silence it replaces. An unreadable `vocabulary.file` is the only
// failure here, and it is one because a path that does not resolve is a typo
// rather than a decision.
func checkVocabulary() (bool, string) {
	cfg, _ := config.Load("")

	// A discarding logger: LoadVocabulary warns about an unreadable file, and
	// doctor reports that itself, in the line the user is reading.
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	vocab := speech.LoadVocabulary(cfg, quiet)

	fileBroken := ""
	if cfg.Vocabulary.File != "" {
		if _, err := os.ReadFile(cfg.Vocabulary.File); err != nil {
			fileBroken = fmt.Sprintf("vocabulary.file %s cannot be read (%v)", cfg.Vocabulary.File, err)
		}
	}

	if vocab.Empty() {
		if fileBroken != "" {
			return false, fileBroken + " and vocabulary.words is empty, so nothing biases this model"
		}
		return true, "no [vocabulary] configured — nothing is biased"
	}

	msg := fmt.Sprintf("%d phrase(s) → %s", len(vocab.Phrases), vocabularyMechanism(cfg, vocab))
	if vocab.Boost > boostLikelyToInsert {
		msg += fmt.Sprintf("; boost = %.1f is above %.1f, which is likely to insert listed words where they were not said",
			vocab.Boost, boostLikelyToInsert)
	}
	if fileBroken != "" {
		return false, fileBroken + "; proceeding with vocabulary.words alone — " + msg
	}
	return true, msg
}

// vocabularyMechanism names what this model does with a phrase list, in the
// terms of the §7 table.
func vocabularyMechanism(cfg config.Config, vocab speech.Vocabulary) string {
	if models.RuntimeFor(cfg.Model) == models.RuntimeWhisper {
		kept, dropped := speech.CapPhrasesForPrompt(vocab.Phrases, speech.WhisperPromptTokenCap)
		s := "whisper initial prompt (--prompt)"
		if dropped > 0 {
			s += fmt.Sprintf("; %d dropped — the list is longer than whisper's %d-token prompt holds, and %d phrase(s) fit",
				dropped, speech.WhisperPromptTokenCap, len(kept))
		}
		return s
	}

	known, inCatalog := models.Lookup(cfg.Model)
	switch {
	case !inCatalog:
		return fmt.Sprintf("a hotwords file at %s if %q is a transducer, and nothing otherwise — the model is not in the catalog, so mavor cannot say which until it loads it",
			speech.HotwordsPath(cfg), cfg.Model)
	case known.Transducer:
		return fmt.Sprintf("a hotwords file at %s, with boost %.1f, decoded by modified beam search",
			speech.HotwordsPath(cfg), vocab.Boost)
	default:
		return fmt.Sprintf("nothing: %q is not a transducer, and sherpa-onnx biases only inside transducer beam search, so the phrases are ignored",
			cfg.Model)
	}
}
