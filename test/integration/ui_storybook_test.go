//go:build integration || e2e

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/test/desktop"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUIStorybookReport(t *testing.T) {
	h := StartWithBar(t)
	stageDesktop(t, h)
	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", h.DBusAddr)
	o, err := overlay.NewDefault(8, testPreviewWidth, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	dir, err := filepath.Abs("../reports")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	if root := os.Getenv("MAVOR_VISUAL_SCRATCH"); root != "" {
		scratch, err = os.MkdirTemp(root, "sway-")
		if err != nil {
			t.Fatal(err)
		}
		t.Log("retained captures", scratch)
	}
	var evidence []desktop.Evidence
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, s := range desktop.Scenes() {
		r, err := desktop.Drive(ctx, o, s)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		started := time.Now()
		raw := h.Grim()
		ended := time.Now()
		candidates := o.(overlay.FrameHistory).RecentFrames()
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err = stagedFrame(img); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(scratch, s.ID+".png")
		if err = os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
		evidence = append(evidence, desktop.Evidence{ID: s.ID, File: path, Receipt: r, Candidates: candidates, CaptureStarted: started, CaptureEnded: ended})
	}
	hidden := desktop.Scenes()[0]
	r, err := desktop.Drive(ctx, o, hidden)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	path := filepath.Join(scratch, "hidden-after-preview.png")
	if err = os.WriteFile(path, h.Grim(), 0644); err != nil {
		t.Fatal(err)
	}
	if err = desktop.ValidateArtifact(evidence[0].File, desktop.Evidence{ID: "hidden-after-preview", File: path, Receipt: r}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MAVOR_VISUAL_SCRATCH") != "" {
		data, e := json.Marshal(evidence)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(scratch, "evidence.json"), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if err = desktop.BuildReport(dir, "ui-storybook", "Sway", "Grim · real layer-shell surface", "Waybar 32px + production margin 8px", evidence); err != nil {
		t.Fatal(err)
	}
}
