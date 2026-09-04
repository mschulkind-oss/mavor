package overlay

import "log/slog"

// NewDefault returns the overlay the daemon should use. Today that is the one
// backend there is: a wlr-layer-shell surface, needing a compositor that
// implements the protocol — sway, hyprland, river and the rest of wlroots.
// There is no C library and no cgo behind it.
//
// Overlay is the seam a second backend would arrive through, so this is the
// only place that has to learn how to choose between them.
//
// A compositor without layer-shell, or no compositor at all, is not fatal:
// dictation works fine without a visual indicator, so the caller may fall back
// to Noop rather than refusing to start.
func NewDefault(topMargin int, log *slog.Logger) (Overlay, error) {
	return NewWL(topMargin, log)
}

// Shutdown exists for symmetry with the daemon's teardown path. The overlay
// owns nothing process-wide, so there is nothing to tear down: each overlay's
// own Close releases its connection.
func Shutdown() {}
