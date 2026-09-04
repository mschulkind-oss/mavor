package speech

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestMockStreamTranscriberLifecycle(t *testing.T) {
	ctx := context.Background()
	st := NewMockStreamTranscriber("final transcript", "hel", "hello", "hello world")

	if st.IsStreaming() {
		t.Fatal("expected not streaming before StartStream")
	}

	if err := st.StartStream(ctx); err != nil {
		t.Fatalf("StartStream: %v", err)
	}

	if !st.IsStreaming() {
		t.Fatal("expected streaming after StartStream")
	}

	p1, err := st.FeedChunk(ctx, []byte{1, 2, 3})
	if err != nil || p1 != "hel" {
		t.Fatalf("FeedChunk 1: got (%q, %v), want (\"hel\", nil)", p1, err)
	}

	p2, err := st.FeedChunk(ctx, []byte{4, 5, 6})
	if err != nil || p2 != "hello" {
		t.Fatalf("FeedChunk 2: got (%q, %v), want (\"hello\", nil)", p2, err)
	}

	p3, err := st.FeedChunk(ctx, []byte{7, 8, 9})
	if err != nil || p3 != "hello world" {
		t.Fatalf("FeedChunk 3: got (%q, %v), want (\"hello world\", nil)", p3, err)
	}

	// Past canned index returns the last partial
	p4, err := st.FeedChunk(ctx, []byte{10})
	if err != nil || p4 != "hello world" {
		t.Fatalf("FeedChunk 4: got (%q, %v), want (\"hello world\", nil)", p4, err)
	}

	chunks := st.Chunks()
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d", len(chunks))
	}
	wantChunk1 := []byte{1, 2, 3}
	if !reflect.DeepEqual(chunks[0], wantChunk1) {
		t.Fatalf("chunk[0] = %v, want %v", chunks[0], wantChunk1)
	}

	final, err := st.StopStream(ctx)
	if err != nil || final != "final transcript" {
		t.Fatalf("StopStream: got (%q, %v), want (\"final transcript\", nil)", final, err)
	}

	if st.IsStreaming() {
		t.Fatal("expected not streaming after StopStream")
	}

	// Also verify Transcribe works as Transcriber
	res, err := st.Transcribe(ctx, "/path/to/audio.wav")
	if err != nil || res != "final transcript" {
		t.Fatalf("Transcribe: got (%q, %v), want (\"final transcript\", nil)", res, err)
	}
}

func TestMockStreamTranscriberCustomFeed(t *testing.T) {
	ctx := context.Background()
	st := NewMockStreamTranscriber("")
	st.SetFeedFn(func(ctx context.Context, chunk []byte) (string, error) {
		return string(chunk), nil
	})

	if err := st.StartStream(ctx); err != nil {
		t.Fatalf("StartStream: %v", err)
	}

	res, err := st.FeedChunk(ctx, []byte("custom token"))
	if err != nil || res != "custom token" {
		t.Fatalf("FeedChunk: got (%q, %v), want (\"custom token\", nil)", res, err)
	}
}

func TestMockStreamTranscriberErrors(t *testing.T) {
	ctx := context.Background()
	st := NewMockStreamTranscriber("test")
	errStart := errors.New("start failed")
	errFeed := errors.New("feed failed")
	errStop := errors.New("stop failed")
	st.SetErrors(errStart, errFeed, errStop)

	if err := st.StartStream(ctx); !errors.Is(err, errStart) {
		t.Fatalf("StartStream err = %v, want %v", err, errStart)
	}

	if _, err := st.FeedChunk(ctx, []byte{1}); !errors.Is(err, errFeed) {
		t.Fatalf("FeedChunk err = %v, want %v", err, errFeed)
	}

	if _, err := st.StopStream(ctx); !errors.Is(err, errStop) {
		t.Fatalf("StopStream err = %v, want %v", err, errStop)
	}
}
