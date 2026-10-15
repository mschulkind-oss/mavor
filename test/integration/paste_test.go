//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/output"
)

// TestPasteDispatchRealSway verifies that Paste.Emit correctly populates both
// Wayland CLIPBOARD and PRIMARY selection buffers during the lease window and
// restores previous selections afterward in a real headless Sway session.
func TestPasteDispatchRealSway(t *testing.T) {
	h := Start(t, Options{Width: 800, Height: 600})

	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	// Pre-populate initial selections
	cmd1 := exec.Command("wl-copy", "prior clipboard text")
	cmd1.Env = append(os.Environ(), h.Env()...)
	if err := cmd1.Run(); err != nil {
		t.Fatalf("set initial clipboard: %v", err)
	}

	cmd2 := exec.Command("wl-copy", "--primary", "prior primary text")
	cmd2.Env = append(os.Environ(), h.Env()...)
	if err := cmd2.Run(); err != nil {
		t.Fatalf("set initial primary: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	for _, selection := range []struct {
		args []string
		want string
	}{
		{[]string{"-n"}, "prior clipboard text"},
		{[]string{"-p", "-n"}, "prior primary text"},
	} {
		cmd := exec.Command("wl-paste", selection.args...)
		cmd.Env = append(os.Environ(), h.Env()...)
		got, err := cmd.CombinedOutput()
		if err != nil || string(got) != selection.want {
			t.Fatalf("initial selection %v = %q, err=%v, want %q", selection.args, got, err, selection.want)
		}
	}

	p := output.NewPaste(nil)
	p.Launcher = pasteDiagnosticLauncher{t: t}
	p.Chord = "shift+insert"
	p.RestoreSelection = true
	p.LeaseDuration = 200 * time.Millisecond
	p.RestoreDelay = 50 * time.Millisecond

	// Monitor selections during active lease window in background goroutine
	done := make(chan struct{})
	var sampleClip, samplePrim string
	go func() {
		time.Sleep(80 * time.Millisecond)
		c := exec.Command("wl-paste", "-n")
		c.Env = append(os.Environ(), h.Env()...)
		out1, err := c.CombinedOutput()
		if err != nil {
			t.Errorf("lease CLIPBOARD read: %v: %s", err, out1)
		}
		sampleClip = string(out1)

		p := exec.Command("wl-paste", "-p", "-n")
		p.Env = append(os.Environ(), h.Env()...)
		out2, err := p.CombinedOutput()
		if err != nil {
			t.Errorf("lease PRIMARY read: %v: %s", err, out2)
		}
		samplePrim = string(out2)
		close(done)
	}()

	testTranscript := "Dictation transcription test."
	if err := p.Emit(context.Background(), testTranscript); err != nil {
		t.Errorf("Emit failed: %v", err)
	}
	<-done

	if sampleClip != testTranscript {
		t.Errorf("CLIPBOARD during lease = %q, want %q", sampleClip, testTranscript)
	}
	if samplePrim != testTranscript {
		t.Errorf("PRIMARY during lease = %q, want %q", samplePrim, testTranscript)
	}

	// Wait for restoreDelay + settle
	time.Sleep(100 * time.Millisecond)

	cPost := exec.Command("wl-paste", "-n")
	cPost.Env = append(os.Environ(), h.Env()...)
	postClip, err := cPost.CombinedOutput()
	if err != nil {
		t.Errorf("restored CLIPBOARD read: %v: %s", err, postClip)
	}

	pPost := exec.Command("wl-paste", "-p", "-n")
	pPost.Env = append(os.Environ(), h.Env()...)
	postPrim, err := pPost.CombinedOutput()
	if err != nil {
		t.Errorf("restored PRIMARY read: %v: %s", err, postPrim)
	}

	if string(postClip) != "prior clipboard text" {
		t.Errorf("CLIPBOARD after restore = %q, want %q", string(postClip), "prior clipboard text")
	}
	if string(postPrim) != "prior primary text" {
		t.Errorf("PRIMARY after restore = %q, want %q", string(postPrim), "prior primary text")
	}
}

// Keep command results/deadlines visible when real compositor failures recur.
type pasteDiagnosticLauncher struct{ t *testing.T }

func (l pasteDiagnosticLauncher) Run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	start := time.Now()
	deadline, bounded := ctx.Deadline()
	out, err := (output.RealLauncher{}).Run(ctx, stdin, name, args...)
	l.t.Logf("run name=%s args=%v input_bytes=%d output=%q elapsed=%s bounded=%v deadline=%s ctx_err=%v err=%v", name, args, len(stdin), out, time.Since(start), bounded, deadline, ctx.Err(), err)
	if name == "wl-paste" && err != nil {
		return out, fmt.Errorf("snapshot %v: %w", args, err)
	}
	return out, err
}
func (l pasteDiagnosticLauncher) Start(ctx context.Context, stdin []byte, name string, args ...string) (output.ManagedCmd, error) {
	cmd, err := (output.RealLauncher{}).Start(ctx, stdin, name, args...)
	l.t.Logf("start name=%s args=%v err=%v", name, args, err)
	return cmd, err
}

// TestPasteDispatchWaitsForSlowHoldersRealSway pins the registration handshake
// against a real compositor. The holders are launched through a 250ms delay, so
// a dispatch that injected the chord 20ms after Start — before either holder had
// connected — would read the pre-dispatch selections at that moment and hand
// them to the reader. mavor must instead wait until a reader would receive the
// transcript.
func TestPasteDispatchWaitsForSlowHoldersRealSway(t *testing.T) {
	h := Start(t, Options{Width: 800, Height: 600})

	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)
	t.Setenv("MAVOR_TEST_HOLDER_DELAY", "0.250")

	for _, seed := range []struct {
		args []string
		want string
	}{
		{[]string{"prior clipboard"}, "prior clipboard"},
		{[]string{"--primary", "prior primary"}, "prior primary"},
	} {
		cmd := exec.Command("wl-copy", seed.args...)
		cmd.Env = append(os.Environ(), h.Env()...)
		if err := cmd.Run(); err != nil {
			t.Fatalf("seed %v: %v", seed.args, err)
		}
	}
	time.Sleep(50 * time.Millisecond)

	const transcript = "Delayed holder transcript."
	p := output.NewPaste(nil)
	p.Launcher = slowHolderLauncher{t: t, transcript: transcript}
	p.RestoreSelection = true
	p.LeaseDuration = 200 * time.Millisecond
	p.RestoreDelay = 50 * time.Millisecond

	if err := p.Emit(context.Background(), transcript); err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)
	for _, sel := range []struct {
		args []string
		want string
	}{
		{[]string{"-n"}, "prior clipboard"},
		{[]string{"-p", "-n"}, "prior primary"},
	} {
		cmd := exec.Command("wl-paste", sel.args...)
		cmd.Env = append(os.Environ(), h.Env()...)
		got, err := cmd.CombinedOutput()
		if err != nil || strings.TrimRight(string(got), "\n") != sel.want {
			t.Errorf("restored %v = %q, err=%v, want %q", sel.args, got, err, sel.want)
		}
	}
}

// slowHolderLauncher starts each selection holder only after
// $MAVOR_TEST_HOLDER_DELAY seconds, which is what a loaded machine looks like to
// the dispatch, and asserts while injecting the chord that both selections
// already serve the transcript.
type slowHolderLauncher struct {
	t          *testing.T
	transcript string
}

func (l slowHolderLauncher) Run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if name == "wtype" {
		l.assertSelectionServes("CLIPBOARD", "-n")
		l.assertSelectionServes("PRIMARY", "-p", "-n")
	}
	return output.RealLauncher{}.Run(ctx, stdin, name, args...)
}

func (l slowHolderLauncher) assertSelectionServes(label string, args ...string) {
	l.t.Helper()
	cmd := exec.Command("wl-paste", args...)
	cmd.Env = os.Environ()
	got, err := cmd.CombinedOutput()
	if err != nil {
		l.t.Errorf("reading %s at chord time: %v: %s", label, err, got)
		return
	}
	if strings.TrimRight(string(got), "\n") != l.transcript {
		l.t.Errorf("chord injected while %s served %q, not the transcript", label, got)
	}
}

func (l slowHolderLauncher) Start(ctx context.Context, stdin []byte, name string, args ...string) (output.ManagedCmd, error) {
	// "$@" keeps every holder argument (including the transcript) exactly as
	// the driver passed it; only the exec is delayed.
	return (output.RealLauncher{}).Start(ctx, stdin, "sh",
		append([]string{"-c", `sleep "$MAVOR_TEST_HOLDER_DELAY"; exec "$@"`, "sh", name}, args...)...)
}
