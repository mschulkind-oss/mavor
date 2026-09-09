package speech

import "testing"

// Whisper models emit non-speech annotations as ordinary tokens — they are in
// the training transcripts, so near-silence decodes to "[BLANK_AUDIO]" or
// "(machine whirring)" rather than to nothing. Those must never reach the
// user's keyboard.
func TestStripNonSpeech(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"blank audio alone", "[BLANK_AUDIO]", ""},
		{"parenthesised noise alone", "(machine whirring)", ""},
		{"asterisked noise alone", "*coughs*", ""},
		{"music tag alone", "[MUSIC]", ""},
		{"marker with surrounding space", "  [BLANK_AUDIO]  ", ""},
		{"two markers alone", "[BLANK_AUDIO] (silence)", ""},

		{"trailing marker", "hello world (typing)", "hello world"},
		{"leading marker", "(door closes) hello world", "hello world"},
		{"embedded marker", "hello (typing) world", "hello world"},
		{"marker before comma", "hello (typing), world", "hello, world"},
		{"marker before period", "hello (typing).", "hello."},

		{"plain text untouched", "hello world", "hello world"},
		{"punctuation untouched", "hello, world. how are you?", "hello, world. how are you?"},
		{"empty input", "", ""},

		// Multi-segment transcripts keep their line structure: collapsing
		// newlines would silently reflow dictated text.
		{"newlines preserved", "first line\nsecond line", "first line\nsecond line"},
		{"marker on its own line", "first line\n[BLANK_AUDIO]\nsecond line", "first line\nsecond line"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripNonSpeech(tc.in); got != tc.want {
				t.Errorf("StripNonSpeech(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
