//go:build integration || e2e

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/mschulkind-oss/mavor/test/desktop"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	var children []*stagingProcess
	launch := func(name string, args ...string) {
		env := append(os.Environ(), h.Env()...)
		env = append(env, "MAVOR_STORYBOOK=1", "GTK_A11Y=none", "GSK_RENDERER=cairo")
		cmd, stop, err := launchStagingProcess(env, testLogger{prefix: name, h: h}, name, args...)
		if err != nil {
			t.Fatalf("storybook prerequisite %s: %v", name, err)
		}
		t.Cleanup(stop)
		children = append(children, cmd)
		t.Logf("tracked storybook process %s pid=%d", name, cmd.Process.Pid)
	}
	// Replace Sway's existing background client rather than adding a competing
	// layer surface. Sway 1.9 paints the older black surface above a newer
	// independent swaybg; no readiness timeout can make that wallpaper visible.
	// Sway disconnects its previous client and owns the replacement's lifetime.
	var replies []struct {
		Success bool
	}
	response := h.swaymsg(fmt.Sprintf("output HEADLESS-1 bg %s stretch", shellQuote(filepath.Join(root, "test/desktop/wallpaper.png"))))
	if err := json.Unmarshal([]byte(response), &replies); err != nil {
		t.Fatal(err)
	}
	if len(replies) != 1 || !replies[0].Success {
		t.Fatalf("stage Sway-owned wallpaper: %s", response)
	}
	launch(binary, "Mavor session notes")
	deadline := time.Now().Add(10 * time.Second)
	var rect struct{ X, Y, Width, Height int }
	mapped := false
	for time.Now().Before(deadline) {
		for _, child := range children {
			if err := child.alive(); err != nil {
				retainStagingFailure(t, h, nil, err)
				t.Fatal(err)
			}
		}
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
		err := fmt.Errorf("storybook native editor did not map")
		retainStagingFailure(t, h, nil, err)
		t.Fatal(err)
	}
	if rect.Width != 900 || rect.Height != 500 || rect.X != 510 || rect.Y < 300 || rect.Y >= 350 {
		err := fmt.Errorf("editor staging geometry: %+v", rect)
		retainStagingFailure(t, h, nil, err)
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var raw []byte
	err = waitStagedPresentation(ctx, 50*time.Millisecond, func() error {
		for _, child := range children {
			if err := child.alive(); err != nil {
				return err
			}
		}
		return nil
	}, func() error {
		cmd := exec.CommandContext(ctx, "grim", "-o", "HEADLESS-1", "-")
		cmd.Env = append(os.Environ(), h.Env()...)
		frame, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("capture staged frame: %w", err)
		}
		raw = frame
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		return stagedFrame(img)
	})
	if err != nil {
		retainStagingFailure(t, h, raw, err)
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
type stagingProcess struct {
	*exec.Cmd
	done chan struct{}
	err  error // written before done closes
}

func (p *stagingProcess) alive() error {
	select {
	case <-p.done:
		return fmt.Errorf("staging child %s exited: %v", p.Path, p.err)
	default:
		return nil
	}
}

func launchStagingProcess(env []string, log io.Writer, name string, args ...string) (*stagingProcess, func(), error) {
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
	p := &stagingProcess{Cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	var once sync.Once
	return p, func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			_ = stdin.Close()
			<-p.done
		})
	}, nil
}

// A live child and mapped geometry are prerequisites, not presentation proof.
// Each successful check must sample a real frame in the caller.
func waitStagedPresentation(ctx context.Context, interval time.Duration, alive, frame func() error) error {
	var last error
	for {
		if err := alive(); err != nil {
			return err
		}
		last = frame()
		if last == nil {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("staging presentation deadline: %w (last frame: %v)", ctx.Err(), last)
		case <-timer.C:
		}
	}
}

// Fail-only captures are diagnostics, never report evidence. CI retains this
// directory separately from the screenshots that passed the report oracle.
func retainStagingFailure(t *testing.T, h *Harness, raw []byte, cause error) {
	t.Helper()
	dir := filepath.Join("..", "reports", "staging-failure", sanitize(t.Name()))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Log(err)
		return
	}
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Log(err)
		}
	}
	if len(raw) > 0 {
		write("last-frame.png", raw)
	}
	write("failure.txt", []byte(cause.Error()))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	diagnostic := func(name string, args ...string) []byte {
		data, err := h.desktopCommand(ctx, name, args...)
		if err != nil {
			return append(data, []byte(fmt.Sprintf("\nerror: %v (context: %v)\n", err, ctx.Err()))...)
		}
		return data
	}
	write("tree.json", diagnostic("swaymsg", "-t", "get_tree"))
	write("outputs.json", diagnostic("swaymsg", "-t", "get_outputs"))
	if len(raw) == 0 {
		frame, err := h.desktopCommand(ctx, "grim", "-o", "HEADLESS-1", "-")
		if err == nil {
			write("last-frame.png", frame)
		} else {
			write("capture-error.txt", []byte(err.Error()))
		}
	}
	if data, err := os.ReadFile(filepath.Join(h.XDGRuntime, "sway.conf")); err == nil {
		write("sway.conf", data)
	}
	t.Logf("staging diagnostics: %s", dir)
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

// Start deliberately owns a black background. Staging must replace that owner,
// not rely on the compositor-specific ordering of two background surfaces.
func TestDesktopStagingReplacesBlackBackdrop(t *testing.T) {
	h := StartWithBar(t)
	stageDesktop(t, h)
	for i := 0; i < 3; i++ {
		raw := h.Grim()
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := stagedFrame(img); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStagedPresentationPolling(t *testing.T) {
	for _, tc := range []struct {
		name       string
		readyAfter int
		dead       bool
		wantError  bool
	}{
		{"already-ready", 1, false, false},
		{"eventually-ready", 3, false, false},
		{"permanently-absent", 0, false, true},
		{"dead-child", 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls := 0
			err := waitStagedPresentation(ctx, time.Millisecond, func() error {
				if tc.dead {
					return fmt.Errorf("editor exited")
				}
				return nil
			}, func() error {
				calls++
				if tc.readyAfter == 0 && calls == 3 {
					cancel()
				}
				if tc.readyAfter > 0 && calls >= tc.readyAfter {
					return nil
				}
				return stagedFrame(image.NewRGBA(image.Rect(0, 0, 1, 1)))
			})
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			if !tc.wantError && calls != tc.readyAfter {
				t.Fatalf("samples=%d", calls)
			}
			if tc.dead && calls != 0 {
				t.Fatal("sampled after child death")
			}
			if tc.readyAfter == 0 && (calls < 2 || !strings.Contains(err.Error(), "teal=0")) {
				t.Fatalf("missing repeated frame diagnostics: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestStagingExitedChild(t *testing.T) {
	child, stop, err := launchStagingProcess(os.Environ(), io.Discard, "sh", "-c", "exit 7")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case <-child.done:
	case <-time.After(time.Second):
		t.Fatal("child exit was not observed")
	}
	if err := child.alive(); err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("exit evidence: %v", err)
	}
	stop()
	stop()
}

func TestStagingDiagnosticsBounded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "grim"), []byte("#!/bin/sh\nexit 7\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join("..", "reports", "staging-failure", sanitize(t.Name()))) })
	shim := filepath.Join(dir, "swaymsg")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec sleep 2\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	h := &Harness{t: t, XDGRuntime: dir}
	before := time.Now()
	retainStagingFailure(t, h, nil, fmt.Errorf("original cause"))
	if time.Since(before) > 1500*time.Millisecond {
		t.Fatal("diagnostics exceeded bounded failure budget")
	}
	artifact := filepath.Join("..", "reports", "staging-failure", sanitize(t.Name()))
	data, err := os.ReadFile(filepath.Join(artifact, "failure.txt"))
	if err != nil || string(data) != "original cause" {
		t.Fatalf("lost cause: %s %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(artifact, "tree.json"))
	if err != nil || !strings.Contains(string(data), "error:") {
		t.Fatalf("lost diagnostic error: %s %v", data, err)
	}
}

func TestHarnessStartupFailureCleanup(t *testing.T) {
	if dir := os.Getenv("MAVOR_STARTUP_FAILURE_TEST"); dir != "" {
		Start(t, Options{})
		t.Fatal("startup failure was not injected")
	}
	dir := t.TempDir()
	for _, name := range []string{"sway", "dbus-daemon"} {
		real, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		script := fmt.Sprintf("#!/bin/sh\necho $$ > %s\nexec %s \"$@\"\n", shellQuote(filepath.Join(dir, name+".pid")), shellQuote(real))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Failure occurs after both owned children have started, before Start returns.
	if err := os.WriteFile(filepath.Join(dir, "swaymsg"), []byte("#!/bin/sh\nexit 17\n"), 0755); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestHarnessStartupFailureCleanup$", "-test.v")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MAVOR_STARTUP_FAILURE_TEST="+dir)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "swaymsg") {
		t.Fatalf("unexpected injected failure: %v %s", err, out)
	}
	for _, name := range []string{"sway", "dbus-daemon"} {
		data, err := os.ReadFile(filepath.Join(dir, name+".pid"))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		// Also clean up the intentionally red pre-repair run.
		defer syscall.Kill(pid, syscall.SIGTERM)
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !os.IsNotExist(err) {
			t.Errorf("startup leaked %s pid=%d", name, pid)
		}
	}
}

func TestHarnessBlackBackdropDelayedPresentation(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name  string
		level uint8
	}{{"gray", 63}, {"black", 0}} {
		img := image.NewRGBA(image.Rect(0, 0, 10, 10))
		for y := 0; y < 10; y++ {
			for x := 0; x < 10; x++ {
				img.Set(x, y, color.RGBA{c.level, c.level, c.level, 255})
			}
		}
		f, err := os.Create(filepath.Join(dir, c.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, img)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	marker := shellQuote(filepath.Join(dir, "sampled"))
	script := fmt.Sprintf("#!/bin/sh\nif [ -f %s ]; then cat %s; else touch %s; cat %s; fi\n", marker, shellQuote(filepath.Join(dir, "black.png")), marker, shellQuote(filepath.Join(dir, "gray.png")))
	if err := os.WriteFile(filepath.Join(dir, "grim"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	h := &Harness{t: t, XDGRuntime: dir}
	h.requireDarkBackdrop()
}

func TestBlackBackdropRejectsPermanentAbsence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var frame bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			img.Set(x, y, color.RGBA{63, 63, 63, 255})
		}
	}
	if err := png.Encode(&frame, img); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := waitDarkBackdrop(ctx, time.Millisecond, func() ([]byte, error) {
		calls++
		if calls == 3 {
			cancel()
		}
		return frame.Bytes(), nil
	})
	if err == nil || !strings.Contains(err.Error(), "(63,63,63)") || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestStagingDiagnosticsCommandFailure(t *testing.T) {
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join("..", "reports", "staging-failure", sanitize(t.Name()))) })
	dir := t.TempDir()
	for _, name := range []string{"swaymsg", "grim"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 7\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	retainStagingFailure(t, &Harness{t: t, XDGRuntime: dir}, nil, fmt.Errorf("editor exited"))
	artifact := filepath.Join("..", "reports", "staging-failure", sanitize(t.Name()))
	for _, name := range []string{"tree.json", "outputs.json", "capture-error.txt"} {
		data, err := os.ReadFile(filepath.Join(artifact, name))
		if err != nil || !strings.Contains(string(data), "exit status 7") {
			t.Fatalf("missing command failure %s: %s %v", name, data, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(artifact, "failure.txt"))
	if err != nil || string(data) != "editor exited" {
		t.Fatalf("lost original failure: %s %v", data, err)
	}
}
