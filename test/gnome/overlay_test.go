//go:build gnome

package gnome

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/test/desktop"
)

type overlayCommand struct {
	Command   string   `json:"command"`
	Version   int      `json:"version"`
	Sequence  uint64   `json:"sequence"`
	Visual    string   `json:"visual"`
	Level     *float64 `json:"level"`
	Text      *string  `json:"text"`
	TimeoutMS int      `json:"timeout_ms"`
}

// TestOverlayChild is a generic process wrapping the production Overlay.
// Child stdout is exclusively protocol JSON; diagnostics are stderr.
func TestOverlayChild(t *testing.T) {
	if os.Getenv("MAVOR_GNOME_OVERLAY_CHILD") != "1" {
		return
	}
	if err := serveOverlay(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func serveOverlay() error {
	o, err := overlay.NewDefault(8, .5, nil)
	if err != nil {
		return err
	}
	defer o.Close()
	observer, ok := o.(overlay.FrameObserver)
	if !ok {
		return fmt.Errorf("real backend lacks FrameObserver")
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	encoder := json.NewEncoder(os.Stdout)
	var last uint64
	visual := overlay.Hidden
	var backupClose func() error
	defer func() {
		if backupClose != nil {
			_ = backupClose()
		}
	}()
	var lifecycle *desktop.Lifecycle
	defer func() {
		if lifecycle != nil {
			_ = lifecycle.Close()
		}
	}()
	ready := false
	applied := false
	for scanner.Scan() {
		var command overlayCommand
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&command); err != nil {
			return err
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return fmt.Errorf("trailing command data")
		}
		if command.Command == "ready" {
			if ready || command.Version != 1 {
				return fmt.Errorf("duplicate ready or unsupported protocol version")
			}
			ready = true
			if err := encoder.Encode(map[string]any{"version": 1, "backend": overlay.SelectedBackend(os.Getenv("XDG_CURRENT_DESKTOP")), "status": "ready"}); err != nil {
				return err
			}
			continue
		}
		if !ready {
			return fmt.Errorf("ready required first")
		}
		if command.Sequence <= last {
			return fmt.Errorf("sequence must strictly increase")
		}
		last = command.Sequence
		switch command.Command {
		case "backup_run":
			evidence, cleanup, e := desktop.RunBackup(o)
			if e != nil {
				return e
			}
			backupClose = cleanup
			err = encoder.Encode(map[string]any{"sequence": last, "evidence": evidence})
		case "backup_stop":
			if e := backupClose(); e != nil {
				return e
			}
			backupClose = nil
			err = encoder.Encode(map[string]any{"sequence": last, "status": "stopped", "child_reaped": true})
		case "daemon_start":
			if lifecycle != nil {
				return fmt.Errorf("lifecycle already running")
			}
			lifecycle, err = desktop.StartLifecycle(o, command.Visual == "error")
			if err != nil {
				return err
			}
			err = encoder.Encode(map[string]any{"sequence": last, "status": "initializing"})
		case "daemon_request":
			if e := lifecycle.RequestNotice(); e != nil {
				return e
			}
			err = encoder.Encode(map[string]any{"sequence": last, "status": "initializing"})
		case "daemon_release":
			lifecycle.Release()
			response, e := lifecycle.Await()
			if e != nil {
				return e
			}
			err = encoder.Encode(map[string]any{"sequence": last, "state": response.State, "error": response.Error})
		case "daemon_stop":
			if e := lifecycle.Close(); e != nil {
				return e
			}
			lifecycle = nil
			err = encoder.Encode(map[string]any{"sequence": last, "status": "stopped"})
		case "apply":
			next := visual
			switch command.Visual {
			case "":
			case "hidden":
				next = overlay.Hidden
			case "recording":
				next = overlay.Recording
			case "transcribing":
				next = overlay.Transcribing
			case "error":
				next = overlay.Error
			case "initializing":
				next = overlay.Initializing
			case "degraded":
				next = overlay.Degraded
			default:
				return fmt.Errorf("invalid visual")
			}
			if command.Level != nil {
				v := *command.Level
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
					return fmt.Errorf("level outside finite [0,1]")
				}
			}
			if next != visual || !applied {
				if err := o.Show(next); err != nil {
					return err
				}
				visual = next
			}
			applied = true
			if command.Level != nil {
				if err := o.SetLevel(*command.Level); err != nil {
					return err
				}
			}
			if command.Text != nil {
				if err := o.SetText(*command.Text); err != nil {
					return err
				}
			}
			err = encoder.Encode(map[string]any{"sequence": last, "status": "queued"})
		case "await_frame":
			if command.TimeoutMS < 1 || command.TimeoutMS > 5000 {
				return fmt.Errorf("timeout_ms outside [1,5000]")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(command.TimeoutMS)*time.Millisecond)
			receipt, e := observer.SyncFrame(ctx)
			cancel()
			if e != nil {
				return e
			}
			err = encoder.Encode(map[string]any{"sequence": last, "receipt": receipt})
		case "recent_frames":
			history, ok := o.(overlay.FrameHistory)
			if !ok {
				return fmt.Errorf("frame history required")
			}
			err = encoder.Encode(map[string]any{"sequence": last, "receipts": history.RecentFrames()})
		case "close":
			if err := o.Close(); err != nil {
				return err
			}
			return encoder.Encode(map[string]any{"sequence": last, "status": "closed"})
		default:
			return fmt.Errorf("unknown command")
		}
		if err != nil {
			return err
		}
	}
	return scanner.Err()
}

func runOverlayHarness(t *testing.T, missing bool, args ...string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	script := "overlay_harness.py"
	if len(args) > 0 && args[0] == "coexistence" {
		script = "coexistence_harness.py"
		args = args[1:]
	}
	command := append([]string{"supervise.py", "python3", script, binary}, args...)
	cmd := exec.CommandContext(ctx, "python3", command...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	if missing {
		cmd.Env = append(os.Environ(), "MAVOR_GNOME_SHELL=/nonexistent/mavor-overlay-shell")
	}
	out, err := cmd.CombinedOutput()
	t.Log(string(out))
	if missing {
		if err == nil || !strings.Contains(string(out), "mavor-overlay-shell") || !strings.Contains(string(out), "Supervisor reaped") {
			t.Fatalf("missing prerequisite/cleanup not proven: %v", err)
		}
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Diagnostics: ") {
			return strings.TrimPrefix(line, "Diagnostics: ")
		}
	}
	t.Fatal("no artifact directory")
	return ""
}
func TestGNOMEOverlayMissingPrerequisite(t *testing.T) { runOverlayHarness(t, true) }
func TestGNOMEOverlay(t *testing.T) {
	root := runOverlayHarness(t, false)
	var receipts map[string]overlay.FrameReceipt
	b, err := os.ReadFile(filepath.Join(root, "receipts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &receipts); err != nil {
		t.Fatal(err)
	}

	order := []string{"baseline", "recording", "preview", "preview-long", "preview-cleared", "transcribing", "error", "hidden", "recording-again", "fullscreen", "overview-exit", "workspace-other"}
	if len(receipts) != len(order) {
		t.Fatalf("missing production states: %d", len(receipts))
	}
	var lastFrame, lastRevision uint64
	for _, name := range order {
		r, ok := receipts[name]
		if !ok {
			t.Fatalf("missing receipt %s", name)
		}
		expected := overlay.Recording
		switch name {
		case "baseline", "hidden":
			expected = overlay.Hidden
		case "transcribing":
			expected = overlay.Transcribing
		case "error":
			expected = overlay.Error
		}
		if r.Scene.Visual != expected {
			t.Fatalf("%s receipt has wrong visual %v", name, r.Scene.Visual)
		}
		if name != "baseline" && (r.Frame <= lastFrame || r.Revision <= lastRevision) {
			t.Fatalf("stale state receipt %s", name)
		}
		lastFrame, lastRevision = r.Frame, r.Revision
	}
	baselineFile, e := os.Open(filepath.Join(root, "baseline.png"))
	if e != nil {
		t.Fatal(e)
	}
	baseline, e := png.Decode(baselineFile)
	baselineFile.Close()
	if e != nil {
		t.Fatal(e)
	}
	for name, r := range receipts {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(root, name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			img, err := png.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			if img.Bounds().Dx() != 1920 || img.Bounds().Dy() != 1080 {
				t.Fatal("wrong actual compositor dimensions")
			}
			base := baseline
			if name == "fullscreen" {
				f, e := os.Open(filepath.Join(root, "fullscreen-baseline.png"))
				if e != nil {
					t.Fatal(e)
				}
				base, e = png.Decode(f)
				f.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
			bounds, err := validateOverlayCapture(base, img, r)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("actual frame=%d revision=%d screenshot=%v X-protocol=%v", r.Frame, r.Revision, bounds, r.Screen)
		})
	}
	if !t.Failed() {
		fmt.Println("Production GNOME visual and clearing proof PASS")
	}
}

// Use the same screenshot-space oracle as both storybooks. Screen remains X
// protocol metadata, never an oracle placement assertion.
func validateOverlayCapture(baseline, img image.Image, r overlay.FrameReceipt) (image.Rectangle, error) {
	return desktop.Validate(baseline, img, r)
}

func TestGNOMEOverlayGeometry(t *testing.T) {
	root := runOverlayHarness(t, false, "geometry")
	var receipts map[string]struct {
		overlay.FrameReceipt
		CaptureStageWidth float64 `json:"capture_stage_width"`
	}
	b, err := os.ReadFile(filepath.Join(root, "geometry-receipts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &receipts); err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 6 {
		t.Fatal("missing geometry cases")
	}
	for name, r := range receipts {
		t.Run(name, func(t *testing.T) {
			f, e := os.Open(filepath.Join(root, name+".png"))
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			im, e := png.Decode(f)
			if e != nil {
				t.Fatal(e)
			}
			// GNOME Screenshot captures the logical stage at its highest render scale;
			// receipt.Screen is explicitly X protocol coordinates, not PNG coordinates.
			factor := float64(im.Bounds().Dx()) / r.CaptureStageWidth / r.Scale
			red := 0
			for y := int(float64(r.Screen.Min.Y) * factor); y < int(float64(r.Screen.Max.Y)*factor); y++ {
				for x := int(float64(r.Screen.Min.X) * factor); x < int(float64(r.Screen.Max.X)*factor); x++ {
					rr, g, b, _ := im.At(x, y).RGBA()
					if rr > 20000 && rr > g*2 && rr > b*2 {
						red++
					}
				}
			}
			if red < 1000 {
				t.Fatalf("actual HUD absent from predicted primary workarea: %d factor=%v", red, factor)
			}
			t.Logf("actual %dx%d capture; X-scale=%v capture/X=%v red=%d", im.Bounds().Dx(), im.Bounds().Dy(), r.Scale, factor, red)
		})
	}
}

// Keep the original missing-preview regression, now against the common oracle.
func TestPreviewProofRejectsMissingText(t *testing.T) {
	scene := overlay.Scene{Visual: overlay.Recording, SurfaceW: 960, SurfaceH: 101, MaxPreviewWidth: 960, Levels: make([]float64, 46), Preview: "permanent proof text"}
	baseline := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	empty := scene
	empty.Preview = ""
	missing, err := overlay.Render(empty)
	if err != nil {
		t.Fatal(err)
	}
	actual := image.NewRGBA(baseline.Bounds())
	draw.Draw(actual, missing.Bounds().Add(image.Pt(480, 40)), missing, image.Point{}, draw.Over)
	r := overlay.FrameReceipt{Backend: "x11", Status: "submitted", Frame: 1, Revision: 1, Scale: 1, Scene: scene, Canvas: image.Pt(960, 101)}
	if _, err := validateOverlayCapture(baseline, actual, r); err == nil {
		t.Fatal("missing preview passed")
	}
}

func TestGNOMEHUDClipboardCoexistence(t *testing.T) {
	root := runOverlayHarness(t, false, "coexistence")
	b, err := os.ReadFile(filepath.Join(root, "coexistence-receipts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipts map[string]overlay.FrameReceipt
	if err = json.Unmarshal(b, &receipts); err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 8 {
		t.Fatalf("missing combined captures: %d", len(receipts))
	}
	read := func(name string) image.Image {
		f, err := os.Open(filepath.Join(root, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		img, err := png.Decode(f)
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	baseline := read("baseline")
	for name, r := range receipts {
		t.Run(name, func(t *testing.T) {
			expected := overlay.Recording
			switch name {
			case "baseline", "hidden-after-paste":
				expected = overlay.Hidden
			case "updated-with-copy":
				expected = overlay.Transcribing
			case "replacement-error", "remapped-after-close":
				expected = overlay.Error
			}
			if r.Scene.Visual != expected {
				t.Fatalf("wrong coexistence visual %s: %v", name, r.Scene.Visual)
			}
			if expected == overlay.Recording && r.Scene.Preview != "PREVIEW ONLY — NEVER DISPATCHED 7f412" {
				t.Fatal("missing preview-only sentinel")
			}
			bounds, err := validateOverlayCapture(baseline, read(name), r)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("independent screenshot bounds %v; frame %d", bounds, r.Frame)
		})
	}
}

func TestDesktopGPUFailureChild(t *testing.T) { desktop.GPUFailureChild() }
