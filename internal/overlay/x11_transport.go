package overlay

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/render"
	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"
)

func localDisplay(display string) (string, error) {
	display = strings.TrimPrefix(display, "unix")
	if !strings.HasPrefix(display, ":") {
		return "", fmt.Errorf("overlay: local Unix DISPLAY required")
	}
	number := strings.TrimPrefix(display, ":")
	number = strings.TrimSuffix(number, ".0")
	n, err := strconv.Atoi(number)
	if err != nil || n < 0 || n > 65535 || strconv.Itoa(n) != number {
		return "", fmt.Errorf("overlay: invalid DISPLAY")
	}
	return number, nil
}
func readCookie(r io.Reader, hostname, display string) ([]byte, error) {
	// Limit both file size and individual fields; never log credential bytes.
	r = io.LimitReader(r, 1<<20)
	for {
		var family uint16
		if err := binary.Read(r, binary.BigEndian, &family); err != nil {
			return nil, fmt.Errorf("overlay: no matching Xauthority cookie: %w", err)
		}
		fields := make([]string, 4)
		for i := range fields {
			var n uint16
			if err := binary.Read(r, binary.BigEndian, &n); err != nil {
				return nil, fmt.Errorf("overlay: truncated Xauthority")
			}
			b := make([]byte, int(n))
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, fmt.Errorf("overlay: truncated Xauthority")
			}
			fields[i] = string(b)
		}
		if (family == 256 && fields[0] == hostname || family == 65535) && (fields[1] == display || fields[1] == "") && fields[2] == "MIT-MAGIC-COOKIE-1" && len(fields[3]) == 16 {
			return []byte(fields[3]), nil
		}
	}
}
func xPixels(img *image.RGBA) []byte { return packXPixels(nil, img) }
func packXPixels(b []byte, img *image.RGBA) []byte {
	size := img.Bounds().Dx() * img.Bounds().Dy() * 4
	if cap(b) < size {
		b = make([]byte, size)
	} else {
		b = b[:size]
	}
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			s := img.Pix[y*img.Stride+x*4:]
			d := b[(y*img.Bounds().Dx()+x)*4:]
			d[0], d[1], d[2], d[3] = s[2], s[1], s[0], s[3]
		}
	}
	return b
}
func uploadRows(w, maxRequest int) (int, error) {
	if w <= 0 || maxRequest*4-24 < w*4 {
		return 0, fmt.Errorf("overlay: X request cannot hold one image row")
	}
	return min(65535, (maxRequest*4-24)/(w*4)), nil
}

type xTransport struct {
	c          *xgb.Conn
	raw        net.Conn
	screen     *xproto.ScreenInfo
	win        xproto.Window
	cmap       xproto.Colormap
	pix        xproto.Pixmap
	gc         xproto.Gcontext
	rect       image.Rectangle
	maxPreview int
	mapped     bool
	eventsDone chan struct{}
	lost       chan struct{}
	pixels     []byte
	canvas     image.Point
	scale      float64
}

func (t *xTransport) atom(name string) (xproto.Atom, error) {
	a, err := xproto.InternAtom(t.c, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	if a == nil {
		return 0, fmt.Errorf("overlay: missing atom reply")
	}
	return a.Atom, nil
}
func (t *xTransport) close() {
	if t != nil {
		_ = t.raw.Close()
		t.c.Close()
		if t.eventsDone != nil {
			<-t.eventsDone
		}
	}
}
func connectX11(margin int, fraction float64) (_ *xTransport, err error) {
	deadline := time.Now().Add(5 * time.Second)
	number, err := localDisplay(os.Getenv("DISPLAY"))
	if err != nil {
		return nil, err
	}
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return nil, e
		}
		path = filepath.Join(home, ".Xauthority")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("overlay: open Xauthority: %w", err)
	}
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		f.Close()
		return nil, fmt.Errorf("overlay: Xauthority must be a bounded regular file")
	}
	hostname, err := os.Hostname()
	if err != nil {
		f.Close()
		return nil, err
	}
	cookie, err := readCookie(f, hostname, number)
	f.Close()
	if err != nil {
		return nil, err
	}
	raw, err := net.DialTimeout("unix", "/tmp/.X11-unix/X"+number, time.Until(deadline))
	if err != nil {
		return nil, err
	}
	_ = raw.SetDeadline(deadline)
	c, err := xgb.NewConnNetWithCookieHex(raw, hex.EncodeToString(cookie))
	if err != nil {
		raw.Close()
		return nil, err
	}
	t := &xTransport{c: c, raw: raw, screen: xproto.Setup(c).DefaultScreen(c)}
	t.eventsDone = make(chan struct{})
	t.lost = make(chan struct{})
	go func() {
		defer close(t.eventsDone)
		defer close(t.lost)
		for {
			e, err := c.WaitForEvent()
			if e == nil && err == nil {
				return
			}
		}
	}()
	defer func() {
		if err != nil {
			t.close()
		} else {
			_ = raw.SetDeadline(time.Time{})
		}
	}()
	if t.screen == nil {
		return nil, fmt.Errorf("overlay: missing X screen")
	}
	if xproto.Setup(c).ImageByteOrder != xproto.ImageOrderLSBFirst {
		return nil, fmt.Errorf("overlay: unsupported X image byte order")
	}
	if err = render.Init(c); err != nil {
		return nil, err
	}
	if err = randr.Init(c); err != nil {
		return nil, err
	}
	if err = shape.Init(c); err != nil {
		return nil, err
	}
	sv, e := shape.QueryVersion(c).Reply()
	if e != nil {
		return nil, e
	}
	if sv == nil || !supportsInputShape(sv.MajorVersion, sv.MinorVersion) {
		return nil, fmt.Errorf("overlay: SHAPE input regions unavailable")
	}
	formats, e := render.QueryPictFormats(c).Reply()
	if e != nil {
		return nil, e
	}
	visual, e := argbVisual(formats)
	if e != nil {
		return nil, e
	}
	packed := false
	for _, format := range xproto.Setup(c).PixmapFormats {
		if format.Depth == 32 && format.BitsPerPixel == 32 && format.ScanlinePad == 32 {
			packed = true
		}
	}
	if !packed {
		return nil, fmt.Errorf("overlay: unsupported depth-32 pixel packing")
	}
	area, e := t.workareaUntil(deadline)
	if e != nil {
		return nil, e
	}
	t.rect, t.canvas, t.maxPreview, e = hudGeometry(area, margin, fraction)
	if e != nil {
		return nil, e
	}
	t.scale = area.Scale
	w, h := t.rect.Dx(), t.rect.Dy()
	if t.win, err = xproto.NewWindowId(c); err != nil {
		return nil, err
	}
	if t.cmap, err = xproto.NewColormapId(c); err != nil {
		return nil, err
	}
	if err = xproto.CreateColormapChecked(c, xproto.ColormapAllocNone, t.cmap, t.screen.Root, visual).Check(); err != nil {
		return nil, err
	}
	if err = xproto.CreateWindowChecked(c, 32, t.win, t.screen.Root, int16(t.rect.Min.X), int16(t.rect.Min.Y), uint16(w), uint16(h), 0, xproto.WindowClassInputOutput, visual, xproto.CwBackPixel|xproto.CwBorderPixel|xproto.CwOverrideRedirect|xproto.CwColormap, []uint32{0, 0, 1, uint32(t.cmap)}).Check(); err != nil {
		return nil, err
	}
	notification, e := t.atom("_NET_WM_WINDOW_TYPE_NOTIFICATION")
	if e != nil {
		return nil, e
	}
	kind, e := t.atom("_NET_WM_WINDOW_TYPE")
	if e != nil {
		return nil, e
	}
	data := make([]byte, 4)
	xgb.Put32(data, uint32(notification))
	if err = xproto.ChangePropertyChecked(c, xproto.PropModeReplace, t.win, kind, xproto.AtomAtom, 32, 1, data).Check(); err != nil {
		return nil, err
	}
	hints, e := t.atom("WM_HINTS")
	if e != nil {
		return nil, e
	}
	data = make([]byte, 36)
	xgb.Put32(data, 1)
	if err = xproto.ChangePropertyChecked(c, xproto.PropModeReplace, t.win, hints, hints, 32, 9, data).Check(); err != nil {
		return nil, err
	}
	if err = shape.RectanglesChecked(c, shape.SoSet, shape.SkInput, xproto.ClipOrderingUnsorted, t.win, 0, 0, nil).Check(); err != nil {
		return nil, err
	}
	regions, e := shape.GetRectangles(c, t.win, shape.SkInput).Reply()
	if e != nil {
		return nil, e
	}
	if regions == nil || len(regions.Rectangles) != 0 {
		return nil, fmt.Errorf("overlay: nonempty input region")
	}
	if t.pix, err = xproto.NewPixmapId(c); err != nil {
		return nil, err
	}
	if err = xproto.CreatePixmapChecked(c, 32, t.pix, xproto.Drawable(t.win), uint16(w), uint16(h)).Check(); err != nil {
		return nil, err
	}
	if t.gc, err = xproto.NewGcontextId(c); err != nil {
		return nil, err
	}
	if err = xproto.CreateGCChecked(c, t.gc, xproto.Drawable(t.pix), xproto.GcGraphicsExposures, []uint32{0}).Check(); err != nil {
		return nil, err
	}
	return t, nil
}
func (t *xTransport) submit(img *image.RGBA) error {
	_ = t.raw.SetDeadline(time.Now().Add(5 * time.Second))
	defer t.raw.SetDeadline(time.Time{})
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	rows, err := uploadRows(w, int(xproto.Setup(t.c).MaximumRequestLength))
	if err != nil {
		return err
	}
	t.pixels = packXPixels(t.pixels, img)
	data := t.pixels
	for y := 0; y < h; y += rows {
		n := min(rows, h-y)
		if err := xproto.PutImageChecked(t.c, xproto.ImageFormatZPixmap, xproto.Drawable(t.pix), t.gc, uint16(w), uint16(n), 0, int16(y), 0, 32, data[y*w*4:(y+n)*w*4]).Check(); err != nil {
			return err
		}
	}
	if err := xproto.CopyAreaChecked(t.c, xproto.Drawable(t.pix), xproto.Drawable(t.win), t.gc, 0, 0, 0, 0, uint16(w), uint16(h)).Check(); err != nil {
		return err
	}
	if !t.mapped {
		if err := xproto.MapWindowChecked(t.c, t.win).Check(); err != nil {
			return err
		}
		if err := xproto.ConfigureWindowChecked(t.c, t.win, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove}).Check(); err != nil {
			return err
		}
		t.mapped = true
	}
	return nil
}

// ProbeX11 validates prerequisites without mapping a window or changing focus.
func ProbeX11() error {
	t, err := connectX11(8, .5)
	if err != nil {
		return err
	}
	t.close()
	return nil
}

func supportsInputShape(major, minor uint16) bool { return major > 1 || major == 1 && minor >= 1 }
func argbVisual(formats *render.QueryPictFormatsReply) (xproto.Visualid, error) {
	if formats != nil && len(formats.Screens) > 0 {
		for _, depth := range formats.Screens[0].Depths {
			if depth.Depth != 32 {
				continue
			}
			for _, v := range depth.Visuals {
				for _, f := range formats.Formats {
					if f.Id == v.Format && f.Type == render.PictTypeDirect && f.Depth == 32 && f.Direct.RedShift == 16 && f.Direct.GreenShift == 8 && f.Direct.BlueShift == 0 && f.Direct.AlphaShift == 24 && f.Direct.RedMask == 255 && f.Direct.GreenMask == 255 && f.Direct.BlueMask == 255 && f.Direct.AlphaMask == 255 {
						return v.Visual, nil
					}
				}
			}
		}
	}
	return 0, fmt.Errorf("overlay: no supported direct ARGB visual")
}
