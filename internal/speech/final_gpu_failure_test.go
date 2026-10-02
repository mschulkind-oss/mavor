package speech

import (
	"context"
	"errors"
	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/models"
	"path/filepath"
	"testing"
)

type gpuSegmentProbe struct{ calls int }

func (p *gpuSegmentProbe) Transcribe(context.Context, string) (string, error) {
	p.calls++
	return "", &GPURequestError{Err: errors.New("request failed")}
}
func TestFailedGPUSegmentDoesNotReplayOrReturnPartialMain(t *testing.T) {
	p := &gpuSegmentProbe{}
	s, e := NewFinalSession(t.Context(), p, FinalSessionOptions{Mode: models.FinalSegments})
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "capture.wav")
	_ = audio.WriteWAV(path, make([]int16, 40000), 16000)
	r, e := s.Finish(t.Context(), path)
	if !IsGPURequestFailure(e) || r.Text != "" || p.calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", r, e, p.calls)
	}
}
