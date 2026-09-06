package daemon

import "testing"

// The streaming recognisers shout; the overlay should not.
func TestAllCapsPreviewIsLowercased(t *testing.T) {
	const shouted = "THE GRASS IS TALL AND WIDE"
	if got := soften(shouted); got != "the grass is tall and wide" {
		t.Errorf("soften(%q) = %q", shouted, got)
	}
}

// A model that cases its own output is left exactly alone — including any
// acronym inside it, which is why the test is "no lower case anywhere" rather
// than "looks shouty".
func TestMixedCaseIsLeftAlone(t *testing.T) {
	for _, s := range []string{
		"The grass is tall",
		"I asked GEMINI for help",
		"gRPC and mTLS",
	} {
		if got := soften(s); got != s {
			t.Errorf("soften(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestTextWithNoLettersIsUnchanged(t *testing.T) {
	for _, s := range []string{"", "   ", "123 456", "-- ... --"} {
		if got := soften(s); got != s {
			t.Errorf("soften(%q) = %q, want it unchanged", s, got)
		}
	}
}

// Apostrophes and punctuation are not letters and must not stop the rule
// firing — the zipformer emits plenty of them.
func TestShoutedTextWithPunctuationIsStillSoftened(t *testing.T) {
	const s = "IT'S REALLY GREAT, UP UNTIL THE END."
	if got := soften(s); got != "it's really great, up until the end." {
		t.Errorf("soften(%q) = %q", s, got)
	}
}
