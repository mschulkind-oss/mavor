//go:build integration

package integration

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/output"
)

// This proves selection persistence on Sway, not GNOME fallback readiness.
func TestClipboardDispatchPersistenceRealSway(t *testing.T) {
	h := Start(t, Options{Width: 800, Height: 600})
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)
	run := func(name string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		// Do not capture daemonized wl-copy's inherited stdout/stderr pipes.
		if name == "wl-copy" {
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			return nil
		}
		cmd.WaitDelay = time.Second
		b, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %q: %v", name, args, err)
		}
		return b
	}
	run("wl-copy", "--primary", "untouched primary")
	text := "--clear café 世界 $(not a shell)"
	c := output.NewClipboard()
	if err := c.Emit(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	time.Sleep(output.ClipboardLaunchTimeout + 200*time.Millisecond)
	for i := 0; i < 2; i++ {
		if got := string(run("wl-paste", "--no-newline")); got != text {
			t.Fatalf("CLIPBOARD = %q", got)
		}
		if got := string(run("wl-paste", "--primary", "--no-newline")); got != "untouched primary" {
			t.Fatalf("PRIMARY = %q", got)
		}
	}
	if err := c.Emit(context.Background(), "next transcript"); err != nil {
		t.Fatal(err)
	}
	if got := string(run("wl-paste", "--no-newline")); got != "next transcript" {
		t.Fatalf("successive transcript = %q", got)
	}
}
