//go:build integration

package integration

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/output"
)

// In-process typing has to be checked against a real compositor, because the
// thing it replaces — wtype — is only replaceable if the characters actually
// arrive. A unit test can prove the keymap is well formed and prove nothing
// about whether anything was typed.
//
// Verified through the clipboard: type into a `wl-paste`-visible consumer is
// awkward to arrange headlessly, so the assertion here is that the protocol is
// accepted end to end and that the timing is what the change was made for.

func TestNativeTypingIsAcceptedByTheCompositor(t *testing.T) {
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	n, err := output.NewNative(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err != nil {
		t.Fatalf("NewNative: %v — sway implements zwp_virtual_keyboard_manager_v1, so this should succeed", err)
	}
	defer n.Close()

	if err := n.Emit(context.Background(), "hello from mavor"); err != nil {
		t.Fatalf("Emit: %v", err)
	}
}

// Unicode, punctuation and case all go through the same one-key-per-rune
// keymap, so a transcript with any of them must not error.
func TestNativeTypingHandlesAwkwardText(t *testing.T) {
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	n, err := output.NewNative(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("NewNative: %v", err)
	}
	defer n.Close()

	for _, s := range []string{
		"Mixed CASE with punctuation: it's here, isn't it?",
		"em—dash, ellipsis… and a quote “like this”",
		"digits 0123456789 and symbols !@#$%^&*()[]{}",
	} {
		if err := n.Emit(context.Background(), s); err != nil {
			t.Errorf("Emit(%q): %v", s, err)
		}
	}
}

// The reason the change was made. wtype measured 4.14 ms/char on this same
// compositor; batching the whole transcript should be far below that.
func TestNativeTypingIsFasterThanWtype(t *testing.T) {
	h := sharedCompositor(t)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	n, err := output.NewNative(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("NewNative: %v", err)
	}
	defer n.Close()

	text := strings.Repeat("the quick brown fox ", 20) // 400 characters
	start := time.Now()
	if err := n.Emit(context.Background(), text); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	elapsed := time.Since(start)
	perChar := float64(elapsed.Microseconds()) / float64(len(text)) / 1000
	t.Logf("in-process: %d chars in %v (%.4f ms/char); wtype measured 4.14 ms/char", len(text), elapsed, perChar)

	// Generous: the claim is an order of magnitude, so failing at half
	// wtype's cost catches a regression without pinning the hardware.
	if perChar > 2.0 {
		t.Errorf("%.4f ms/char is not meaningfully better than wtype's 4.14", perChar)
	}
}
