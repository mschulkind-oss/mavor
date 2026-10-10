package speech

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
)

type readinessProbe struct {
	path   string
	digest string
	fail   error
}

func (p *readinessProbe) Transcribe(ctx context.Context, path string) (string, error) {
	p.path = path
	raw, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	p.digest = fmt.Sprintf("%x", sha256.Sum256(raw))
	s, e := audio.ReadWAVSamples(path)
	if e != nil {
		return "", e
	}
	if len(s) != 32000 {
		return "", errors.New("wrong fixture length")
	}
	_ = os.WriteFile(path+".txt", []byte("discard"), 0600)
	return "discard", p.fail
}
func TestVerifyReadinessDecodesAndCleans(t *testing.T) {
	for _, failure := range []error{nil, errors.New("decode allocation failed")} {
		p := &readinessProbe{fail: failure}
		err := VerifyReadiness(t.Context(), p)
		t.Logf("generated non-private readiness WAV SHA256=%s", p.digest)
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
		for _, path := range []string{p.path, p.path + ".txt"} {
			if _, e := os.Stat(path); !os.IsNotExist(e) {
				t.Fatalf("fixture remains: %s", path)
			}
		}
	}
}
func TestReadinessStreamingResetAndCancellation(t *testing.T) {
	st := NewMockStreamTranscriber("discarded", "provisional")
	if e := VerifyReadiness(t.Context(), st); e != nil || st.IsStreaming() {
		t.Fatalf("%v active=%v", e, st.IsStreaming())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if e := VerifyReadiness(ctx, st); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	st.SetErrors(nil, errors.New("decoder failed"), nil)
	if e := VerifyReadiness(t.Context(), st); e == nil || st.IsStreaming() {
		t.Fatalf("%v", e)
	}
}

// The regression this whole policy exists for: the first version gave the
// connection ten seconds, a 3 GB model load blew it on a busy machine, and the
// fix then was to raise the number — which only moved the threshold until a
// saturated disk outran the new one. There is no budget to outrun now; this
// stays as the check that a load taking longer than those budgets used to allow
// still comes up.
func TestDefaultGPUAllowsLoadPastOldTenSecondDeadline(t *testing.T) {
	sup := NewSupervisor(SupervisorConfig{PollInterval: 5 * time.Millisecond, CommandFunc: func(ctx context.Context, cfg SupervisorConfig) *exec.Cmd {
		host, port := hostPort(cfg.ServerSocket)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCPURecoveryHelperProcess$")
		cmd.Env = append(os.Environ(), "MAVOR_CPU_RECOVERY_HELPER=1", "MAVOR_CPU_RECOVERY_MODE=success", "MAVOR_CPU_RECOVERY_DELAY=10100ms", "MAVOR_CPU_RECOVERY_ADDRESS="+net.JoinHostPort(host, port))
		return cmd
	}})
	defer sup.Stop()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := sup.Start(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStartedSupervisorOutlivesStartupProbeContext(t *testing.T) {
	sup, launches, _ := recoverySupervisor(t, "success", "success", false, false)
	ctx, cancel := context.WithCancel(t.Context())
	if err := sup.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
	if !sup.IsRunning() {
		t.Fatal("startup context cancellation killed transferred warm child")
	}
	st := NewServerTranscriber("")
	st.Supervisor = sup
	if _, err := st.Transcribe(t.Context(), recoveryWAV(t)); err != nil || len(*launches) != 1 {
		t.Fatalf("child restarted after readiness: %v launches=%d", err, len(*launches))
	}
}
