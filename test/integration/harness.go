//go:build integration || e2e

package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Geometry shared by every harness-backed test: the virtual output size,
// the overlay's top margin, and the waybar height it must clear. Declared
// here rather than in a test file because the harness itself writes the
// margin into the daemon config, and the harness builds under both the
// integration and e2e tags.
const (
	testWidth  = 1920
	testHeight = 1080
	// Deliberately not the daemon default (8): a margin larger than the test
	// waybar makes the assertion discriminating. If the overlay were placed
	// from the screen edge rather than below waybar's exclusive zone, this
	// value would put it exactly on waybar's bottom edge and the gap check
	// would fail.
	testTopMargin    = 32
	testWaybarHeight = 32
)

// Harness drives a headless Sway compositor for tests. Spawn one per test;
// it owns a private XDG_RUNTIME_DIR, a session dbus, and a wlroots headless
// output. Stop() is registered with t.Cleanup so individual tests don't
// need to remember to tear it down.
type Harness struct {
	t           *testing.T
	XDGRuntime  string
	WaylandDisp string
	DBusAddr    string
	SwaySock    string
	AudioSource string // e.g. "mavor-test-foo.monitor"; "" if AudioSink not requested
	ShimDir     string // PATH-prepended dir holding fake whisper-cli, if any
	sinkModule  string

	dbus     *exec.Cmd
	sway     *exec.Cmd
	waybar   *exec.Cmd
	stopOnce sync.Once
	mu       sync.Mutex
}

type Options struct {
	// Width, Height for the virtual output. Defaults to 1920x1080.
	Width, Height int
	// LaunchWaybar starts waybar in the compositor with a minimal config.
	LaunchWaybar bool
	// AudioSink, if non-empty, asks the harness to load a module-null-sink
	// with this name on the host pipewire and route the daemon's parec at
	// its monitor. Cleanup unloads the module on test exit. Use a name
	// unique per test (e.g. "mavor-test-<TestName>") so parallel tests do not
	// step on each other.
	AudioSink string
	// FakeTranscript, if non-empty, shims whisper-cli with a script that
	// writes this text as the transcription sidecar. The real whisper-cli
	// stays untouched on disk; only PATH is altered for the daemon child.
	FakeTranscript string
}

// Start brings up the harness. Returns once the wayland socket is accepting
// connections (the daemon can be started immediately afterward).
func Start(t *testing.T, opts Options) *Harness {
	t.Helper()
	xdg := t.TempDir()
	if err := os.Chmod(xdg, 0o700); err != nil {
		t.Fatal(err)
	}
	h := start(t, opts, xdg)
	t.Cleanup(h.Stop)
	return h
}

// start brings up the compositor in the given runtime directory. Split out of
// Start so the shared compositor can own a directory and a lifetime that are
// not tied to whichever test happened to ask for it first.
func start(t *testing.T, opts Options, xdg string) *Harness {
	t.Helper()
	if opts.Width == 0 {
		opts.Width = 1920
	}
	if opts.Height == 0 {
		opts.Height = 1080
	}

	h := &Harness{t: t, XDGRuntime: xdg}

	h.startDBus()
	h.startSway(opts)
	h.waitForWayland()
	h.configureOutput(opts.Width, opts.Height)
	if opts.LaunchWaybar {
		h.startWaybar()
	}
	if opts.AudioSink != "" {
		h.loadNullSink(opts.AudioSink)
	}
	if opts.FakeTranscript != "" {
		h.installWhisperShim(opts.FakeTranscript)
	}
	return h
}

func (h *Harness) startDBus() {
	conf, err := findDBusSessionConf()
	if err != nil {
		h.t.Fatal(err)
	}
	cmd := exec.Command("dbus-daemon", "--config-file="+conf, "--print-address", "--nofork")
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+h.XDGRuntime)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		h.t.Fatal(err)
	}
	cmd.Stderr = testLogger{prefix: "dbus", h: h}
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("dbus-daemon: %v", err)
	}
	addrCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := stdout.Read(buf)
		addrCh <- strings.TrimSpace(string(buf[:n]))
	}()
	select {
	case addr := <-addrCh:
		if addr == "" {
			h.t.Fatal("dbus-daemon produced no address")
		}
		h.DBusAddr = addr
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		h.t.Fatal("dbus-daemon did not print address within 2s")
	}
	h.dbus = cmd
}

func (h *Harness) startSway(opts Options) {
	confPath := filepath.Join(h.XDGRuntime, "sway.conf")
	body := fmt.Sprintf("output HEADLESS-1 mode %dx%d\n", opts.Width, opts.Height)
	if err := os.WriteFile(confPath, []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
	cmd := exec.Command("sway", "--config", confPath)
	cmd.Env = append(os.Environ(),
		"XDG_RUNTIME_DIR="+h.XDGRuntime,
		"DBUS_SESSION_BUS_ADDRESS="+h.DBusAddr,
		"WLR_BACKENDS=headless",
		"WLR_LIBINPUT_NO_DEVICES=1",
		"WLR_RENDERER=pixman",
		"WLR_HEADLESS_OUTPUTS=1",
	)
	cmd.Stdout = testLogger{prefix: "sway", h: h}
	cmd.Stderr = testLogger{prefix: "sway", h: h}
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("sway: %v", err)
	}
	h.sway = cmd
}

func (h *Harness) waitForWayland() {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(h.XDGRuntime)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "wayland-") && !strings.HasSuffix(e.Name(), ".lock") {
				h.WaylandDisp = e.Name()
				goto found
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatal("wayland socket never appeared")
found:
	// Sway writes its IPC socket alongside the Wayland one. Wait briefly for
	// it so swaymsg calls succeed.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(filepath.Join(h.XDGRuntime, "sway-ipc.*.sock"))
		if len(matches) > 0 {
			h.SwaySock = matches[0]
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatal("sway IPC socket never appeared")
}

func (h *Harness) configureOutput(w, hpx int) {
	// Tell sway the output's mode again now that the compositor is up. The
	// inline `output HEADLESS-1 ...` directive applies when the output is
	// first discovered; for headless backends we sometimes need to re-issue.
	h.swaymsg(fmt.Sprintf("output HEADLESS-1 mode %dx%d", w, hpx))
}

func (h *Harness) startWaybar() {
	confDir := filepath.Join(h.XDGRuntime, "waybar")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	// Minimal waybar: top-anchored bar, one text module so it has visible
	// content for screenshot-based assertions.
	conf := `{"layer":"top","position":"top","height":32,"modules-left":["custom/label"],
  "custom/label":{"format":"WAYBAR-TEST","exec":"echo WAYBAR-TEST"}}`
	style := `* { background: #222; color: #fff; font: 14px sans-serif; padding: 0 8px; }`
	if err := os.WriteFile(filepath.Join(confDir, "config"), []byte(conf), 0o644); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "style.css"), []byte(style), 0o644); err != nil {
		h.t.Fatal(err)
	}
	cmd := exec.Command("waybar", "-c", filepath.Join(confDir, "config"),
		"-s", filepath.Join(confDir, "style.css"))
	cmd.Env = append(os.Environ(),
		"XDG_RUNTIME_DIR="+h.XDGRuntime,
		"WAYLAND_DISPLAY="+h.WaylandDisp,
		"DBUS_SESSION_BUS_ADDRESS="+h.DBusAddr,
	)
	cmd.Stdout = testLogger{prefix: "waybar", h: h}
	cmd.Stderr = testLogger{prefix: "waybar", h: h}
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("waybar: %v", err)
	}
	h.waybar = cmd
	// Give waybar a moment to register on the layer.
	time.Sleep(300 * time.Millisecond)
}

// adopt points the harness's logging and failure reporting at the test that
// is running now. The shared compositor outlives any single test, and calling
// Fatal on a finished test panics.
func (h *Harness) adopt(t *testing.T) {
	h.mu.Lock()
	h.t = t
	h.mu.Unlock()
}

// detach releases the harness from any test. Subprocess output after this
// point goes to stderr rather than panicking a finished test.
func (h *Harness) detach() { h.adopt(nil) }

// test returns the test currently owning the harness.
func (h *Harness) test() *testing.T {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.t
}

// Stop tears down the compositor and supporting processes. Safe to call more
// than once.
func (h *Harness) Stop() {
	h.stopOnce.Do(func() {
		// Unload the host pipewire null-sink BEFORE killing dbus/sway so
		// pactl can still reach the host daemon.
		if h.sinkModule != "" {
			cmd := exec.Command("pactl", "unload-module", h.sinkModule)
			_ = cmd.Run()
		}
		for _, p := range []*exec.Cmd{h.waybar, h.sway, h.dbus} {
			if p != nil && p.Process != nil {
				_ = p.Process.Kill()
			}
		}
		for _, p := range []*exec.Cmd{h.waybar, h.sway, h.dbus} {
			if p != nil {
				_, _ = p.Process.Wait()
			}
		}
	})
}

func (h *Harness) loadNullSink(name string) {
	// A reachable PulseAudio/PipeWire server is an environment prerequisite,
	// not something this repo controls. Containers routinely have none, and a
	// hard failure there reads like a code regression — skip instead, loudly.
	if out, err := exec.Command("pactl", "info").CombinedOutput(); err != nil {
		h.t.Skipf("no reachable PulseAudio/PipeWire server, skipping audio test: %v (%s)",
			err, strings.TrimSpace(string(out)))
	}

	cmd := exec.Command("pactl", "load-module", "module-null-sink",
		"sink_name="+name,
		"sink_properties=device.description="+name,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("pactl load-module: %v (%s)", err, out)
	}
	h.sinkModule = strings.TrimSpace(string(out))
	h.AudioSource = name + ".monitor"
	// pipewire publishes the new node asynchronously; wait briefly for the
	// monitor source to appear so parec can target it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		listCmd := exec.Command("pactl", "list", "short", "sources")
		if listOut, err := listCmd.Output(); err == nil && strings.Contains(string(listOut), h.AudioSource) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Skipf("audio source %s did not appear; the audio server is not usable here", h.AudioSource)
}

// installWhisperShim drops a fake whisper-cli into a PATH-prepended dir so
// the daemon's transcribe step is deterministic without bypassing the real
// speech.WhisperCli code path. The script writes the configured transcript to
// the sidecar file the daemon expects (-otxt).
func (h *Harness) installWhisperShim(transcript string) {
	dir := filepath.Join(h.XDGRuntime, "shims")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	script := `#!/bin/sh
# Fake whisper-cli for integration tests. Mimics ` + "`" + `whisper-cli -otxt` + "`" + `:
# expects to find the WAV path after a -f flag and writes <wav>.txt next to it.
wav=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-f" ]; then wav="$2"; shift 2; continue; fi
  shift
done
[ -z "$wav" ] && { echo "fake whisper-cli: missing -f" >&2; exit 1; }
printf '%s\n' ` + shellQuote(transcript) + ` > "${wav}.txt"
`
	dest := filepath.Join(dir, "whisper-cli")
	if err := os.WriteFile(dest, []byte(script), 0o755); err != nil {
		h.t.Fatal(err)
	}
	h.ShimDir = dir
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Env returns the env-var slice to pass to a subprocess that should talk to
// this harness's compositor (the daemon).
func (h *Harness) Env() []string {
	return []string{
		"XDG_RUNTIME_DIR=" + h.XDGRuntime,
		"WAYLAND_DISPLAY=" + h.WaylandDisp,
		"DBUS_SESSION_BUS_ADDRESS=" + h.DBusAddr,
		"GDK_BACKEND=wayland",
	}
}

// swaymsg sends a single IPC command and returns the response body.
func (h *Harness) swaymsg(args ...string) string {
	cmd := exec.Command("swaymsg", args...)
	cmd.Env = append(os.Environ(),
		"XDG_RUNTIME_DIR="+h.XDGRuntime,
		"WAYLAND_DISPLAY="+h.WaylandDisp,
		"SWAYSOCK="+h.SwaySock,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("swaymsg %v: %v (%s)", args, err, out)
	}
	return string(out)
}

// Grim takes a screenshot of HEADLESS-1 and returns the PNG bytes.
func (h *Harness) Grim() []byte {
	return h.grim("HEADLESS-1")
}

func (h *Harness) grim(output string) []byte {
	cmd := exec.Command("grim", "-o", output, "-")
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+h.XDGRuntime, "WAYLAND_DISPLAY="+h.WaylandDisp)
	out, err := cmd.Output()
	if err != nil {
		h.t.Fatalf("grim: %v", err)
	}
	return out
}

// WlPaste returns the current clipboard contents.
func (h *Harness) WlPaste() string {
	cmd := exec.Command("wl-paste", "--no-newline")
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+h.XDGRuntime, "WAYLAND_DISPLAY="+h.WaylandDisp)
	out, err := cmd.Output()
	if err != nil {
		h.t.Fatalf("wl-paste: %v", err)
	}
	return string(out)
}

// RunDaemon spawns the mavor binary as a child of the harness and returns the
// daemon's socket path plus a teardown closure. modelName is a whisper model
// name without "ggml-" prefix or ".bin" suffix (e.g. "tiny.en"). If a real
// model file isn't already at the expected path, a stub is dropped so the
// daemon's pre-flight check passes — tests that need real transcription
// must arrange for a real model.
func (h *Harness) RunDaemon(ctx context.Context, binary, modelName string, extraEnv ...string) (socket string, stop func()) {
	socket = filepath.Join(h.XDGRuntime, "mavor.sock")
	modelDir := filepath.Join(h.XDGRuntime, "cache", "mavor", "models")
	modelPath := filepath.Join(modelDir, "ggml-"+modelName+".bin")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	if _, err := os.Stat(modelPath); err != nil {
		if err := os.WriteFile(modelPath, []byte("stub"), 0o644); err != nil {
			h.t.Fatal(err)
		}
	}

	cfgDir := filepath.Join(h.XDGRuntime, "config", "mavor")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		h.t.Fatal(err)
	}
	cfg := fmt.Sprintf("model = %q\nmodel_dir = %q\nsocket = %q\ntop_margin = %d\n",
		modelName, modelDir, socket, testTopMargin)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		h.t.Fatal(err)
	}

	cmd := exec.CommandContext(ctx, binary, "daemon", "-v")
	env := append([]string{}, h.Env()...)
	env = append(env,
		"XDG_CACHE_HOME="+filepath.Join(h.XDGRuntime, "cache"),
		"XDG_CONFIG_HOME="+filepath.Join(h.XDGRuntime, "config"),
	)
	if h.AudioSource != "" {
		env = append(env, "PULSE_SOURCE="+h.AudioSource)
	}
	env = append(env, extraEnv...)
	base := os.Environ()
	if h.ShimDir != "" {
		// Prepend the shim dir so the daemon picks up the fake whisper-cli
		// without disturbing the rest of the inherited PATH.
		for i, kv := range base {
			if strings.HasPrefix(kv, "PATH=") {
				base[i] = "PATH=" + h.ShimDir + ":" + kv[len("PATH="):]
				break
			}
		}
	}
	cmd.Env = append(base, env...)
	cmd.Stdout = testLogger{prefix: "mavor", h: h}
	cmd.Stderr = testLogger{prefix: "mavor", h: h}
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("mavor daemon: %v", err)
	}
	stop = func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_, _ = cmd.Process.Wait()
	}
	h.t.Cleanup(stop)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			return socket, stop
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatalf("daemon socket %s never appeared", socket)
	return socket, stop
}

func findDBusSessionConf() (string, error) {
	// nix store path is fastest; fall back to FHS locations.
	candidates := []string{
		"/etc/dbus-1/session.conf",
	}
	// Globbing /nix/store/*-dbus-*/share/dbus-1/session.conf
	if matches, _ := filepath.Glob("/nix/store/*-dbus-*/share/dbus-1/session.conf"); len(matches) > 0 {
		candidates = append([]string{matches[0]}, candidates...)
	}
	for _, c := range candidates {
		if b, err := os.ReadFile(c); err == nil && strings.Contains(string(b), "<listen>") {
			return c, nil
		}
	}
	return "", errors.New("integration: no usable dbus session.conf with <listen> found")
}

// testLogger pipes a subprocess stdout/stderr into testing.T.Log so output is
// captured per test and emitted on failure.
//
// It resolves the test on every write rather than capturing one, because the
// shared compositor outlives any single test: its sway and waybar keep writing
// after the test that started them has finished, and Logf on a completed test
// panics. Once no test owns the harness, output goes to stderr instead.
type testLogger struct {
	prefix string
	h      *Harness
}

func (l testLogger) Write(p []byte) (int, error) {
	t := l.h.test()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if t == nil {
			fmt.Fprintf(os.Stderr, "[%s] %s\n", l.prefix, line)
			continue
		}
		t.Logf("[%s] %s", l.prefix, line)
	}
	return len(p), nil
}

var _ io.Writer = testLogger{}

// sanitize turns a test name into a string usable as a PipeWire node name.
func sanitize(s string) string {
	return strings.NewReplacer("/", "-", " ", "-", ":", "-").Replace(s)
}

// pipeAudio plays a short generated tone INTO the null sink so parec
// (capturing from <sink>.monitor) records non-empty data. Uses paplay over
// the PulseAudio protocol — pw-cat would need the PipeWire native socket
// which is not always exposed inside a container.
func pipeAudio(t *testing.T, h *Harness, sinkName string) {
	t.Helper()
	wavPath := h.XDGRuntime + "/inject.wav"
	gen := exec.Command("ffmpeg", "-nostats", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5",
		"-ar", "48000", "-ac", "2", "-f", "wav", wavPath,
	)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg synth: %v (%s)", err, out)
	}
	play := exec.Command("paplay", "--device="+sinkName, wavPath)
	if out, err := play.CombinedOutput(); err != nil {
		t.Fatalf("paplay: %v (%s)", err, out)
	}
}
