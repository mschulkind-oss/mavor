package overlay

import "testing"

func TestDesktopBackend(t *testing.T) {
	for _, tc := range []struct{ desktop, backend string }{{"GNOME", "x11"}, {"ubuntu:gnome", "x11"}, {"GNOME-Classic:GNOME", "x11"}, {"", "wayland"}, {"sway", "wayland"}, {"notgnome", "wayland"}} {
		if got := SelectedBackend(tc.desktop); got != tc.backend {
			t.Errorf("%q: %q", tc.desktop, got)
		}
	}
}
