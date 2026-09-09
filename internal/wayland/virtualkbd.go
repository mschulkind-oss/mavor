package wayland

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The request order here is keymap(0), key(1), modifiers(2), destroy(3), and
// it is worth stating because getting it wrong is silent. Opcodes are
// positional, so an off-by-one does not fail — it calls a DIFFERENT request
// with the arguments of the one intended. Sending modifiers at opcode 3
// destroyed the keyboard instead, and the only symptom was "invalid object"
// on the next request, blaming code that was correct.
//
// Typing into the focused window is a Wayland protocol, not a program. mavor
// shelled out to wtype for it, which costs a process spawn per dictation and a
// protocol round-trip per keystroke — measured at 4.14 ms a character, which
// is most of the time an overlay spends saying "transcribing". This speaks
// zwp_virtual_keyboard_v1 directly, on the connection the overlay already has.

// VirtualKeyboard types text into whatever has focus.
type VirtualKeyboard struct {
	d  *Display
	id ObjectID
}

// NewVirtualKeyboard creates a keyboard on the compositor's first seat.
//
// It fails on a compositor without zwp_virtual_keyboard_manager_v1 — GNOME's
// Mutter and KDE's KWin both lack it — and that failure is the caller's cue to
// fall back rather than to give up.
func (d *Display) NewVirtualKeyboard() (*VirtualKeyboard, error) {
	if d.seat == 0 {
		return nil, fmt.Errorf("wayland: compositor offers no wl_seat, so there is nothing to type on")
	}
	if d.vkManager == 0 {
		return nil, fmt.Errorf("wayland: compositor does not implement zwp_virtual_keyboard_manager_v1 — " +
			"synthetic typing needs it, and sway, hyprland and river have it where GNOME and KDE do not")
	}

	id := d.conn.newID(nil)
	// zwp_virtual_keyboard_manager_v1.create_virtual_keyboard(seat, id)
	b := newBuilder(d.vkManager, 0)
	b.putObject(d.seat)
	b.putObject(id)
	if err := d.conn.send(b); err != nil {
		return nil, err
	}
	// Confirm the compositor accepted it before reporting success, so a
	// refusal is an error from this constructor rather than a broken pipe
	// from the first thing typed.
	if err := d.Roundtrip(); err != nil {
		return nil, fmt.Errorf("wayland: compositor refused a virtual keyboard: %w", err)
	}
	return &VirtualKeyboard{d: d, id: id}, nil
}

// Type sends text as keystrokes, in order.
//
// Every distinct rune gets its own keycode in a keymap uploaded first, so no
// modifier state is ever needed: a capital letter is its own key, not shift
// plus another. That is what makes this safe to do while the user holds a
// modifier of their own.
func (vk *VirtualKeyboard) Type(text string) error {
	if text == "" {
		return nil
	}
	for _, chunk := range chunkToFit(text) {
		if err := vk.typeChunk(chunk); err != nil {
			return err
		}
	}
	return nil
}

func (vk *VirtualKeyboard) typeChunk(text string) error {
	codes, keymap, err := buildKeymap(text)
	if err != nil {
		return err
	}
	if err := vk.uploadKeymap(keymap); err != nil {
		return err
	}
	// No modifiers, ever: the keymap puts every character on its own key.
	// Sent once, after the keymap, so a Shift the user is physically holding
	// cannot turn the transcript into capitals.
	if err := vk.modifiers(0, 0, 0, 0); err != nil {
		return err
	}
	// Flush before the keys. A rejected keymap otherwise shows up hundreds of
	// requests later as a broken pipe, blaming whichever key happened to be
	// in flight rather than the mapping the compositor actually refused.
	if err := vk.d.Roundtrip(); err != nil {
		return fmt.Errorf("wayland: compositor refused the keymap: %w", err)
	}

	for _, r := range text {
		code, ok := codes[r]
		if !ok {
			continue
		}
		if err := vk.key(code, 1); err != nil {
			return err
		}
		if err := vk.key(code, 0); err != nil {
			return err
		}
	}

	// Flush, and find out whether the compositor objected.
	//
	// Typing writes and never reads, so a protocol error sits unread in the
	// socket while more requests pile on top of it — and the first symptom is
	// a "broken pipe" several hundred requests after the mistake, pointing at
	// innocent code. A roundtrip here drains the compositor's replies and
	// surfaces what it actually said.
	return vk.d.Roundtrip()
}

func (vk *VirtualKeyboard) uploadKeymap(keymap string) error {
	fd, err := unix.MemfdCreate("mavor-keymap", unix.MFD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("wayland: memfd_create for keymap: %w", err)
	}
	defer unix.Close(fd)

	// The trailing NUL is part of the contract: the compositor reads the
	// mapping as a C string of exactly this size.
	body := append([]byte(keymap), 0)
	if err := unix.Ftruncate(fd, int64(len(body))); err != nil {
		return fmt.Errorf("wayland: size keymap: %w", err)
	}
	mem, err := unix.Mmap(fd, 0, len(body), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return fmt.Errorf("wayland: map keymap: %w", err)
	}
	copy(mem, body)
	if err := unix.Munmap(mem); err != nil {
		return fmt.Errorf("wayland: unmap keymap: %w", err)
	}

	// zwp_virtual_keyboard_v1.keymap(format:uint, fd, size:uint)
	// Format 1 is XKB_V1, the only one the protocol defines.
	b := newBuilder(vk.id, 0)
	b.putUint(1)
	b.putFD(fd)
	b.putUint(uint32(len(body)))
	return vk.d.conn.send(b)
}

// key presses (state 1) or releases (state 0) one evdev keycode.
func (vk *VirtualKeyboard) key(keycode uint32, state uint32) error {
	// zwp_virtual_keyboard_v1.key(time:uint, key:uint, state:uint)
	//
	// `key` is an evdev keycode, which is the xkb keycode minus 8. The
	// protocol takes evdev; the keymap is written in xkb. Conflating the two
	// types the wrong characters, and does it silently — so keycodes are
	// evdev everywhere in this file, and buildKeymap adds the 8 in the one
	// place that writes xkb.
	b := newBuilder(vk.id, 1)
	b.putUint(uint32(time.Now().UnixMilli()))
	b.putUint(keycode)
	b.putUint(state)
	return vk.d.conn.send(b)
}

func (vk *VirtualKeyboard) modifiers(depressed, latched, locked, group uint32) error {
	// zwp_virtual_keyboard_v1.modifiers(depressed, latched, locked, group)
	b := newBuilder(vk.id, 2)
	b.putUint(depressed)
	b.putUint(latched)
	b.putUint(locked)
	b.putUint(group)
	return vk.d.conn.send(b)
}

// Close destroys the keyboard.
func (vk *VirtualKeyboard) Close() error {
	// zwp_virtual_keyboard_v1.destroy()
	return vk.d.conn.send(newBuilder(vk.id, 3))
}

// Which physical key a character is typed on is not an implementation detail.
//
// The keysym on a key says what the character is, and a client that reads only
// the keysym — GTK, Qt, any native wlroots client — types what mavor put there
// whatever key it sits under. Chromium is not that client. It resolves ASCII
// letters and digits from the keysym, and every other character — space,
// punctuation, anything outside ASCII — from the *physical* key beneath it, by
// falling through to `DomCodeToUsLayoutKeyboardCode`. A character parked on a
// key that produces no text in a US layout is silently dropped.
//
// Counting keycodes off from 1 put every space on the physical Escape key,
// because a space is the lowest codepoint left after CleanText and the runes
// were assigned in sorted order. Chrome swallowed every space of every
// dictation while every native app typed the same transcript correctly. So
// keys are handed out from two pools, and never counted off from 1.

// spaceKeycode is the space bar. A space is pinned to it: it is the one rune
// every transcript contains, and the one whose loss is most visible.
const spaceKeycode = 57

// characterKeycodes carry a character and nothing else in a US layout — the
// digit row, the three letter rows, the punctuation around them, and the three
// international keys. An application that decides what a keystroke means from
// the physical key still gets text out of every one of these, so any rune may
// be assigned one.
var characterKeycodes = []uint32{
	2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, // 1234567890-=
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, // qwertyuiop[]
	30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, // asdfghjkl;'`
	43,                                     // backslash
	44, 45, 46, 47, 48, 49, 50, 51, 52, 53, // zxcvbnm,./
	86,  // the 102nd key, <> on an ISO board
	89,  // RO
	124, // YEN
}

// spilloverKeycodes carry no character, but pressing one does nothing on any
// desktop: F13-F24, which nothing binds, and the numeric keypad. Only ASCII
// alphanumerics are put here, because those are the runes Chromium reads off
// the keysym — a letter types correctly on F13 where a comma would not.
//
// They exist so that a transcript using the whole ASCII alphabet still fits in
// a single keymap. Every key deliberately absent from both pools is one an
// application acts on rather than types: Escape, Backspace, Tab, Enter, the
// modifiers, F1-F12 (help, reload, fullscreen, devtools) and the navigation
// cluster.
var spilloverKeycodes = []uint32{
	183, 184, 185, 186, 187, 188, 189, 190, 191, 192, 193, 194, // F13-F24
	71, 72, 73, 75, 76, 77, 79, 80, 81, 82, 83, // keypad 7894561230.
	55, 74, 78, 98, 121, // keypad * - + / ,
}

// assignKeycodes gives every rune a physical key, or reports that this set of
// runes does not fit in one keymap.
//
// Alphanumerics take the spillover pool first. Taking it in one pass instead
// would let a letter claim a character key and starve a comma, which has
// nowhere else to go.
func assignKeycodes(runes []rune) (map[rune]uint32, bool) {
	codes := make(map[rune]uint32, len(runes))
	spill := spilloverKeycodes
	for _, r := range runes {
		switch {
		case r == ' ':
			codes[r] = spaceKeycode
		case isASCIIAlnum(r) && len(spill) > 0:
			codes[r], spill = spill[0], spill[1:]
		}
	}
	chars := characterKeycodes
	for _, r := range runes {
		if _, done := codes[r]; done {
			continue
		}
		if len(chars) == 0 {
			return nil, false
		}
		codes[r], chars = chars[0], chars[1:]
	}
	return codes, true
}

func isASCIIAlnum(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// chunkToFit splits text so every piece can be typed from one keymap. Splits
// are on rune boundaries and preserve order, so the text typed is the text
// given however many pieces it takes.
func chunkToFit(text string) []string {
	var out []string
	var distinct []rune
	seen := map[rune]bool{}
	start := 0
	for i, r := range text {
		if seen[r] {
			continue
		}
		if _, ok := assignKeycodes(append(append([]rune{}, distinct...), r)); !ok {
			out = append(out, text[start:i])
			seen = map[rune]bool{}
			distinct = distinct[:0]
			start = i
		}
		seen[r] = true
		distinct = append(distinct, r)
	}
	return append(out, text[start:])
}

// buildKeymap returns the evdev keycode per distinct rune and the xkb keymap
// that defines them.
//
// One rune per key, with no modifier levels, is what makes typing arbitrary
// text tractable: the alternative is knowing which layout the user has and
// which shift level produces which character on it.
func buildKeymap(text string) (map[rune]uint32, string, error) {
	var runes []rune
	seen := map[rune]bool{}
	for _, r := range text {
		if !seen[r] {
			seen[r] = true
			runes = append(runes, r)
		}
	}
	// Sorted so the same text always produces byte-identical keymaps, which
	// makes the output testable and the wire traffic predictable.
	sort.Slice(runes, func(i, j int) bool { return runes[i] < runes[j] })

	codes, ok := assignKeycodes(runes)
	if !ok {
		return nil, "", fmt.Errorf("wayland: %d distinct runes need more keys than one keymap holds", len(runes))
	}

	var keycodes, symbols strings.Builder
	var highest uint32
	for _, r := range runes {
		// The keymap is written in xkb keycodes, which are the evdev codes
		// plus 8. This is the one place that conversion happens; everything
		// else in this file is evdev, because that is what the protocol's
		// `key` request takes.
		xkb := codes[r] + 8
		if xkb > highest {
			highest = xkb
		}
		fmt.Fprintf(&keycodes, "    <K%d> = %d;\n", xkb, xkb)
		fmt.Fprintf(&symbols, "    key <K%d> {[ %s ]};\n", xkb, keysymName(r))
	}

	return codes, fmt.Sprintf(`xkb_keymap {
xkb_keycodes "(unnamed)" {
    minimum = 8;
    maximum = %d;
%s};
xkb_types "(unnamed)" { include "complete" };
xkb_compatibility "(unnamed)" { include "complete" };
xkb_symbols "(unnamed)" {
    name[Group1] = "mavor";
%s};
};
`, highest, keycodes.String(), symbols.String()), nil
}

// keysymName spells a rune the way xkb wants it in a symbols map.
//
// The UXXXX form covers everything, including the characters whose ASCII value
// would otherwise be read as a keysym NAME — "a" is a valid keysym name and
// happens to work, while "(" is not and does not.
func keysymName(r rune) string {
	return fmt.Sprintf("U%04X", r)
}
