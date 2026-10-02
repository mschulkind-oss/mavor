package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

type previewLogSink chan string

func (s previewLogSink) Write(b []byte) (int, error) { s <- string(b); return len(b), nil }

func TestFinalCompanionDiagnostics(t *testing.T) {
	for _, failure := range []string{"start", "feed", "queue"} {
		t.Run(failure, func(t *testing.T) {
			logs := make(previewLogSink, 100)
			companion := speech.NewMockStreamTranscriber("preview only")
			var src speech.StreamTranscriber = companion
			var held *heldMainStream
			switch failure {
			case "start":
				companion.SetErrors(errors.New("broken start"), nil, nil)
			case "feed":
				companion.SetErrors(nil, errors.New("broken feed"), nil)
			case "queue":
				held = &heldMainStream{MockStreamTranscriber: companion, entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
				src = held
			}
			rec := &audio.MockRecorder{}
			chunks := make([][]byte, 40)
			for i := range chunks {
				chunks[i] = []byte{1, 0}
				if failure == "queue" {
					chunks[i] = make([]byte, 40000)
				}
			}
			rec.SetChunks(chunks...)
			if err := rec.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			d, _ := newTestDaemon(t, func(c *Config) {
				c.FinalMode = models.FinalStreaming
				c.Recorder = rec
				c.Transcriber = speech.NewMockStreamTranscriber("main final")
				c.PreviewEnabled = true
				c.PreviewMode = speech.PreviewCompanion
				c.PreviewCompanion = src
				c.Logger = slog.New(slog.NewTextHandler(logs, nil))
			})
			if err := d.startFinalCycle(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if held != nil {
					close(held.release)
				}
				d.cancelFinalCycle()
				d.awaitStreamDrain(context.Background())
			}()
			timeout := time.After(3 * time.Second)
			want := "preview: companion " + failure
			for {
				select {
				case line := <-logs:
					if strings.Contains(line, want) {
						return
					}
				case <-timeout:
					t.Fatalf("no diagnostic for companion %s failure", failure)
				}
			}
		})
	}
}
