//go:build integration

package integration

import (
	"bytes"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/test/desktop"
)

func TestDaemonInitializationLifecycleDesktop(t *testing.T) {
	if os.Getenv("MAVOR_READINESS_MODEL_DIR") == "" {
		t.Skip("existing mounted models required")
	}
	h := StartWithBar(t)
	stageDesktop(t, h)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)
	t.Setenv("XDG_CURRENT_DESKTOP", "sway")
	o, e := overlay.NewDefault(8, .5, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer o.Close()
	root := os.Getenv("MAVOR_VISUAL_SCRATCH")
	if root == "" {
		root = t.TempDir()
	}
	dir, e := os.MkdirTemp(root, "lifecycle-sway-")
	if e != nil {
		t.Fatal(e)
	}
	t.Log("runtime captures", dir)
	_ = o.Show(overlay.Hidden)
	_, e = o.(overlay.FrameObserver).SyncFrame(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	base, e := png.Decode(bytes.NewReader(h.Grim()))
	if e != nil {
		t.Fatal(e)
	}
	capture := func(name string, visual overlay.Visual, diagnostic bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		r, e := o.(overlay.FrameObserver).SyncFrame(ctx)
		if e != nil {
			t.Fatal(e)
		}
		time.Sleep(150 * time.Millisecond)
		raw := h.Grim()
		img, e := png.Decode(bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		if r.Scene.Visual != visual || diagnostic && r.Scene.Preview == "" {
			t.Fatalf("lost runtime state/subtitle: %+v", r)
		}
		if _, e = desktop.Validate(base, img, r); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, name+".png"), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	for _, fail := range []bool{false, true} {
		l, e := desktop.StartLifecycle(o, fail)
		if e != nil {
			t.Fatal(e)
		}
		capture("initializing", overlay.Initializing, false)
		l.Release()
		r, e := l.Await()
		if e != nil {
			_ = l.Close()
			t.Fatal(e)
		}
		if fail {
			t.Log("original failure", r.Error)
			capture("error", overlay.Error, true)
		} else {
			capture("ready", overlay.Hidden, false)
		}
		if e = l.Close(); e != nil {
			t.Fatal(e)
		}
	}
	result, cleanup, e := desktop.RunBackup(o)
	if e != nil {
		t.Fatal(e)
	}
	capture("degraded-backup", overlay.Degraded, true)
	if e = cleanup(); e != nil {
		t.Fatal(e)
	}
	t.Logf("controlled backup: %+v child_reaped=true", result)

}

func TestDesktopGPUFailureChild(t *testing.T) { desktop.GPUFailureChild() }
