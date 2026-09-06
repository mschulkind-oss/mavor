package wayland

import (
	"strings"
	"testing"
)

// One key per distinct rune, and no modifier levels. That is what makes typing
// arbitrary text tractable: the alternative is knowing the user's layout and
// which shift level produces which character on it.
func TestKeymapGivesEveryRuneItsOwnKey(t *testing.T) {
	codes, km := buildKeymap("aA!é")

	if len(codes) != 4 {
		t.Fatalf("got %d keycodes for 4 distinct runes: %v", len(codes), codes)
	}
	seen := map[uint32]rune{}
	for r, c := range codes {
		if prev, dup := seen[c]; dup {
			t.Errorf("runes %q and %q share keycode %d", prev, r, c)
		}
		seen[c] = r
	}
	for _, want := range []string{"U0061", "U0041", "U0021", "U00E9"} {
		if !strings.Contains(km, want) {
			t.Errorf("keymap is missing %s:\n%s", want, km)
		}
	}
}

// Keycodes start at 9, not 8. The protocol's key request takes an EVDEV code,
// which is the xkb code minus 8, so an xkb 8 is evdev 0 — not a key. The
// compositor answers that by closing the connection, and it surfaces as a
// broken pipe several hundred requests later.
func TestKeycodesStartAtNine(t *testing.T) {
	codes, _ := buildKeymap("hello world")
	for r, c := range codes {
		if c < 9 {
			t.Errorf("rune %q got keycode %d; evdev would be %d, which is not a key", r, c, int(c)-8)
		}
	}
}

// The declared maximum has to cover every code, or the keymap will not compile.
func TestKeymapMaximumCoversEveryKeycode(t *testing.T) {
	codes, km := buildKeymap("abcdefghij")
	var highest uint32
	for _, c := range codes {
		if c > highest {
			highest = c
		}
	}
	if !strings.Contains(km, "maximum = 18") {
		t.Errorf("keymap does not declare a maximum covering keycode %d:\n%s", highest, km)
	}
}

// Identical text must produce an identical keymap, so the wire traffic is
// predictable and this is testable at all.
func TestKeymapIsDeterministic(t *testing.T) {
	_, a := buildKeymap("the quick brown fox")
	_, b := buildKeymap("the quick brown fox")
	if a != b {
		t.Error("the same text produced two different keymaps")
	}
}

// A transcript with more distinct runes than a keymap holds is split, and the
// pieces must still concatenate to the original — a lost or reordered chunk is
// a mangled transcript.
func TestChunkingPreservesTheText(t *testing.T) {
	var sb strings.Builder
	for r := rune(0x100); r < 0x100+700; r++ {
		sb.WriteRune(r)
	}
	text := sb.String()

	chunks := chunkByDistinctRunes(text, maxKeycodes)
	if len(chunks) < 2 {
		t.Fatalf("700 distinct runes produced %d chunk(s); the limit is %d", len(chunks), maxKeycodes)
	}
	if strings.Join(chunks, "") != text {
		t.Error("chunks do not reassemble into the original text")
	}
	for i, c := range chunks {
		distinct := map[rune]bool{}
		for _, r := range c {
			distinct[r] = true
		}
		if len(distinct) > maxKeycodes {
			t.Errorf("chunk %d needs %d keycodes, over the %d limit", i, len(distinct), maxKeycodes)
		}
	}
}

// Ordinary text is one chunk; the splitting must not fire when it is not needed.
func TestOrdinaryTextIsASingleChunk(t *testing.T) {
	chunks := chunkByDistinctRunes(strings.Repeat("the quick brown fox jumps ", 100), maxKeycodes)
	if len(chunks) != 1 {
		t.Errorf("ordinary English split into %d chunks", len(chunks))
	}
}
