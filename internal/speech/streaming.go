// StreamTranscriber and the chunk-feeding protocol behind live token
// streaming, for engines that can emit text before the utterance ends.
package speech

import (
	"context"
	"sync"
)

// StreamTranscriber is an optional interface that Transcribers can implement
// to support live real-time token streaming and intermediate recognition updates
// while audio capture is actively running.
type StreamTranscriber interface {
	Transcriber

	// StartStream initializes a new streaming recognition session.
	StartStream(ctx context.Context) error

	// FeedChunk sends raw PCM audio bytes (16kHz mono s16le) to the active streaming recognizer
	// and returns the latest accumulated partial transcription text.
	FeedChunk(ctx context.Context, chunk []byte) (string, error)

	// StopStream concludes the active streaming session and returns any final accumulated text.
	StopStream(ctx context.Context) (string, error)
}

// MockStreamTranscriber is a test mock implementing StreamTranscriber.
type MockStreamTranscriber struct {
	Mock
	mu           sync.Mutex
	started      bool
	stopped      bool
	chunks       [][]byte
	partialTexts []string
	currentIdx   int
	feedErr      error
	startErr     error
	stopErr      error
	finalStream  string
	feedFn       func(ctx context.Context, chunk []byte) (string, error)
}

// NewMockStreamTranscriber creates a MockStreamTranscriber with optional canned partial texts.
func NewMockStreamTranscriber(finalText string, partials ...string) *MockStreamTranscriber {
	return &MockStreamTranscriber{
		Mock:         Mock{Text: finalText},
		partialTexts: partials,
		finalStream:  finalText,
	}
}

func (m *MockStreamTranscriber) StartStream(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startErr != nil {
		return m.startErr
	}
	m.started = true
	m.stopped = false
	m.chunks = nil
	m.currentIdx = 0
	return nil
}

func (m *MockStreamTranscriber) FeedChunk(ctx context.Context, chunk []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.feedErr != nil {
		return "", m.feedErr
	}
	cp := make([]byte, len(chunk))
	copy(cp, chunk)
	m.chunks = append(m.chunks, cp)

	if m.feedFn != nil {
		return m.feedFn(ctx, chunk)
	}

	if len(m.partialTexts) > 0 {
		if m.currentIdx < len(m.partialTexts) {
			txt := m.partialTexts[m.currentIdx]
			m.currentIdx++
			return txt, nil
		}
		return m.partialTexts[len(m.partialTexts)-1], nil
	}
	return "", nil
}

func (m *MockStreamTranscriber) StopStream(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopErr != nil {
		return "", m.stopErr
	}
	m.stopped = true
	m.started = false
	return m.finalStream, nil
}

func (m *MockStreamTranscriber) Chunks() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(m.chunks))
	for i, c := range m.chunks {
		cp := make([]byte, len(c))
		copy(cp, c)
		out[i] = cp
	}
	return out
}

func (m *MockStreamTranscriber) IsStreaming() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started && !m.stopped
}

func (m *MockStreamTranscriber) SetFeedFn(fn func(ctx context.Context, chunk []byte) (string, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.feedFn = fn
}

func (m *MockStreamTranscriber) SetErrors(startErr, feedErr, stopErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startErr = startErr
	m.feedErr = feedErr
	m.stopErr = stopErr
}
