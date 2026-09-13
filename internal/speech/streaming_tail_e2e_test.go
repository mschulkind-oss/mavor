//go:build e2e

package speech

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
)

// Every streaming model in the catalog used to return this fixture's
// transcript with the last word missing — "...then Jeremy runs up the",
// dropping "path". A cache-aware streaming encoder will not emit the frames
// for its final chunk until it has that chunk's right-context lookahead, and
// the end of the recording never supplies one; InputFinished says no more
// audio is coming, which is not the same as supplying the lookahead the
// encoder is waiting on. feedTailPadding is what closes that gap.
//
// This is an e2e test rather than a unit test because there is nothing
// underneath it to fake: the behaviour lives in the ONNX encoder's chunking,
// so a stubbed recognizer would assert only that mavor calls a function it
// already calls. It runs against every streaming model that happens to be
// downloaded and skips the rest, so a machine with one model still gets the
// regression covered.
func TestStreamingModelsKeepTheLastWord(t *testing.T) {
	audio, err := filepath.Abs("../../test/fixtures/real_speech.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(audio); err != nil {
		t.Skipf("no audio fixture at %s", audio)
	}
	modelDir := config.DefaultModelDir()

	// The fixture ends "...then Jeremy runs up the patch". Models disagree
	// with the reference on that final word — most hear "path" — so the
	// assertion is on the word before it, which every model gets and which
	// the bug removed along with everything after it.
	const lastWordBeforeTheDisputedOne = "the"

	var ran int
	for _, m := range models.Catalog {
		if m.Engine != "sherpa" || !m.Streaming {
			continue
		}
		t.Run(m.Name, func(t *testing.T) {
			dir := filepath.Join(modelDir, "sherpa", m.TargetDir)
			if _, err := os.Stat(dir); err != nil {
				t.Skipf("%s is not downloaded (%s); run `mavor models pull %s`", m.Name, dir, m.Name)
			}
			ran++

			cfg := config.Default()
			cfg.Model = m.Name
			cfg.Paths.Models = modelDir

			tr, err := NewSherpaTranscriber(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatalf("build transcriber: %v", err)
			}
			defer tr.Close()

			ctx := context.Background()
			if err := tr.Start(ctx); err != nil {
				t.Fatalf("load model: %v", err)
			}
			text, err := tr.Transcribe(ctx, audio)
			if err != nil {
				t.Fatalf("transcribe: %v", err)
			}

			fields := strings.Fields(strings.ToLower(strings.TrimRight(strings.TrimSpace(text), ".")))
			if len(fields) == 0 {
				t.Fatalf("empty transcript")
			}
			// The trailing word is whichever one the model heard; what the
			// bug did was truncate before it. Requiring the fixture's
			// penultimate word to be present, and not to be the last thing
			// said, is the assertion that fails when the tail is dropped and
			// passes for every spelling of the final word.
			last := fields[len(fields)-1]
			if last == lastWordBeforeTheDisputedOne {
				t.Errorf("transcript stops at %q — the streaming tail was dropped:\n%s",
					lastWordBeforeTheDisputedOne, text)
			}
		})
	}
	if ran == 0 {
		t.Skip("no streaming models downloaded")
	}
}
