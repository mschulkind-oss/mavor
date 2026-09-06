package output

import (
	"context"
	"testing"
)

// record every command Emit runs, so a test can assert on what did NOT run.
func recordingRunner(seen *[]string) Runner {
	return func(ctx context.Context, name string, args []string, stdin []byte) error {
		*seen = append(*seen, name)
		return nil
	}
}

// The default must not touch the clipboard. Someone dictating into an editor
// while holding a URL to paste should still have the URL afterwards.
func TestClipboardIsOffByDefault(t *testing.T) {
	var seen []string
	w := &Wayland{Run: recordingRunner(&seen)}

	if err := w.Emit(context.Background(), "hello"); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	for _, name := range seen {
		if name == "wl-copy" {
			t.Fatalf("wl-copy ran with Clipboard unset; commands were %v", seen)
		}
	}
	if len(seen) != 1 || seen[0] != "wtype" {
		t.Errorf("commands = %v, want just wtype — typing is not optional", seen)
	}
}

func TestClipboardRunsWhenEnabled(t *testing.T) {
	var seen []string
	w := &Wayland{Run: recordingRunner(&seen), Clipboard: true}

	if err := w.Emit(context.Background(), "hello"); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(seen) != 2 || seen[0] != "wtype" || seen[1] != "wl-copy" {
		t.Errorf("commands = %v, want wtype then wl-copy", seen)
	}
}

// Typing is the product: a clipboard failure must not hide a typing failure,
// and turning the clipboard off must not swallow one either.
func TestTypingErrorSurvivesEitherClipboardSetting(t *testing.T) {
	for _, clip := range []bool{false, true} {
		boom := func(ctx context.Context, name string, args []string, stdin []byte) error {
			if name == "wtype" {
				return context.DeadlineExceeded
			}
			return nil
		}
		w := &Wayland{Run: boom, Clipboard: clip}
		if err := w.Emit(context.Background(), "hello"); err == nil {
			t.Errorf("Clipboard=%v: Emit returned nil despite wtype failing", clip)
		}
	}
}

// Empty transcripts run nothing at all, whatever the setting.
func TestEmptyTextRunsNothing(t *testing.T) {
	var seen []string
	w := &Wayland{Run: recordingRunner(&seen), Clipboard: true}
	if err := w.Emit(context.Background(), "   "); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("commands = %v, want none", seen)
	}
}
