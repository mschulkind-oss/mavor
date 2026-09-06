package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 0.5 is the natural thing to try for "type a bit faster", and it stopped the
// daemon starting with a message naming a Go struct field. The message has to
// name the key, the line, and the fix.
func TestFractionalTypingDelayIsExplained(t *testing.T) {
	p := writeCfg(t, "[output]\ntyping_delay_ms = 0.5\n")

	_, err := LoadFile(p)
	if err == nil {
		t.Fatal("a fractional delay parsed; wtype rejects one, so it must not")
	}
	msg := err.Error()

	for _, want := range []string{"typing_delay_ms", "write 1, not 0.5"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "line 2") {
		t.Errorf("error does not say which line:\n%s", msg)
	}
}

// A percentage is the obvious wrong guess for a fraction.
func TestPreviewWidthAsAPercentageIsExplained(t *testing.T) {
	p := writeCfg(t, "[overlay]\npreview_width = \"50%\"\n")

	_, err := LoadFile(p)
	if err == nil {
		t.Fatal("a string preview_width parsed")
	}
	if !strings.Contains(err.Error(), "fraction of the screen") {
		t.Errorf("error does not say what the key wants:\n%s", err)
	}
}

// Keys with no special hint still get the key name and the line, which is the
// part go-toml buries.
func TestAnyTypeErrorNamesItsKeyAndLine(t *testing.T) {
	p := writeCfg(t, "model = \"whisper-tiny.en\"\n\n[overlay]\ntop_margin = \"eight\"\n")

	_, err := LoadFile(p)
	if err == nil {
		t.Fatal("a string top_margin parsed")
	}
	msg := err.Error()
	if !strings.Contains(msg, "overlay.top_margin") {
		t.Errorf("error does not name the key:\n%s", msg)
	}
	if !strings.Contains(msg, "line 4") {
		t.Errorf("error does not name the line:\n%s", msg)
	}
}

// A valid file must not be dragged through any of this.
func TestAValidFileStillLoads(t *testing.T) {
	p := writeCfg(t, "[output]\ntyping_delay_ms = 2\n")
	f, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if f.Output.TypingDelayMS == nil || *f.Output.TypingDelayMS != 2 {
		t.Errorf("typing_delay_ms did not survive: %v", f.Output.TypingDelayMS)
	}
}
