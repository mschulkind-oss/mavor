//go:build integration

package integration

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/wayland"
)

// The Wayland client is hand-written against the protocol XML, and the parts
// that go wrong when hand-written — opcode numbers, argument order, the
// configure/ack handshake, the shared-memory handoff — all fail silently or as
// an opaque disconnect rather than as a compile error. The only proof that
// works is putting known pixels on a real compositor and reading them back.
func TestWaylandClientPaintsALayerSurface(t *testing.T) {
	h := Start(t, Options{})

	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)

	d, err := wayland.Connect()
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer d.Close()

	const (
		width  = 300
		height = 60
		margin = 40
	)

	s, err := d.NewSurface("mavor-test", wayland.LayerTop, wayland.AnchorTop, width, height)
	if err != nil {
		t.Fatalf("create layer surface: %v", err)
	}
	if err := s.SetMargin(margin, 0, 0, 0); err != nil {
		t.Fatalf("set margin: %v", err)
	}
	if err := s.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// The compositor assigns the size; attaching before acking its configure
	// is a protocol error, so this wait is load-bearing rather than a timing
	// convenience.
	if err := s.WaitConfigure(); err != nil {
		t.Fatalf("wait for configure: %v", err)
	}
	if s.Width != width || s.Height != height {
		t.Fatalf("compositor sized the surface %dx%d, want %dx%d", s.Width, s.Height, width, height)
	}

	buf, err := d.NewBuffer(s.Width, s.Height)
	if err != nil {
		t.Fatalf("allocate buffer: %v", err)
	}
	defer buf.Close()

	// Opaque magenta: a colour that appears nowhere in sway's default output,
	// so finding it proves it came from this client.
	const wantR, wantG, wantB = 0xff, 0x00, 0xff
	for i := 0; i+3 < len(buf.Pix); i += 4 {
		buf.Pix[i+0] = wantB // ARGB8888 little-endian is B, G, R, A in bytes
		buf.Pix[i+1] = wantG
		buf.Pix[i+2] = wantR
		buf.Pix[i+3] = 0xff
	}

	if err := s.Attach(buf); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if err := d.Roundtrip(); err != nil {
		t.Fatalf("roundtrip after attach: %v", err)
	}

	img, err := png.Decode(bytes.NewReader(h.Grim()))
	if err != nil {
		t.Fatalf("decode screenshot: %v", err)
	}

	// The surface is anchored to the top edge and pushed down by the margin,
	// so its band sits at y in [margin, margin+height).
	probe := image.Pt(img.Bounds().Dx()/2, margin+height/2)
	r, g, b, _ := img.At(probe.X, probe.Y).RGBA()
	gotR, gotG, gotB := r>>8, g>>8, b>>8
	if gotR != wantR || gotG != wantG || gotB != wantB {
		writeArtifact(t, "wayland-client-paint.png", h.Grim())
		t.Errorf("pixel at %v = #%02x%02x%02x, want #%02x%02x%02x — the surface did not reach the screen",
			probe, gotR, gotG, gotB, wantR, wantG, wantB)
	}

	// Above the margin must still be the compositor's background, which is
	// what proves the margin was applied rather than the surface just
	// covering everything.
	r, g, b, _ = img.At(probe.X, margin/2).RGBA()
	if r>>8 == wantR && g>>8 == wantG && b>>8 == wantB {
		t.Errorf("the painted colour appears above the %dpx margin — set_margin had no effect", margin)
	}

	if err := s.Destroy(); err != nil {
		t.Fatalf("destroy: %v", err)
	}
}

func writeArtifact(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll("../reports/screenshots", 0o755); err != nil {
		return
	}
	_ = os.WriteFile("../reports/screenshots/"+name, data, 0o644)
	t.Logf("wrote ../reports/screenshots/%s", name)
}
