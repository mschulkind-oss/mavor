package speech

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/mschulkind-oss/mavor/internal/audio"
)

// readinessPCM is a deterministic, non-private two-second voiced fixture.
// Quiet 200ms margins surround a modulated 120Hz fundamental with vowel-like
// 720Hz and 1200Hz harmonics. It exercises decoding, not recognition accuracy.
func readinessPCM() []int16 {
	samples := make([]int16, 32000)
	for i := 3200; i < 28800; i++ {
		t := float64(i-3200) / 16000
		envelope := math.Min(1, math.Min(t/.08, (1.6-t)/.08))
		samples[i] = int16(envelope * (5000*math.Sin(2*math.Pi*120*t) + 2500*math.Sin(2*math.Pi*720*t) + 1500*math.Sin(2*math.Pi*1200*t)))
	}
	return samples
}

// VerifyReadiness performs actual inference and discards every recognized token.
// It never captures audio, emits text, or retries the main model on CPU. Native
// calls are joined before stream reset; cancellation cannot preempt a C call.
func VerifyReadiness(ctx context.Context, t Transcriber) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	st, streaming := t.(StreamTranscriber)
	if sh, ok := Unwrap(t).(*SherpaTranscriber); ok && !sh.Streaming {
		streaming = false
	}
	if streaming {
		abort, ok := st.(StreamAborter)
		if !ok {
			return fmt.Errorf("readiness: streaming model cannot reset")
		}
		defer func() {
			if resetErr := abort.AbortStream(context.Background()); resetErr != nil {
				err = errors.Join(err, fmt.Errorf("readiness reset: %w", resetErr))
			}
		}()
		if err := st.StartStream(ctx); err != nil {
			return fmt.Errorf("readiness start: %w", err)
		}
		samples := readinessPCM()
		pcm := make([]byte, len(samples)*2)
		for i, v := range samples {
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
		}
		for off := 0; off < len(pcm); off += 3200 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := st.FeedChunk(ctx, pcm[off:off+3200]); err != nil {
				return fmt.Errorf("readiness feed: %w", err)
			}
		}
		if _, err := st.StopStream(ctx); err != nil {
			return fmt.Errorf("readiness finalize: %w", err)
		}
		return ctx.Err()
	}
	dir, err := os.MkdirTemp("", "mavor-readiness-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "controlled-voiced.wav")
	if err := audio.WriteWAV(path, readinessPCM(), 16000); err != nil {
		return err
	}
	// Unwrap the chunking adapter: this short probe must not invoke request-time
	// CPU recovery, even when that separate policy was explicitly enabled.
	t = Unwrap(t)
	var decodeErr error
	if s, ok := t.(*ServerTranscriber); ok {
		_, decodeErr = s.transcribeOnce(ctx, path)
	} else {
		_, decodeErr = t.Transcribe(ctx, path)
	}
	if decodeErr != nil {
		return fmt.Errorf("readiness inference: %w", decodeErr)
	}
	return ctx.Err()
}
