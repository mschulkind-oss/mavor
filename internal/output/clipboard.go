package output

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ClipboardLaunchTimeout bounds helper startup, not selection ownership.
// wl-copy normally forks a holder that survives the launching process.
const ClipboardLaunchTimeout = 3 * time.Second

// Clipboard copies to CLIPBOARD only for later manual paste. It never injects
// keys, reads or restores selections, or changes PRIMARY.
type Clipboard struct{ Run Runner }

// NewClipboard uses ordinary backgrounding wl-copy, without --paste-once.
func NewClipboard() *Clipboard { return &Clipboard{Run: DefaultRunner} }

func (c *Clipboard) Emit(ctx context.Context, text string) error {
	text = CleanText(text)
	if text == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, ClipboardLaunchTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("output: clipboard copy: %w", err)
	}
	run := c.Run
	if run == nil {
		run = DefaultRunner
	}
	err := run(ctx, "wl-copy", []string{"--type", "text/plain;charset=utf-8"}, []byte(text))
	if err != nil {
		return fmt.Errorf("output: clipboard copy: %w", errors.Join(err, ctx.Err()))
	}
	return nil
}
