package wayland

import (
	"fmt"
	"strings"
	"testing"
)

// One key per distinct rune, and no modifier levels. That is what makes typing
// arbitrary text tractable: the alternative is knowing the user's layout and
// which shift level produces which character on it.
func TestKeymapGivesEveryRuneItsOwnKey(t *testing.T) {
	codes, km, err := buildKeymap("aA!é")
	if err != nil {
		t.Fatalf("buildKeymap: %v", err)
	}

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

// Keycodes are evdev, and evdev 0 is not a key. The compositor answers one by
// closing the connection, and it surfaces as a broken pipe several hundred
// requests later.
func TestKeycodesAreRealKeys(t *testing.T) {
	codes, _, err := buildKeymap("hello world")
	if err != nil {
		t.Fatalf("buildKeymap: %v", err)
	}
	for r, c := range codes {
		if c == 0 || c > 247 {
			t.Errorf("rune %q got evdev keycode %d, which is not a key", r, c)
		}
	}
}

// A space is the one rune every transcript contains. It goes on the space bar,
// so that even an application that reads the physical key rather than the
// keysym on it gets a space.
func TestSpaceLandsOnTheSpaceBar(t *testing.T) {
	codes, _, err := buildKeymap("hello world")
	if err != nil {
		t.Fatalf("buildKeymap: %v", err)
	}
	if got := codes[' ']; got != spaceKeycode {
		t.Errorf("space typed on evdev %d, want the space bar (%d)", got, spaceKeycode)
	}
}

// commandKeys are the evdev keycodes an application acts on rather than types.
// Assigning one to a character means that character disappears, or worse, in
// every application that decides what a keystroke means from the physical key:
// a space on Escape is how Chrome came to swallow every space in a dictation.
var commandKeys = map[uint32]string{
	1: "ESC", 14: "BACKSPACE", 15: "TAB", 28: "ENTER", 29: "LEFTCTRL",
	42: "LEFTSHIFT", 54: "RIGHTSHIFT", 56: "LEFTALT", 58: "CAPSLOCK",
	59: "F1", 60: "F2", 61: "F3", 62: "F4", 63: "F5", 64: "F6", 65: "F7",
	66: "F8", 67: "F9", 68: "F10", 87: "F11", 88: "F12",
	69: "NUMLOCK", 70: "SCROLLLOCK", 96: "KPENTER", 97: "RIGHTCTRL",
	99: "SYSRQ", 100: "RIGHTALT", 102: "HOME", 103: "UP", 104: "PAGEUP",
	105: "LEFT", 106: "RIGHT", 107: "END", 108: "DOWN", 109: "PAGEDOWN",
	110: "INSERT", 111: "DELETE", 119: "PAUSE", 125: "LEFTMETA",
	126: "RIGHTMETA", 127: "COMPOSE",
}

func TestNoRuneIsTypedOnACommandKey(t *testing.T) {
	// Every printable ASCII rune plus a few beyond it, which is more than any
	// real transcript asks for.
	var sb strings.Builder
	for r := rune(0x20); r < 0x7f; r++ {
		sb.WriteRune(r)
	}
	sb.WriteString("éüñ—")

	for i, chunk := range chunkToFit(sb.String()) {
		codes, _, err := buildKeymap(chunk)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		for r, c := range codes {
			if name, bad := commandKeys[c]; bad {
				t.Errorf("rune %q is typed on KEY_%s (evdev %d)", r, name, c)
			}
		}
	}
}

// The spillover pool is only safe for the runes Chromium resolves from the
// keysym. Everything else has to land on a key that produces text in a US
// layout, or it is dropped.
func TestOnlyAlphanumericsUseTheSpilloverKeys(t *testing.T) {
	spill := map[uint32]bool{}
	for _, c := range spilloverKeycodes {
		spill[c] = true
	}
	// Enough distinct runes to exhaust the character keys and push into the
	// spillover pool.
	text := "The Quick Brown Fox Jumps Over 1234567890 Lazy Dogs; wave, {mixed} [punctuation] & <symbols>!"
	codes, _, err := buildKeymap(text)
	if err != nil {
		t.Fatalf("buildKeymap: %v", err)
	}
	used := 0
	for r, c := range codes {
		if !spill[c] {
			continue
		}
		used++
		if !isASCIIAlnum(r) {
			t.Errorf("rune %q is on spillover key %d, which produces no text", r, c)
		}
	}
	if used == 0 {
		t.Fatal("the spillover pool was never reached, so this proves nothing")
	}
}

// The declared maximum has to cover every code, or the keymap will not compile.
func TestKeymapMaximumCoversEveryKeycode(t *testing.T) {
	codes, km, err := buildKeymap("abcdefghij")
	if err != nil {
		t.Fatalf("buildKeymap: %v", err)
	}
	var highest uint32
	for _, c := range codes {
		if c+8 > highest {
			highest = c + 8
		}
	}
	if want := fmt.Sprintf("maximum = %d;", highest); !strings.Contains(km, want) {
		t.Errorf("keymap does not declare a maximum covering xkb keycode %d:\n%s", highest, km)
	}
}

// Identical text must produce an identical keymap, so the wire traffic is
// predictable and this is testable at all.
func TestKeymapIsDeterministic(t *testing.T) {
	_, a, err := buildKeymap("the quick brown fox")
	if err != nil {
		t.Fatalf("buildKeymap: %v", err)
	}
	_, b, _ := buildKeymap("the quick brown fox")
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

	chunks := chunkToFit(text)
	if len(chunks) < 2 {
		t.Fatalf("700 distinct runes produced %d chunk(s)", len(chunks))
	}
	if strings.Join(chunks, "") != text {
		t.Error("chunks do not reassemble into the original text")
	}
	for i, c := range chunks {
		if _, _, err := buildKeymap(c); err != nil {
			t.Errorf("chunk %d does not fit in one keymap: %v", i, err)
		}
	}
}

// Ordinary text is one chunk; the splitting must not fire when it is not
// needed. Every keymap swap is another chance for the focused application to
// rebuild its layout mid-transcript.
func TestOrdinaryTextIsASingleChunk(t *testing.T) {
	if chunks := chunkToFit(strings.Repeat("the quick brown fox jumps ", 100)); len(chunks) != 1 {
		t.Errorf("ordinary English split into %d chunks", len(chunks))
	}
}

// The two pools together are sized so that a transcript using the entire
// alphabet in both cases, every digit and ordinary punctuation still uploads
// one keymap.
func TestFullASCIIAlphabetIsASingleChunk(t *testing.T) {
	text := "The Quick Brown Fox Jumps Over 1234567890 Lazy Dogs, " +
		"WHILE Every Kind Of Punctuation: \"quoted\"; (parens) [brackets] {braces} " +
		"50% of 3+4 = 7/1 — don't ask why!"
	if chunks := chunkToFit(text); len(chunks) != 1 {
		t.Errorf("a transcript with %d distinct runes split into %d chunks", len(distinctRunesOf(text)), len(chunks))
	}
}

func distinctRunesOf(s string) map[rune]bool {
	d := map[rune]bool{}
	for _, r := range s {
		d[r] = true
	}
	return d
}
