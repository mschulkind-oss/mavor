package daemon

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/speech"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A live main result, including its last word, must be authoritative. A
// companion's convincing final result is never a candidate for emission.
func TestLiveMainFinalNoReplayAndTail(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "companion"}[preview], func(t *testing.T) {
			wav := filepath.Join(t.TempDir(), "capture.wav")
			if err := audio.WriteWAV(wav, []int16{1, 2, 3, 4, 5}, 16000); err != nil {
				t.Fatal(err)
			}
			main := speech.NewMockStreamTranscriber("authoritative last word")
			main.Mock.Text = "WRONG full replay"
			out := &output.Mock{}
			d, sock := newTestDaemon(t, func(c *Config) {
				c.FinalMode = models.FinalStreaming
				c.Recorder = &audio.MockRecorder{FixturePath: wav}
				c.Transcriber = main
				c.Output = out
				c.PreviewEnabled = preview
				c.PreviewMode = speech.PreviewCompanion
				c.PreviewCompanion = speech.NewMockStreamTranscriber("NEVER companion")
			})
			stop := runDaemon(t, d)
			defer stop()
			sendWithRetry(t, sock, "start")
			sendWithRetry(t, sock, "stop")
			waitForState(t, sock, "idle")
			if got := out.Calls()[0]; got != "authoritative last word" {
				t.Fatalf("got %q; live MAIN tail must emit without full replay", got)
			}
			if len(main.Chunks()) == 0 {
				t.Fatal("final WAV tail never fed")
			}
		})
	}
}

type heldMainStream struct {
	*speech.MockStreamTranscriber
	entered chan struct{}
	release chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (h *heldMainStream) FeedChunk(ctx context.Context, b []byte) (string, error) {
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return "held partial", nil
}
func (h *heldMainStream) StopStream(ctx context.Context) (string, error) {
	close(h.stopped)
	return h.MockStreamTranscriber.StopStream(ctx)
}
func TestLegacyMainPreviewFeedJoinsBeforeStop(t *testing.T) {
	rec := &audio.MockRecorder{}
	rec.SetChunks([]byte{1, 0})
	if e := rec.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	src := &heldMainStream{MockStreamTranscriber: speech.NewMockStreamTranscriber("tail"), entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	d, _ := newTestDaemon(t, func(c *Config) { c.Recorder = rec; c.Transcriber = src; c.PreviewMode = speech.PreviewMainModel })
	d.startStreamingMonitoring(t.Context())
	<-src.entered
	d.stopStreamingMonitoring()
	select {
	case <-src.stopped:
		close(src.release)
		t.Fatal("StopStream raced with an in-flight FeedChunk")
	case <-time.After(20 * time.Millisecond):
	}
	close(src.release)
	d.awaitStreamDrain(t.Context())
}

func TestFinalReleaseDoesNotCancelMainFeedOrEmitEarly(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "capture.wav")
	if e := audio.WriteWAV(wav, []int16{1, 2, 3}, 16000); e != nil {
		t.Fatal(e)
	}
	rec := &audio.MockRecorder{FixturePath: wav}
	rec.SetChunks([]byte{1, 0})
	main := &heldMainStream{MockStreamTranscriber: speech.NewMockStreamTranscriber("quiet last word"), entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	out := &output.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.FinalMode = models.FinalStreaming
		c.Recorder = rec
		c.Transcriber = main
		c.Output = out
		c.PreviewMode = speech.PreviewMainModel
		c.SilenceFilter = true
	})
	stop := runDaemon(t, d)
	defer stop()
	sendWithRetry(t, sock, "start")
	<-main.entered
	if len(out.Calls()) != 0 {
		t.Fatal("emitted while recording")
	}
	sendWithRetry(t, sock, "stop")
	if len(out.Calls()) != 0 {
		t.Fatal("emitted before real audio drained")
	}
	// Subsequent WAV tail feeds must not try to close entered twice.
	close(main.release)
	waitForState(t, sock, "idle")
	if got := out.Calls(); len(got) != 1 || got[0] != "quiet last word" {
		t.Fatalf("calls=%v", got)
	}
}

type noChunkRecorder struct{ audio.Recorder }

func TestFinalMissingChunkReaderCompleteReplay(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "capture.wav")
	if e := audio.WriteWAV(wav, []int16{1, 2, 3}, 16000); e != nil {
		t.Fatal(e)
	}
	main := speech.NewMockStreamTranscriber("not authoritative after failure")
	main.Mock.Text = "safe complete replay"
	out := &output.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.FinalMode = models.FinalStreaming
		c.Recorder = noChunkRecorder{&audio.MockRecorder{FixturePath: wav}}
		c.Transcriber = main
		c.Output = out
		c.PreviewEnabled = false
	})
	stop := runDaemon(t, d)
	defer stop()
	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")
	if got := out.Calls(); len(got) != 1 || got[0] != "safe complete replay" {
		t.Fatalf("calls=%v", got)
	}
}
func TestFinalCompanionFeedCannotGateMainResult(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "capture.wav")
	if e := audio.WriteWAV(wav, []int16{1, 2, 3}, 16000); e != nil {
		t.Fatal(e)
	}
	rec := &audio.MockRecorder{FixturePath: wav}
	rec.SetChunks([]byte{1, 0})
	companion := &heldMainStream{MockStreamTranscriber: speech.NewMockStreamTranscriber("never emitted"), entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	main := speech.NewMockStreamTranscriber("main once")
	out := &output.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.FinalMode = models.FinalStreaming
		c.Recorder = rec
		c.Transcriber = main
		c.Output = out
		c.PreviewMode = speech.PreviewCompanion
		c.PreviewCompanion = companion
	})
	stop := runDaemon(t, d)
	defer stop()
	sendWithRetry(t, sock, "start")
	<-companion.entered
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")
	if got := out.Calls(); len(got) != 1 || got[0] != "main once" {
		t.Fatalf("calls=%v", got)
	}
	close(companion.release)
}

type heldFinalStop struct {
	*speech.MockStreamTranscriber
	entered, release chan struct{}
}

func (h *heldFinalStop) StopStream(context.Context) (string, error) {
	close(h.entered)
	<-h.release
	return "late final never emitted", nil
}
func TestFinalShutdownNeverEmitsLateResult(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "capture.wav")
	if e := audio.WriteWAV(wav, []int16{1, 2, 3}, 16000); e != nil {
		t.Fatal(e)
	}
	main := &heldFinalStop{speech.NewMockStreamTranscriber("partial"), make(chan struct{}), make(chan struct{})}
	out := &output.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.FinalMode = models.FinalStreaming
		c.Recorder = &audio.MockRecorder{FixturePath: wav}
		c.Transcriber = main
		c.Output = out
		c.PreviewEnabled = false
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	<-main.entered
	cancel()
	close(main.release)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown leaked final worker")
	}
	if len(out.Calls()) != 0 {
		t.Fatal("late output escaped cancelled recording")
	}
	if _, e := os.Stat(wav); !os.IsNotExist(e) {
		t.Fatal("recording temp leaked")
	}
}

func TestFinalReplayPreservesEnabledEnergyRejection(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "quiet.wav")
	if e := audio.WriteWAV(wav, make([]int16, 16000), 16000); e != nil {
		t.Fatal(e)
	}
	main := speech.NewMockStreamTranscriber("unused")
	main.Mock.Text = "hallucinated replay"
	out := &output.Mock{}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.FinalMode = models.FinalStreaming
		c.Recorder = noChunkRecorder{&audio.MockRecorder{FixturePath: wav}}
		c.Transcriber = main
		c.Output = out
		c.PreviewEnabled = false
		c.SilenceFilter = true
	})
	stop := runDaemon(t, d)
	defer stop()
	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")
	if got := out.Calls(); len(got) != 0 {
		t.Fatalf("enabled energy filter emitted unrecognized quiet replay: %v", got)
	}
}

func TestFinalNewRecordingHasIndependentMainState(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "capture.wav")
	main := speech.NewMockStreamTranscriber("fresh authoritative text")
	out := &output.Mock{}
	rec := &audio.MockRecorder{FixturePath: wav}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.FinalMode = models.FinalStreaming
		c.Recorder = rec
		c.Transcriber = main
		c.Output = out
		c.PreviewMode = speech.PreviewMainModel
	})
	stop := runDaemon(t, d)
	defer stop()
	for _, samples := range [][]int16{{1, 2, 3}, {7, 8}} {
		if e := audio.WriteWAV(wav, samples, 16000); e != nil {
			t.Fatal(e)
		}
		sendWithRetry(t, sock, "start")
		sendWithRetry(t, sock, "stop")
		waitForState(t, sock, "idle")
		chunks := main.Chunks()
		var actual []byte
		for _, b := range chunks {
			actual = append(actual, b...)
		}
		want := make([]byte, len(samples)*2)
		for i, v := range samples {
			binary.LittleEndian.PutUint16(want[i*2:], uint16(v))
		}
		if !bytes.Equal(actual, want) {
			t.Fatalf("new-cycle audio contaminated: got %v want %v", actual, want)
		}
	}
	if len(out.Calls()) != 2 {
		t.Fatalf("calls=%v", out.Calls())
	}
}
