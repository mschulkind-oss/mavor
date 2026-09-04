//go:build !cgo || nogtk

package overlay

// NewDefault returns a Noop overlay. This variant is used when GTK4 is not
// available — built with -tags nogtk or with CGO disabled. The daemon stays
// functional; just no visible recording indicator.
func NewDefault(int) (Overlay, error) {
	return &Noop{}, nil
}

// Shutdown is a no-op without GTK: there is no application to quit.
func Shutdown() {}
