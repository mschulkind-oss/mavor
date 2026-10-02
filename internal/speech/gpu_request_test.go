package speech

import (
	"context"
	"errors"
	"github.com/mschulkind-oss/mavor/internal/audio"
	"path/filepath"
	"testing"
)

func TestGPURequestFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		mode                    string
		cpu, fallback, eligible bool
	}{{"inference-failure", false, false, true}, {"disconnect", false, false, true}, {"client-error", false, false, false}, {"startup-exit", false, false, false}, {"inference-failure", true, false, false}, {"inference-failure", false, true, false}} {
		t.Run(tc.mode, func(t *testing.T) {
			sup, _, _ := recoverySupervisor(t, tc.mode, "cpu-inference-failure", tc.cpu, tc.fallback)
			st := NewServerTranscriber("")
			st.Supervisor = sup
			path := filepath.Join(t.TempDir(), "valid.wav")
			if e := audio.WriteWAV(path, []int16{1, 2, 3}, 16000); e != nil {
				t.Fatal(e)
			}
			_, e := st.Transcribe(t.Context(), path)
			if IsGPURequestFailure(e) != tc.eligible {
				t.Fatalf("eligibility %v: %v", tc.eligible, e)
			}
		})
	}
	cause := errors.New("request allocation failed")
	e := &GPURequestError{Err: cause}
	if !errors.Is(e, cause) || !IsGPURequestFailure(e) {
		t.Fatal(e)
	}
	if IsGPURequestFailure(&GPURequestError{Err: context.Canceled}) {
		t.Fatal("cancel eligible")
	}
}

func TestInvalidWAVDoesNotQualifyServerFailure(t *testing.T) {
	sup, _, _ := recoverySupervisor(t, "inference-failure", "success", false, false)
	st := NewServerTranscriber("")
	st.Supervisor = sup
	_, e := st.Transcribe(t.Context(), recoveryWAV(t))
	if e == nil || IsGPURequestFailure(e) {
		t.Fatalf("invalid input eligible: %v", e)
	}
}
