package output

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mschulkind-oss/mavor/internal/wayland"
)

// Native types through zwp_virtual_keyboard_v1 on mavor's own Wayland
// connection, instead of spawning wtype.
//
// wtype pays a protocol round-trip per keystroke — measured at 4.14 ms a
// character against a real compositor, or 5.6 seconds for a 1300-character
// dictation, which is most of the time the overlay spends saying
// "transcribing". In-process the whole transcript is one batch of requests and
// one flush.
//
// It needs the same protocol wtype needs, so it works exactly where wtype
// worked and nowhere else. A compositor without zwp_virtual_keyboard_manager_v1
// — GNOME, KDE — fails at construction, which is the caller's cue to fall back.
type Native struct {
	Logger *slog.Logger

	// Clipboard also copies each transcript, as with the wtype dispatcher.
	// Off unless the config turns it on.
	Clipboard bool

	// Run executes the clipboard helper. Typing does not use it.
	Run Runner

	mu sync.Mutex
	d  *wayland.Display
	vk *wayland.VirtualKeyboard
}

// NewNative opens its own connection and creates the keyboard.
//
// Its own, rather than sharing the overlay's: the overlay's connection is owned
// exclusively by its render loop, and typing from another goroutine would be a
// second writer on it. A connection is cheap; a data race on a protocol stream
// is not.
func NewNative(log *slog.Logger) (*Native, error) {
	if log == nil {
		log = slog.Default()
	}
	d, err := wayland.Connect()
	if err != nil {
		return nil, err
	}
	vk, err := d.NewVirtualKeyboard()
	if err != nil {
		d.Close()
		return nil, err
	}
	log.Info("output: typing in-process via zwp_virtual_keyboard_v1")
	return &Native{Logger: log, Run: DefaultRunner, d: d, vk: vk}, nil
}

// Emit types text into the focused window, and copies it when configured to.
func (n *Native) Emit(ctx context.Context, text string) error {
	log := n.Logger
	if log == nil {
		log = slog.Default()
	}
	text = CleanText(text)
	if text == "" {
		return nil
	}

	start := time.Now()
	log.Info("output: dispatching", "text_len", len(text), "text_preview", truncate(text, 200))

	n.mu.Lock()
	typeErr := n.vk.Type(text)
	n.mu.Unlock()

	elapsed := time.Since(start)
	var perChar float64
	if len(text) > 0 {
		perChar = float64(elapsed.Microseconds()) / float64(len(text)) / 1000
	}
	log.Info("output: typed in-process",
		"err", fmt.Sprint(typeErr),
		"elapsed_ms", elapsed.Milliseconds(),
		"ms_per_char", fmt.Sprintf("%.3f", perChar))

	if !n.Clipboard {
		return typeErr
	}
	copyStart := time.Now()
	copyErr := n.Run(ctx, "wl-copy", nil, []byte(text))
	log.Info("output: wl-copy done", "err", fmt.Sprint(copyErr), "elapsed_ms", time.Since(copyStart).Milliseconds())
	return errors.Join(typeErr, copyErr)
}

// Close releases the keyboard and the connection.
func (n *Native) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	var errs []error
	if n.vk != nil {
		errs = append(errs, n.vk.Close())
		n.vk = nil
	}
	if n.d != nil {
		errs = append(errs, n.d.Close())
		n.d = nil
	}
	return errors.Join(errs...)
}
