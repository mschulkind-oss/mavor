package speech

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/output"
)

const (
	// DefaultMaxChunkDuration is the threshold above which Whisper's 30-second
	// internal window risk dropping or hallucinating speech. Audio longer than
	// this is segmented.
	DefaultMaxChunkDuration = 28 * time.Second

	// DefaultMinChunkDuration is the earliest a silence split point will be
	// accepted when segmenting.
	DefaultMinChunkDuration = 15 * time.Second

	// DefaultOverlapDuration is the window overlap used in sliding window mode.
	DefaultOverlapDuration = 4 * time.Second

	// DefaultSlidingWindowDuration is the chunk window size in sliding window mode.
	DefaultSlidingWindowDuration = 24 * time.Second
)

var wordPunctuationRegex = regexp.MustCompile(`[^\w]`)

func cleanWordToken(w string) string {
	return wordPunctuationRegex.ReplaceAllString(strings.ToLower(w), "")
}

// StitchOverlappingTexts combines two overlapping transcription segments,
// identifying matching word sequences across the boundary and deduplicating them.
// If no matching token sequence (>= 2 words) is found, it joins them with a space.
func StitchOverlappingTexts(t1, t2 string) string {
	t1 = strings.TrimSpace(t1)
	t2 = strings.TrimSpace(t2)
	if t1 == "" {
		return t2
	}
	if t2 == "" {
		return t1
	}

	w1 := strings.Fields(t1)
	w2 := strings.Fields(t2)
	if len(w1) == 0 {
		return t2
	}
	if len(w2) == 0 {
		return t1
	}

	c1 := make([]string, len(w1))
	for i, w := range w1 {
		c1[i] = cleanWordToken(w)
	}
	c2 := make([]string, len(w2))
	for i, w := range w2 {
		c2[i] = cleanWordToken(w)
	}

	bestOverlap := 0
	bestI1 := len(w1)
	bestI2 := 0

	maxK := len(c1)
	if len(c2) < maxK {
		maxK = len(c2)
	}
	if maxK > 25 {
		maxK = 25
	}

	for k := maxK; k >= 2; k-- {
		suffix := c1[len(c1)-k:]
		maxStart2 := 12
		if len(c2)-k < maxStart2 {
			maxStart2 = len(c2) - k
		}
		for start2 := 0; start2 <= maxStart2; start2++ {
			matched := true
			for m := 0; m < k; m++ {
				if c2[start2+m] != suffix[m] {
					matched = false
					break
				}
			}
			if matched {
				bestOverlap = k
				bestI1 = len(w1) - k
				bestI2 = start2 + k
				break
			}
		}
		if bestOverlap > 0 {
			break
		}
	}

	if bestOverlap > 0 {
		// Keep w1 up to the end of matched suffix, then append w2 after the match.
		kept1 := strings.Join(w1[:bestI1+bestOverlap], " ")
		if bestI2 < len(w2) {
			tail2 := strings.Join(w2[bestI2:], " ")
			return kept1 + " " + tail2
		}
		return kept1
	}

	return t1 + " " + t2
}

// ChunkingTranscriber wraps a Transcriber and segments audio longer than
// DefaultMaxChunkDuration to avoid Whisper's 30-second window truncation.
type ChunkingTranscriber struct {
	wrapped  Transcriber
	mode     string
	logger   *slog.Logger
	maxChunk time.Duration
	minChunk time.Duration
	window   time.Duration
	overlap  time.Duration
}

// WrapChunking wraps a Transcriber with chunking if mode is not "off".
func WrapChunking(wrapped Transcriber, mode string, logger *slog.Logger) Transcriber {
	if mode == "off" {
		return wrapped
	}
	if mode == "" {
		mode = "auto"
	}
	return NewChunkingTranscriber(wrapped, mode, logger)
}

// NewChunkingTranscriber creates a new ChunkingTranscriber.
func NewChunkingTranscriber(wrapped Transcriber, mode string, logger *slog.Logger) Transcriber {
	if logger == nil {
		logger = slog.Default()
	}
	if mode == "" {
		mode = "auto"
	}
	ct := &ChunkingTranscriber{
		wrapped:  wrapped,
		mode:     strings.ToLower(mode),
		logger:   logger,
		maxChunk: DefaultMaxChunkDuration,
		minChunk: DefaultMinChunkDuration,
		window:   DefaultSlidingWindowDuration,
		overlap:  DefaultOverlapDuration,
	}
	if st, ok := wrapped.(StreamTranscriber); ok {
		return &chunkingStreamTranscriber{
			ChunkingTranscriber: ct,
			stream:              st,
		}
	}
	return ct
}

type chunkingStreamTranscriber struct {
	*ChunkingTranscriber
	stream StreamTranscriber
}

func (c *chunkingStreamTranscriber) StartStream(ctx context.Context) error {
	return c.stream.StartStream(ctx)
}

func (c *chunkingStreamTranscriber) FeedChunk(ctx context.Context, chunk []byte) (string, error) {
	return c.stream.FeedChunk(ctx, chunk)
}

func (c *chunkingStreamTranscriber) StopStream(ctx context.Context) (string, error) {
	return c.stream.StopStream(ctx)
}

func (c *ChunkingTranscriber) Transcribe(ctx context.Context, wavPath string) (string, error) {
	dur, err := audio.AudioDuration(wavPath)
	if err != nil {
		c.logger.Warn("speech: chunker could not determine audio duration, running unchunked", "err", err)
		return c.wrapped.Transcribe(ctx, wavPath)
	}

	if dur <= c.maxChunk || c.mode == "off" {
		return c.wrapped.Transcribe(ctx, wavPath)
	}

	c.logger.Info("speech: audio exceeds chunk threshold — segmenting",
		"duration", dur, "max_chunk", c.maxChunk, "mode", c.mode)

	switch c.mode {
	case "overlap":
		return c.transcribeOverlap(ctx, wavPath, dur)
	case "vad":
		return c.transcribeVAD(ctx, wavPath, dur)
	default: // "auto", "hybrid"
		return c.transcribeAuto(ctx, wavPath, dur)
	}
}

func (c *ChunkingTranscriber) transcribeVAD(ctx context.Context, wavPath string, dur time.Duration) (string, error) {
	splits, err := audio.FindSilenceSplitPoints(wavPath, c.minChunk, c.maxChunk, 0)
	if err != nil || len(splits) == 0 {
		c.logger.Warn("speech: VAD split points not found, falling back to unchunked", "err", err)
		return c.wrapped.Transcribe(ctx, wavPath)
	}

	slices := audio.SilenceSlices(dur, splits)
	return c.transcribeSlicesAndJoin(ctx, wavPath, slices)
}

func (c *ChunkingTranscriber) transcribeOverlap(ctx context.Context, wavPath string, dur time.Duration) (string, error) {
	slices := audio.OverlapSlices(dur, c.window, c.overlap)
	texts, err := c.transcribeSlices(ctx, wavPath, slices)
	if err != nil {
		return "", err
	}
	if len(texts) == 0 {
		return "", nil
	}

	stitched := texts[0]
	for _, nxt := range texts[1:] {
		stitched = StitchOverlappingTexts(stitched, nxt)
	}
	return output.CleanText(stitched), nil
}

func (c *ChunkingTranscriber) transcribeAuto(ctx context.Context, wavPath string, dur time.Duration) (string, error) {
	splits, err := audio.FindSilenceSplitPoints(wavPath, c.minChunk, c.maxChunk, 0)
	if err != nil || len(splits) == 0 {
		c.logger.Info("speech: no VAD silence splits found, using overlapping windows")
		return c.transcribeOverlap(ctx, wavPath, dur)
	}
	slices := audio.SilenceSlices(dur, splits)
	return c.transcribeSlicesAndJoin(ctx, wavPath, slices)
}

func (c *ChunkingTranscriber) transcribeSlices(ctx context.Context, wavPath string, slices []audio.AudioSlice) ([]string, error) {
	var texts []string
	tmpDir := os.TempDir()

	for i, sl := range slices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunkPath := filepath.Join(tmpDir, fmt.Sprintf("mavor-chunk-%d-%d.wav", time.Now().UnixNano(), i))
		defer os.Remove(chunkPath)
		defer os.Remove(chunkPath + ".txt")

		if err := audio.SliceWAV(wavPath, chunkPath, sl.StartSample, sl.EndSample); err != nil {
			return nil, fmt.Errorf("speech: slice %d failed: %w", i, err)
		}

		c.logger.Info("speech: transcribing audio chunk",
			"chunk_idx", i,
			"chunk_count", len(slices),
			"start", sl.StartTime,
			"end", sl.EndTime,
			"duration", sl.EndTime-sl.StartTime,
		)

		txt, err := c.wrapped.Transcribe(ctx, chunkPath)
		if err != nil {
			return nil, fmt.Errorf("speech: transcribe chunk %d: %w", i, err)
		}
		txt = StripNonSpeech(txt)
		txt = strings.TrimSpace(txt)
		texts = append(texts, txt)
	}

	return texts, nil
}

func (c *ChunkingTranscriber) transcribeSlicesAndJoin(ctx context.Context, wavPath string, slices []audio.AudioSlice) (string, error) {
	texts, err := c.transcribeSlices(ctx, wavPath, slices)
	if err != nil {
		return "", err
	}
	var nonEmpties []string
	for _, t := range texts {
		if t != "" {
			nonEmpties = append(nonEmpties, t)
		}
	}
	joined := strings.Join(nonEmpties, " ")
	return output.CleanText(joined), nil
}

// Unwrap returns the underlying Transcriber if t is wrapped in a ChunkingTranscriber.
func Unwrap(t Transcriber) Transcriber {
	if c, ok := t.(*ChunkingTranscriber); ok {
		return c.wrapped
	}
	if cs, ok := t.(*chunkingStreamTranscriber); ok {
		return cs.wrapped
	}
	return t
}

// Unwrap returns the wrapped Transcriber.
func (c *ChunkingTranscriber) Unwrap() Transcriber {
	return c.wrapped
}

// Unwrap returns the wrapped Transcriber.
func (c *chunkingStreamTranscriber) Unwrap() Transcriber {
	return c.wrapped
}
