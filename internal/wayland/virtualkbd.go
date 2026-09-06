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

// maxKeycodes is how many distinct characters one keymap can carry.
//
// xkb keycodes run to 255 and the first 8 are reserved, so 247 is the ceiling.
// A transcript with more distinct runes than that is uploaded as several
// keymaps in turn — rare in any language mavor transcribes, and correct in all
// of them.
const maxKeycodes = 246

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
	for _, chunk := range chunkByDistinctRunes(text, maxKeycodes) {
		if err := vk.typeChunk(chunk); err != nil {
			return err
		}
	}
	return nil
}

func (vk *VirtualKeyboard) typeChunk(text string) error {
	codes, keymap := buildKeymap(text)
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

// key presses (state 1) or releases (state 0) one keycode.
func (vk *VirtualKeyboard) key(keycode uint32, state uint32) error {
	// zwp_virtual_keyboard_v1.key(time:uint, key:uint, state:uint)
	//
	// `key` is an evdev keycode, which is the xkb keycode minus 8. The
	// protocol takes evdev; the keymap is written in xkb. Conflating the two
	// types the wrong characters, and does it silently.
	b := newBuilder(vk.id, 1)
	b.putUint(uint32(time.Now().UnixMilli()))
	b.putUint(keycode - 8)
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

// chunkByDistinctRunes splits text so no piece needs more keycodes than a
// keymap holds. Splits are on rune boundaries and preserve order, so the text
// typed is the text given however many pieces it takes.
func chunkByDistinctRunes(text string, limit int) []string {
	var out []string
	seen := map[rune]bool{}
	start := 0
	for i, r := range text {
		if !seen[r] {
			if len(seen) == limit {
				out = append(out, text[start:i])
				seen = map[rune]bool{}
				start = i
			}
			seen[r] = true
		}
	}
	return append(out, text[start:])
}

// buildKeymap returns a keycode per distinct rune and the xkb keymap that
// defines them.
//
// One rune per key, with no modifier levels, is what makes typing arbitrary
// text tractable: the alternative is knowing which layout the user has and
// which shift level produces which character on it.
func buildKeymap(text string) (map[rune]uint32, string) {
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

	codes := make(map[rune]uint32, len(runes))
	var keycodes, symbols strings.Builder
	for i, r := range runes {
		// From 9, not 8. The protocol's `key` request takes an EVDEV
		// keycode, which is the xkb keycode minus 8 — so an xkb code of 8
		// is evdev 0, which is not a key. The compositor answers that
		// protocol error by closing the connection, and it presents as
		// "broken pipe" on a later write rather than at the offending one.
		code := uint32(i + 9)
		codes[r] = code
		fmt.Fprintf(&keycodes, "    <K%d> = %d;\n", i, code)
		fmt.Fprintf(&symbols, "    key <K%d> {[ %s ]};\n", i, keysymName(r))
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
`, len(runes)+8, keycodes.String(), symbols.String())
}

// keysymName spells a rune the way xkb wants it in a symbols map.
//
// The UXXXX form covers everything, including the characters whose ASCII value
// would otherwise be read as a keysym NAME — "a" is a valid keysym name and
// happens to work, while "(" is not and does not.
func keysymName(r rune) string {
	return fmt.Sprintf("U%04X", r)
}
