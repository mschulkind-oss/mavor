//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
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

	p := output.NewPaste(nil)
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
		out1, _ := c.Output()
		sampleClip = string(out1)

		p := exec.Command("wl-paste", "-p", "-n")
		p.Env = append(os.Environ(), h.Env()...)
		out2, _ := p.Output()
		samplePrim = string(out2)
		close(done)
	}()

	testTranscript := "Dictation transcription test."
	if err := p.Emit(context.Background(), testTranscript); err != nil {
		t.Fatalf("Emit failed: %v", err)
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
	postClip, _ := cPost.Output()

	pPost := exec.Command("wl-paste", "-p", "-n")
	pPost.Env = append(os.Environ(), h.Env()...)
	postPrim, _ := pPost.Output()

	if string(postClip) != "prior clipboard text" {
		t.Errorf("CLIPBOARD after restore = %q, want %q", string(postClip), "prior clipboard text")
	}
	if string(postPrim) != "prior primary text" {
		t.Errorf("PRIMARY after restore = %q, want %q", string(postPrim), "prior primary text")
	}
}
