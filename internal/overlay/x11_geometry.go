package overlay

import (
	"context"
	"fmt"
	"image"
	"math"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"
)

func placeHUD(area image.Rectangle, w, h, margin int) (image.Rectangle, error) {
	if area.Empty() || w <= 0 || h <= 0 || w > area.Dx() || margin < 0 || h+margin > area.Dy() {
		return image.Rectangle{}, fmt.Errorf("overlay: unusable work area %v", area)
	}
	x := area.Min.X + (area.Dx()-w)/2
	return image.Rect(x, area.Min.Y+margin, x+w, area.Min.Y+margin+h), nil
}

type monitorSpec struct{ Connector, Vendor, Product, Serial string }
type monitorMode struct {
	ID                      string
	Width, Height           int32
	Refresh, PreferredScale float64
	Scales                  []float64
	Properties              map[string]dbus.Variant
}
type physicalMonitor struct {
	Spec       monitorSpec
	Modes      []monitorMode
	Properties map[string]dbus.Variant
}
type logicalMonitor struct {
	X, Y       int32
	Scale      float64
	Transform  uint32
	Primary    bool
	Monitors   []monitorSpec
	Properties map[string]dbus.Variant
}
type monitorLayout struct {
	Rect         image.Rectangle
	Primary      bool
	PainterScale float64
}

func mutterLayout(ctx context.Context) ([]monitorLayout, error) {
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if address == "" {
		return nil, fmt.Errorf("overlay: session D-Bus address missing")
	}
	path, err := sessionBusPath(address)
	if err != nil {
		return nil, err
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}
	c, err := dbus.NewConn(raw, dbus.WithContext(ctx))
	if err != nil {
		raw.Close()
		return nil, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if err := c.Auth(nil); err != nil {
		return nil, err
	}
	if err := c.Hello(); err != nil {
		return nil, err
	}
	call := c.Object("org.gnome.Mutter.DisplayConfig", "/org/gnome/Mutter/DisplayConfig").CallWithContext(ctx, "org.gnome.Mutter.DisplayConfig.GetCurrentState", 0)
	if call.Err != nil {
		return nil, call.Err
	}
	if len(call.Body) != 4 {
		return nil, fmt.Errorf("overlay: malformed DisplayConfig reply")
	}
	var physical []physicalMonitor
	var logical []logicalMonitor
	var props map[string]dbus.Variant
	if err := dbus.Store(call.Body[1:], &physical, &logical, &props); err != nil {
		return nil, err
	}
	layout := uint32(1)
	if v, ok := props["layout-mode"]; ok {
		n, ok := v.Value().(uint32)
		if !ok {
			return nil, fmt.Errorf("overlay: invalid layout mode")
		}
		layout = n
	}
	if layout != 1 && layout != 2 {
		return nil, fmt.Errorf("overlay: unsupported layout mode %d", layout)
	}
	var result []monitorLayout
	for _, l := range logical {
		if l.Scale <= 0 || math.IsNaN(l.Scale) || math.IsInf(l.Scale, 0) || l.Transform > 7 || len(l.Monitors) == 0 {
			return nil, fmt.Errorf("overlay: invalid logical monitor")
		}
		w, h := 0, 0
		for _, spec := range l.Monitors {
			found := false
			for _, p := range physical {
				if p.Spec != spec {
					continue
				}
				for _, m := range p.Modes {
					if current, ok := m.Properties["is-current"]; ok && current.Value() == true {
						mw, mh := int(m.Width), int(m.Height)
						if l.Transform%2 == 1 {
							mw, mh = mh, mw
						}
						if layout == 1 {
							mw = int(math.Round(float64(mw) / l.Scale))
							mh = int(math.Round(float64(mh) / l.Scale))
						}
						if w != 0 && (w != mw || h != mh) {
							return nil, fmt.Errorf("overlay: ambiguous mirrored monitor geometry")
						}
						w, h = mw, mh
						found = true
					}
				}
			}
			if !found {
				return nil, fmt.Errorf("overlay: missing current monitor mode")
			}
		}
		if w <= 0 || h <= 0 {
			return nil, fmt.Errorf("overlay: empty monitor mode")
		}
		scale := 1.0
		if layout == 2 {
			scale = l.Scale
		}
		result = append(result, monitorLayout{image.Rect(int(l.X), int(l.Y), int(l.X)+w, int(l.Y)+h), l.Primary, scale})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("overlay: no active monitors")
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Rect.Min.X != result[j].Rect.Min.X {
			return result[i].Rect.Min.X < result[j].Rect.Min.X
		}
		return result[i].Rect.Min.Y < result[j].Rect.Min.Y
	})
	return result, nil
}

func scaledRect(r image.Rectangle, f float64) image.Rectangle {
	return image.Rect(int(math.Round(float64(r.Min.X)*f)), int(math.Round(float64(r.Min.Y)*f)), int(math.Round(float64(r.Max.X)*f)), int(math.Round(float64(r.Max.Y)*f)))
}

// coordinateFactor matches complete layouts, not DPI or the primary scale.
// XWayland can multiply all logical positions by ceil(highest output scale).
func coordinateFactor(logical, xs []image.Rectangle) (float64, error) {
	if len(logical) == 0 || len(logical) != len(xs) {
		return 0, fmt.Errorf("overlay: X/Mutter monitor count mismatch")
	}
	var candidates []float64
	for _, x := range xs {
		if !logical[0].Empty() {
			f := float64(x.Dx()) / float64(logical[0].Dx())
			if f > 0 && f <= 8 && math.Abs(float64(x.Dy())-float64(logical[0].Dy())*f) <= 1 {
				candidates = append(candidates, f)
			}
		}
	}
	var match float64
	for _, f := range candidates {
		used := make([]bool, len(xs))
		valid := true
		for _, l := range logical {
			found := false
			for i, x := range xs {
				if !used[i] && scaledRect(l, f) == x {
					used[i] = true
					found = true
					break
				}
			}
			if !found {
				valid = false
				break
			}
		}
		if valid {
			if match != 0 && math.Abs(match-f) > 1e-6 {
				return 0, fmt.Errorf("overlay: ambiguous X coordinate scale")
			}
			match = f
		}
	}
	if match == 0 {
		return 0, fmt.Errorf("overlay: inconsistent X/Mutter coordinates")
	}
	return match, nil
}

type xGeometry struct {
	Area  image.Rectangle
	Scale float64
}

func (t *xTransport) property(name string) ([]uint32, error) {
	a, err := t.atom(name)
	if err != nil {
		return nil, err
	}
	p, err := xproto.GetProperty(t.c, false, t.screen.Root, a, xproto.AtomCardinal, 0, 1024).Reply()
	if err != nil {
		return nil, err
	}
	if p == nil || p.Type != xproto.AtomCardinal || p.Format != 32 || p.BytesAfter != 0 || len(p.Value)%4 != 0 {
		return nil, fmt.Errorf("overlay: absent/malformed %s", name)
	}
	values := make([]uint32, len(p.Value)/4)
	for i := range values {
		values[i] = xgb.Get32(p.Value[i*4:])
	}
	return values, nil
}
func (t *xTransport) workarea() (xGeometry, error) {
	return t.workareaUntil(time.Now().Add(5 * time.Second))
}
func (t *xTransport) workareaUntil(deadline time.Time) (xGeometry, error) {
	deadline = minDeadline(deadline, time.Now().Add(2*time.Second))
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	layout, err := mutterLayout(ctx)
	if err != nil {
		return xGeometry{}, err
	}
	q, err := randr.GetMonitors(t.c, t.screen.Root, true).Reply()
	if err != nil {
		return xGeometry{}, err
	}
	if q == nil {
		return xGeometry{}, fmt.Errorf("overlay: no X monitors")
	}
	var xs, logical []image.Rectangle
	for _, m := range q.Monitors {
		xs = append(xs, image.Rect(int(m.X), int(m.Y), int(m.X)+int(m.Width), int(m.Y)+int(m.Height)))
	}
	primary := 0
	for i, l := range layout {
		logical = append(logical, l.Rect)
		if l.Primary {
			primary = i
		}
	}
	f, err := coordinateFactor(logical, xs)
	if err != nil {
		return xGeometry{}, err
	}
	target := scaledRect(layout[primary].Rect, f)
	desktop, err := t.property("_NET_CURRENT_DESKTOP")
	var index uint32
	if err == nil && len(desktop) == 1 {
		index = desktop[0]
	} else {
		areas, e := t.property("_NET_WORKAREA")
		if e != nil || len(areas) < 4 || len(areas)%4 != 0 {
			return xGeometry{}, fmt.Errorf("overlay: ambiguous workspace geometry")
		}
		for i := 4; i < len(areas); i++ {
			if areas[i] != areas[i%4] {
				return xGeometry{}, fmt.Errorf("overlay: differing workspace workareas without current desktop")
			}
		}
		if len(areas)/4 > 64 {
			return xGeometry{}, fmt.Errorf("overlay: excessive desktop count")
		}
		var perDesktop [][]uint32
		for d := 0; d < len(areas)/4; d++ {
			a, e := t.property(fmt.Sprintf("_GTK_WORKAREAS_D%d", d))
			if e != nil {
				return xGeometry{}, e
			}
			perDesktop = append(perDesktop, a)
		}
		if !sameWorkareas(perDesktop) {
			return xGeometry{}, fmt.Errorf("overlay: differing monitor workareas without current desktop")
		}

	}
	values, err := t.property(fmt.Sprintf("_GTK_WORKAREAS_D%d", index))
	if err != nil {
		return xGeometry{}, err
	}
	if len(values) != len(xs)*4 {
		return xGeometry{}, fmt.Errorf("overlay: X workarea count mismatch")
	}
	var area image.Rectangle
	for i := 0; i < len(values); i += 4 {
		x, y := int(int32(values[i])), int(int32(values[i+1]))
		w, h := int(values[i+2]), int(values[i+3])
		r := image.Rect(x, y, x+w, y+h)
		if !r.Empty() && r.In(target) {
			if !area.Empty() {
				return xGeometry{}, fmt.Errorf("overlay: ambiguous primary workarea")
			}
			area = r
		}
	}
	if area.Empty() {
		return xGeometry{}, fmt.Errorf("overlay: no primary monitor workarea")
	}
	return xGeometry{Area: area, Scale: f * layout[primary].PainterScale}, nil
}
func hudGeometry(g xGeometry, margin int, fraction float64) (image.Rectangle, image.Point, int, error) {
	preview := int(float64(g.Area.Dx()) / g.Scale * fraction)
	w, h, err := FixedSurfaceSize(preview)
	if err != nil {
		return image.Rectangle{}, image.Point{}, 0, err
	}
	rect, err := placeHUD(g.Area, int(math.Round(float64(w)*g.Scale)), int(math.Round(float64(h)*g.Scale)), int(math.Round(float64(margin)*g.Scale)))
	if err == nil && (rect.Min.X < -32768 || rect.Min.Y < -32768 || rect.Max.X > 32767 || rect.Max.Y > 32767) {
		err = fmt.Errorf("overlay: geometry exceeds X protocol coordinates")
	}
	return rect, image.Pt(w, h), preview, err
}

// Only the current local session bus is used, with an owned bounded socket.
func sessionBusPath(address string) (string, error) {
	if !strings.HasPrefix(address, "unix:") || strings.Contains(address, ";") {
		return "", fmt.Errorf("overlay: local Unix session bus required")
	}
	fields := strings.Split(strings.TrimPrefix(address, "unix:"), ",")
	var path, abstract string
	for _, field := range fields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return "", fmt.Errorf("overlay: invalid bus address")
		}
		v, err := url.PathUnescape(value)
		if err != nil {
			return "", err
		}
		switch key {
		case "path":
			path = v
		case "abstract":
			abstract = v
		}
	}
	if path != "" && abstract == "" && strings.HasPrefix(path, "/") {
		return path, nil
	}
	if abstract != "" && path == "" {
		return "@" + abstract, nil
	}
	return "", fmt.Errorf("overlay: ambiguous/absent session bus path")
}

func sameWorkareas(areas [][]uint32) bool {
	if len(areas) == 0 || len(areas[0]) == 0 || len(areas[0])%4 != 0 {
		return false
	}
	for _, area := range areas {
		if len(area) != len(areas[0]) {
			return false
		}
		for i, v := range area {
			if areas[0][i] != v {
				return false
			}
		}
	}
	return true
}

func minDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
