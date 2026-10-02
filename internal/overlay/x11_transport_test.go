package overlay

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/render"
	"image"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalDisplay(t *testing.T) {
	for _, d := range []string{"", "host:0", "localhost:0", ":-1", ":0.1", ":0junk"} {
		if _, err := localDisplay(d); err == nil {
			t.Errorf("accepted %q", d)
		}
	}
	for _, d := range []string{":0", "unix:12", ":12.0"} {
		if _, err := localDisplay(d); err != nil {
			t.Error(err)
		}
	}
}
func authorityRecord(family uint16, fields ...string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, family)
	for _, s := range fields {
		_ = binary.Write(&b, binary.BigEndian, uint16(len(s)))
		b.WriteString(s)
	}
	return b.Bytes()
}
func TestAuthority(t *testing.T) {
	cookie := "0123456789abcdef"
	data := authorityRecord(256, "machine", "2", "MIT-MAGIC-COOKIE-1", cookie)
	if got, err := readCookie(bytes.NewReader(data), "machine", "2"); err != nil || string(got) != cookie {
		t.Fatalf("%x %v", got, err)
	}
	for _, bad := range [][]byte{nil, data[:len(data)-1], authorityRecord(256, "machine", "3", "MIT-MAGIC-COOKIE-1", cookie), authorityRecord(256, "machine", "2", "MIT-MAGIC-COOKIE-1", "bad")} {
		if _, err := readCookie(bytes.NewReader(bad), "machine", "2"); err == nil {
			t.Fatal("accepted invalid authority")
		}
	}
}
func TestXPixelPacking(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	copy(img.Pix, []byte{1, 2, 3, 4})
	if got := xPixels(img); !bytes.Equal(got, []byte{3, 2, 1, 4}) {
		t.Fatalf("%v", got)
	}
	if rows, err := uploadRows(200, 65535); err != nil || rows*200*4+24 > 65535*4 || rows < 1 {
		t.Fatalf("%d %v", rows, err)
	}
	if _, err := uploadRows(65535, 10); err == nil {
		t.Fatal("oversized row accepted")
	}
}

func TestMutterAuthorityEmptyDisplay(t *testing.T) {
	data := authorityRecord(256, "machine", "", "MIT-MAGIC-COOKIE-1", "0123456789abcdef")
	if _, err := readCookie(bytes.NewReader(data), "machine", "9"); err != nil {
		t.Fatal(err)
	}
}

func TestXHandshakeStallIsBounded(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(30 * time.Millisecond))
	start := time.Now()
	c, err := xgb.NewConnNetWithCookieHex(client, "30313233343536373839616263646566")
	if c != nil {
		c.Close()
	}
	client.Close()
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("unbounded or accepted stall: %v", err)
	}
}

func TestX11UnavailablePrerequisites(t *testing.T) {
	t.Setenv("DISPLAY", "")
	if _, err := NewX11(8, .5, nil); err == nil {
		t.Fatal("missing DISPLAY accepted")
	}
	t.Setenv("DISPLAY", ":0")
	t.Setenv("XAUTHORITY", filepath.Join(t.TempDir(), "missing"))
	if _, err := NewX11(8, .5, nil); err == nil {
		t.Fatal("missing Xauthority accepted")
	}
}

func TestSessionBusAddress(t *testing.T) {
	for _, bad := range []string{"", "tcp:host=example.com,port=123", "unix:path=", "unix:path=/a,abstract=b", "unix:path=%ZZ"} {
		if _, err := sessionBusPath(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	for _, tc := range []struct{ address, path string }{{"unix:path=/run/user/1000/bus,guid=123", "/run/user/1000/bus"}, {"unix:abstract=/tmp/bus-abc", "@/tmp/bus-abc"}, {"unix:path=/tmp/a%20b", "/tmp/a b"}} {
		p, e := sessionBusPath(tc.address)
		if e != nil || p != tc.path {
			t.Fatalf("%q %v", p, e)
		}
	}
}

func TestARGBVisualCapability(t *testing.T) {
	q := &render.QueryPictFormatsReply{Formats: []render.Pictforminfo{{Id: 1, Type: render.PictTypeDirect, Depth: 32, Direct: render.Directformat{RedShift: 16, GreenShift: 8, BlueShift: 0, AlphaShift: 24, RedMask: 255, GreenMask: 255, BlueMask: 255, AlphaMask: 255}}}, Screens: []render.Pictscreen{{Depths: []render.Pictdepth{{Depth: 32, Visuals: []render.Pictvisual{{Visual: 2, Format: 1}}}}}}}
	if v, err := argbVisual(q); err != nil || v != 2 {
		t.Fatalf("%v %v", v, err)
	}
	q.Formats[0].Direct.AlphaMask = 0
	if _, err := argbVisual(q); err == nil {
		t.Fatal("opaque visual accepted")
	}
	if _, err := argbVisual(nil); err == nil {
		t.Fatal("absent RENDER reply accepted")
	}
	for _, v := range []struct {
		major, minor uint16
		ok           bool
	}{{1, 0, false}, {1, 1, true}, {0, 99, false}, {2, 0, true}} {
		if supportsInputShape(v.major, v.minor) != v.ok {
			t.Fatalf("SHAPE %d.%d", v.major, v.minor)
		}
	}
}

func TestSessionBusAuthStallIsBounded(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(30 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	conn, err := dbus.NewConn(client, dbus.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	if err := conn.Auth(nil); err == nil || time.Since(start) > time.Second {
		t.Fatalf("auth stall accepted/unbounded: %v", err)
	}
}
