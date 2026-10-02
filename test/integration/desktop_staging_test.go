//go:build integration || e2e

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/mschulkind-oss/mavor/test/desktop"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Staging is deliberately not part of Start: all ordinary tests retain black.
func stageDesktop(t *testing.T, h *Harness) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	flags, err := exec.Command("pkg-config", "--cflags", "--libs", "gtk4").Output()
	if err != nil {
		t.Fatalf("storybook GTK prerequisite: %v", err)
	}
	binary := filepath.Join(h.XDGRuntime, "editor")
	args := append([]string{filepath.Join(root, "test/gnome/client/consumer.c"), "-o", binary}, strings.Fields(string(flags))...)
	if out, err := exec.Command("cc", args...).CombinedOutput(); err != nil {
		t.Fatalf("build native editor: %v: %s", err, out)
	}
	launch := func(name string, args ...string) {
		env := append(os.Environ(), h.Env()...)
		env = append(env, "MAVOR_STORYBOOK=1", "GTK_A11Y=none", "GSK_RENDERER=cairo")
		cmd, stop, err := launchStagingProcess(env, testLogger{prefix: name, h: h}, name, args...)
		if err != nil {
			t.Fatalf("storybook prerequisite %s: %v", name, err)
		}
		t.Cleanup(stop)
		t.Logf("tracked storybook process %s pid=%d", name, cmd.Process.Pid)
	}
	launch("swaybg", "-i", filepath.Join(root, "test/desktop/wallpaper.png"), "-m", "stretch")
	launch(binary, "Mavor session notes")
	deadline := time.Now().Add(10 * time.Second)
	var rect struct{ X, Y, Width, Height int }
	mapped := false
	for time.Now().Before(deadline) {
		var tree map[string]any
		if err := json.Unmarshal([]byte(h.swaymsg("-t", "get_tree")), &tree); err != nil {
			t.Fatal(err)
		}
		var visit func(map[string]any)
		visit = func(n map[string]any) {
			if n["name"] == "Mavor session notes" {
				data, _ := json.Marshal(n["rect"])
				_ = json.Unmarshal(data, &rect)
				mapped = true
			}
			for _, key := range []string{"nodes", "floating_nodes"} {
				children, _ := n[key].([]any)
				for _, child := range children {
					visit(child.(map[string]any))
				}
			}
		}
		visit(tree)
		if mapped {
			h.swaymsg(`[title="^Mavor session notes$"] floating enable, resize set width 900 px height 500 px, move position 510 px 300 px`)
			if rect.Width == 900 && rect.Height == 500 && rect.X == 510 && rect.Y >= 300 && rect.Y < 350 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !mapped {
		t.Fatal("storybook native editor did not map")
	}
	if rect.Width != 900 || rect.Height != 500 || rect.X != 510 || rect.Y < 300 || rect.Y >= 350 {
		t.Fatalf("editor staging geometry: %+v", rect)
	}
	time.Sleep(500 * time.Millisecond)
	img, err := png.Decode(bytes.NewReader(h.Grim()))
	if err != nil {
		t.Fatal(err)
	}
	if err := stagedFrame(img); err != nil {
		t.Fatal(err)
	}
}

// Frame evidence must show teal/blue wallpaper and a light editor.
func stagedFrame(img image.Image) error {
	teal, blue, bright := 0, 0, 0
	b := img.Bounds()
	for y := 180; y < b.Max.Y; y++ {
		for x := 0; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			r >>= 8
			g >>= 8
			bl >>= 8
			if g > r*14/10 && g > bl*115/100 && g > 30 {
				teal++
			}
			if bl > r*15/10 && bl > g*11/10 && bl > 30 {
				blue++
			}
			if r > 180 && g > 180 && bl > 180 {
				bright++
			}
		}
	}
	if teal < 5000 || blue < 5000 || bright < 10000 {
		return fmt.Errorf("missing wallpaper/editor frame evidence: teal=%d blue=%d bright=%d", teal, blue, bright)
	}
	return nil
}

func TestStagedFrameRejectsEmpty(t *testing.T) {
	if stagedFrame(image.NewRGBA(image.Rect(0, 0, 1920, 1080))) == nil {
		t.Fatal("black frame accepted")
	}
}

// Direct staging children are killed and reaped before Harness.Stop runs.
func launchStagingProcess(env []string, log io.Writer, name string, args ...string) (*exec.Cmd, func(), error) {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Stdout = log
	cmd.Stderr = log
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, nil, err
	}
	return cmd, func() { _ = cmd.Process.Kill(); _ = stdin.Close(); _ = cmd.Wait() }, nil
}

func TestStagingMissingPrerequisite(t *testing.T) {
	if _, _, err := launchStagingProcess(os.Environ(), io.Discard, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing staging prerequisite accepted")
	}
}

func TestStagingCleanup(t *testing.T) {
	cmd, stop, err := launchStagingProcess(os.Environ(), io.Discard, "sleep", "30")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if cmd.ProcessState == nil {
		t.Fatal("staging child was not reaped")
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", cmd.Process.Pid)); !os.IsNotExist(err) {
		t.Fatalf("staging child still present: %v", err)
	}
}

func TestStorybookReportLabelsAndPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.html")
	data := desktop.ReportData{Compositor: "measured Sway", TotalStates: 1, Captures: []desktop.StateCapture{{State: desktop.StoryState{Title: "<fixture>", ID: "fixture", Index: 1}, FullRelPath: "screenshots/01_fixture_full.png"}}}
	if err := desktop.GenerateHTMLReport(path, data); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gnome-storybook.html", "screenshots/01_fixture_full.png", "&lt;fixture&gt;", "measured Sway", "no recording or inference"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("missing report label/path %q", want)
		}
	}
}
