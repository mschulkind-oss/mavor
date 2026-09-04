package overlay

import "log/slog"

// NewDefault returns the overlay the daemon should use. It draws straight onto
// a wlr-layer-shell surface, so it needs a Wayland compositor that implements
// that protocol — sway, hyprland, river and the rest of wlroots — and nothing
// else. There is no C library and no cgo behind it.
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
