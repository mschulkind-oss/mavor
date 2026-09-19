package speech

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/audio"
)

func TestStitchOverlappingTexts(t *testing.T) {
	tests := []struct {
		name string
		t1   string
		t2   string
		want string
	}{
		{
			name: "empty t1",
			t1:   "",
			t2:   "hello world",
			want: "hello world",
		},
		{
			name: "empty t2",
			t1:   "hello world",
			t2:   "",
			want: "hello world",
		},
		{
			name: "no overlap",
			t1:   "first sentence.",
			t2:   "second sentence.",
			want: "first sentence. second sentence.",
		},
		{
			name: "exact suffix prefix match",
			t1:   "We need to check how the ZWP virtual keyboard",
			t2:   "the ZWP virtual keyboard V1 interacts with the compositor.",
			want: "We need to check how the ZWP virtual keyboard V1 interacts with the compositor.",
		},
		{
			name: "match with punctuation differences",
			t1:   "I am stepping away from the desk now, but make sure",
			t2:   "desk now but make sure the JSONL history log records turns.",
			want: "I am stepping away from the desk now, but make sure the JSONL history log records turns.",
		},
		{
			name: "short single word overlap does not stitch falsely",
			t1:   "I saw a car.",
			t2:   "A cat is sleeping.",
			want: "I saw a car. A cat is sleeping.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StitchOverlappingTexts(tt.t1, tt.t2)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

type recordingTranscriber struct {
	mu    sync.Mutex
	calls []string
	reply string
}

func (r *recordingTranscriber) Transcribe(ctx context.Context, wavPath string) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, wavPath)
	r.mu.Unlock()
	return r.reply, nil
}

func TestChunkingTranscriber_ShortAudioUnchunked(t *testing.T) {
	tmpDir := t.TempDir()
	wav := filepath.Join(tmpDir, "short.wav")
	// 5 seconds of audio
	if err := audio.WriteWAV(wav, make([]int16, 5*audio.DefaultSampleRate), audio.DefaultSampleRate); err != nil {
		t.Fatal(err)
	}

	rec := &recordingTranscriber{reply: "short text"}
	ct := NewChunkingTranscriber(rec, "auto", nil)

	got, err := ct.Transcribe(context.Background(), wav)
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if got != "short text" {
		t.Fatalf("unexpected text: %q", got)
	}
	if len(rec.calls) != 1 || rec.calls[0] != wav {
		t.Fatalf("expected single call with original wav, got %v", rec.calls)
	}
}

func TestChunkingTranscriber_OffMode(t *testing.T) {
	tmpDir := t.TempDir()
	wav := filepath.Join(tmpDir, "long.wav")
	// 40 seconds of audio
	if err := audio.WriteWAV(wav, make([]int16, 40*audio.DefaultSampleRate), audio.DefaultSampleRate); err != nil {
		t.Fatal(err)
	}

	rec := &recordingTranscriber{reply: "raw text"}
	wrapped := WrapChunking(rec, "off", nil)

	got, err := wrapped.Transcribe(context.Background(), wav)
	if err != nil {
		t.Fatal(err)
	}
	if got != "raw text" {
		t.Fatalf("unexpected text: %q", got)
	}
	if len(rec.calls) != 1 || rec.calls[0] != wav {
		t.Fatalf("expected single call with original wav, got %v", rec.calls)
	}
}

func TestChunkingTranscriber_VADAndOverlap(t *testing.T) {
	tmpDir := t.TempDir()
	wav := filepath.Join(tmpDir, "long_speech.wav")

	// 40 seconds of audio with silence at 20s-22s
	samples := make([]int16, 40*audio.DefaultSampleRate)
	for i := range samples {
		sec := float64(i) / float64(audio.DefaultSampleRate)
		if sec < 20.0 || sec > 22.0 {
			samples[i] = 2000
		} else {
			samples[i] = 0
		}
	}
	if err := audio.WriteWAV(wav, samples, audio.DefaultSampleRate); err != nil {
		t.Fatal(err)
	}

	t.Run("vad mode", func(t *testing.T) {
		rec := &recordingTranscriber{reply: "chunk segment"}
		ct := NewChunkingTranscriber(rec, "vad", nil)
		got, err := ct.Transcribe(context.Background(), wav)
		if err != nil {
			t.Fatalf("Transcribe failed: %v", err)
		}
		if len(rec.calls) < 2 {
			t.Fatalf("expected at least 2 chunk calls, got %d", len(rec.calls))
		}
		if !strings.Contains(got, "chunk segment chunk segment") {
			t.Fatalf("unexpected joined text: %q", got)
		}
	})

	t.Run("overlap mode", func(t *testing.T) {
		var chunkIdx int
		chunkReplies := []string{
			"All right here is the first part of the virtual keyboard",
			"virtual keyboard interacts with the Wayland compositor",
		}
		fnRec := &functionalTranscriber{
			fn: func(ctx context.Context, p string) (string, error) {
				idx := chunkIdx
				chunkIdx++
				if idx < len(chunkReplies) {
					return chunkReplies[idx], nil
				}
				return "tail text", nil
			},
		}
		ct := NewChunkingTranscriber(fnRec, "overlap", nil)
		got, err := ct.Transcribe(context.Background(), wav)
		if err != nil {
			t.Fatalf("Transcribe failed: %v", err)
		}
		if !strings.Contains(got, "virtual keyboard interacts with") {
			t.Fatalf("expected stitched text, got %q", got)
		}
	})

	t.Run("auto mode", func(t *testing.T) {
		rec := &recordingTranscriber{reply: "auto segment"}
		ct := NewChunkingTranscriber(rec, "auto", nil)
		got, err := ct.Transcribe(context.Background(), wav)
		if err != nil {
			t.Fatalf("Transcribe failed: %v", err)
		}
		if len(rec.calls) < 2 {
			t.Fatalf("expected chunked calls, got %d", len(rec.calls))
		}
		if !strings.Contains(got, "auto segment auto segment") {
			t.Fatalf("unexpected joined text: %q", got)
		}
	})
}

type functionalTranscriber struct {
	fn func(ctx context.Context, wavPath string) (string, error)
}

func (f *functionalTranscriber) Transcribe(ctx context.Context, wavPath string) (string, error) {
	return f.fn(ctx, wavPath)
}

func TestChunkingTranscriber_PreservesStreamInterface(t *testing.T) {
	mockStream := NewMockStreamTranscriber("final transcript", "partial 1", "partial 2")
	wrapped := WrapChunking(mockStream, "auto", nil)

	st, ok := wrapped.(StreamTranscriber)
	if !ok {
		t.Fatalf("wrapped transcriber failed to implement StreamTranscriber")
	}

	if err := st.StartStream(context.Background()); err != nil {
		t.Fatalf("StartStream failed: %v", err)
	}
	txt, err := st.FeedChunk(context.Background(), make([]byte, 960))
	if err != nil {
		t.Fatalf("FeedChunk failed: %v", err)
	}
	if txt != "partial 1" {
		t.Fatalf("unexpected partial: %q", txt)
	}
	final, err := st.StopStream(context.Background())
	if err != nil {
		t.Fatalf("StopStream failed: %v", err)
	}
	if final != "final transcript" {
		t.Fatalf("unexpected final: %q", final)
	}
}

func TestChunkingTranscriber_ContextCancelled(t *testing.T) {
	tmpDir := t.TempDir()
	wav := filepath.Join(tmpDir, "long.wav")
	if err := audio.WriteWAV(wav, make([]int16, 40*audio.DefaultSampleRate), audio.DefaultSampleRate); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	rec := &recordingTranscriber{reply: "text"}
	ct := NewChunkingTranscriber(rec, "auto", nil)

	_, err := ct.Transcribe(ctx, wav)
	if err == nil {
		t.Fatalf("expected context cancelled error, got nil")
	}
}
