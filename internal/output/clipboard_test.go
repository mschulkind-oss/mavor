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

// Unset must leave wtype's default alone, and zero must be expressible as a
// deliberate request — which is why the config field is a pointer.
func TestTypingDelayIsOmittedWhenUnset(t *testing.T) {
	var args []string
	w := &Wayland{Run: func(ctx context.Context, name string, a []string, stdin []byte) error {
		if name == "wtype" {
			args = a
		}
		return nil
	}}
	if err := w.Emit(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if a == "-d" {
			t.Fatalf("wtype got -d with no delay configured: %v", args)
		}
	}
}

func TestTypingDelayIsPassedBeforeTheSeparator(t *testing.T) {
	for _, ms := range []int{0, 12} {
		var args []string
		delay := ms
		w := &Wayland{
			TypingDelayMS: &delay,
			Run: func(ctx context.Context, name string, a []string, stdin []byte) error {
				if name == "wtype" {
					args = a
				}
				return nil
			},
		}
		if err := w.Emit(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		// Everything after "--" is literal text to type, so the flag has to
		// precede it or wtype would type "-d 12".
		want := []string{"-d", itoa(ms), "--", "hi"}
		if len(args) != len(want) {
			t.Fatalf("args = %v, want %v", args, want)
		}
		for i := range want {
			if args[i] != want[i] {
				t.Fatalf("args = %v, want %v", args, want)
			}
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
