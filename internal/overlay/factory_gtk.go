//go:build cgo && !nogtk

package overlay

// NewDefault returns the GTK4 overlay built against gtk4-layer-shell. This is
// the production path; the !cgo / nogtk variant in factory_noop.go returns
// a Noop overlay so the daemon still runs (silently) without GTK.
func NewDefault(topMargin int) (Overlay, error) {
	return NewGTK(topMargin)
}
