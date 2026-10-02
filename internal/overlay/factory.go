package overlay

import (
	"log/slog"
	"os"
	"strings"
)

// SelectedBackend identifies the presentation backend without connecting.
func SelectedBackend(desktop string) string {
	for _, token := range strings.Split(desktop, ":") {
		if strings.EqualFold(token, "GNOME") {
			return "x11"
		}
	}
	return "wayland"
}

// NewDefault selects passive XWayland on GNOME, layer-shell otherwise.
// Connection failure is returned; only the daemon may choose a Noop fallback.
func NewDefault(topMargin int, previewFraction float64, log *slog.Logger) (Overlay, error) {
	if SelectedBackend(os.Getenv("XDG_CURRENT_DESKTOP")) == "x11" {
		return NewX11(topMargin, previewFraction, log)
	}
	return NewWL(topMargin, previewFraction, log)
}
