package audio

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Ducker controls audio sink volume ducking during speech recording.
type Ducker interface {
	// Duck lowers the sink volume, remembering the previous volume.
	Duck() error
	// Restore restores the sink volume to the level before ducking.
	Restore() error
}

// NoopDucker is a no-op implementation of Ducker.
type NoopDucker struct{}

func (n *NoopDucker) Duck() error    { return nil }
func (n *NoopDucker) Restore() error { return nil }

// MockDucker is a test implementation of Ducker tracking calls.
type MockDucker struct {
	mu           sync.Mutex
	ducked       bool
	duckCalls    int
	restoreCalls int
	DuckErr      error
	RestoreErr   error
}

func (m *MockDucker) Duck() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.duckCalls++
	if m.DuckErr != nil {
		return m.DuckErr
	}
	m.ducked = true
	return nil
}

func (m *MockDucker) Restore() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restoreCalls++
	if m.RestoreErr != nil {
		return m.RestoreErr
	}
	m.ducked = false
	return nil
}

func (m *MockDucker) IsDucked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ducked
}

func (m *MockDucker) Calls() (duck int, restore int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.duckCalls, m.restoreCalls
}

// Backend specifies the audio control CLI tool to use.
type Backend string

const (
	BackendAuto  Backend = "auto"
	BackendWpctl Backend = "wpctl"
	BackendPactl Backend = "pactl"
)

// CommandRunner executes a command and returns output and error.
type CommandRunner func(name string, args ...string) ([]byte, error)

func defaultRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// CommandDucker implements Ducker by running wpctl or pactl.
type CommandDucker struct {
	mu           sync.Mutex
	backend      Backend
	duckVolume   string            // e.g. "20%" or "0.2"
	duckSink     string            // target sink name/ID (e.g. "alsa_output.pci-..." or "42")
	duckStreams  []string          // list of application names to duck (e.g. ["spotify", "firefox", "vlc"])
	savedVol     string            // saved volume when whole sink is ducked
	savedStreams map[string]string // sink-input ID -> original volume when ducking specific streams
	ducked       bool
	runner       CommandRunner
	logger       *slog.Logger
}

// NewCommandDucker creates a CommandDucker with the specified backend, duck volume, duck sink, and duck streams.
func NewCommandDucker(backend Backend, duckVolume, duckSink string, duckStreams []string) *CommandDucker {
	// Default to silence. Dictation competes with whatever is playing for both
	// the microphone and the user's attention, so background media is muted
	// outright; a partial reduction is opt-in via duck_volume.
	if duckVolume == "" {
		if backend == BackendWpctl {
			duckVolume = "0"
		} else {
			duckVolume = "0%"
		}
	}
	return &CommandDucker{
		backend:     backend,
		duckVolume:  duckVolume,
		duckSink:    duckSink,
		duckStreams: duckStreams,
		runner:      defaultRunner,
		logger:      slog.Default(),
	}
}

// NewPactlDucker creates a CommandDucker using pactl with target duck percentage (default "20%").
func NewPactlDucker(duckPercent string) *CommandDucker {
	if duckPercent == "" {
		duckPercent = "20%"
	}
	return NewCommandDucker(BackendPactl, duckPercent, "", nil)
}

// NewWpctlDucker creates a CommandDucker using wpctl with target duck volume (default "0.2").
func NewWpctlDucker(duckVolume string) *CommandDucker {
	if duckVolume == "" {
		duckVolume = "0.2"
	}
	return NewCommandDucker(BackendWpctl, duckVolume, "", nil)
}

// NewAutoDucker automatically detects wpctl or pactl on PATH.
func NewAutoDucker() *CommandDucker {
	backend := BackendWpctl
	if _, err := exec.LookPath("wpctl"); err != nil {
		if _, err := exec.LookPath("pactl"); err == nil {
			backend = BackendPactl
		}
	}
	return NewCommandDucker(backend, "", "", nil)
}

// SetSink sets the target audio sink name or ID.
func (c *CommandDucker) SetSink(sink string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.duckSink = sink
}

// SetStreams sets the list of application names to duck.
func (c *CommandDucker) SetStreams(streams []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.duckStreams = streams
}

// SetRunner overrides the command runner for testing.
func (c *CommandDucker) SetRunner(runner CommandRunner) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runner = runner
}

// SetLogger overrides the logger.
func (c *CommandDucker) SetLogger(l *slog.Logger) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logger = l
}

// IsDucked returns whether the volume is currently ducked.
func (c *CommandDucker) IsDucked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ducked
}

// Duck lowers the sink or stream volume, saving the current volume.
func (c *CommandDucker) Duck() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ducked {
		return nil // already ducked; preserve original saved volume
	}

	backend := c.resolveBackend()

	if len(c.duckStreams) > 0 {
		return c.duckStreamsInternal(backend)
	}

	return c.duckSinkInternal(backend)
}

func (c *CommandDucker) duckSinkInternal(backend Backend) error {
	targetSink := c.duckSink
	switch backend {
	case BackendWpctl:
		if targetSink == "" {
			targetSink = "@DEFAULT_AUDIO_SINK@"
		}
		out, err := c.runner("wpctl", "get-volume", targetSink)
		if err != nil {
			return fmt.Errorf("ducking: wpctl get-volume: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		vol, err := parseWpctlVolume(string(out))
		if err != nil {
			return fmt.Errorf("ducking: parse wpctl volume: %w", err)
		}
		c.savedVol = vol

		duckVol := c.duckVolume
		if duckVol == "" {
			duckVol = "0.2"
		}
		if setOut, err := c.runner("wpctl", "set-volume", targetSink, duckVol); err != nil {
			return fmt.Errorf("ducking: wpctl set-volume %s: %w (%s)", duckVol, err, strings.TrimSpace(string(setOut)))
		}
		c.ducked = true
		return nil

	case BackendPactl:
		if targetSink == "" {
			targetSink = "@DEFAULT_SINK@"
		}
		out, err := c.runner("pactl", "get-sink-volume", targetSink)
		if err != nil {
			return fmt.Errorf("ducking: pactl get-sink-volume: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		vol, err := parsePactlVolume(string(out))
		if err != nil {
			return fmt.Errorf("ducking: parse pactl volume: %w", err)
		}
		c.savedVol = vol

		duckVol := c.duckVolume
		if duckVol == "" {
			duckVol = "20%"
		}
		if setOut, err := c.runner("pactl", "set-sink-volume", targetSink, duckVol); err != nil {
			return fmt.Errorf("ducking: pactl set-sink-volume %s: %w (%s)", duckVol, err, strings.TrimSpace(string(setOut)))
		}
		c.ducked = true
		return nil

	default:
		return fmt.Errorf("ducking: unknown backend %q", backend)
	}
}

func (c *CommandDucker) duckStreamsInternal(backend Backend) error {
	out, err := c.runner("pactl", "list", "sink-inputs")
	if err != nil {
		return fmt.Errorf("ducking: pactl list sink-inputs: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	sinkInputs := parsePactlSinkInputs(string(out))
	duckVol := c.duckVolume
	if duckVol == "" {
		duckVol = "20%"
	}

	savedStreams := make(map[string]string)
	for _, si := range sinkInputs {
		if c.matchesStreams(si) {
			origVol := si.volume
			if origVol == "" {
				origVol = "100%"
			}
			setOut, err := c.runner("pactl", "set-sink-input-volume", si.id, duckVol)
			if err != nil {
				// Revert any already ducked streams on failure
				for id, prevVol := range savedStreams {
					_, _ = c.runner("pactl", "set-sink-input-volume", id, prevVol)
				}
				return fmt.Errorf("ducking: pactl set-sink-input-volume %s %s: %w (%s)", si.id, duckVol, err, strings.TrimSpace(string(setOut)))
			}
			savedStreams[si.id] = origVol
		}
	}

	c.savedStreams = savedStreams
	c.ducked = true
	return nil
}

// Restore returns the sink or stream volume to the level captured before Duck().
func (c *CommandDucker) Restore() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.ducked {
		return nil
	}

	backend := c.resolveBackend()
	c.ducked = false

	if len(c.savedStreams) > 0 {
		savedStreams := c.savedStreams
		c.savedStreams = nil

		// Sort IDs for deterministic execution order
		ids := make([]string, 0, len(savedStreams))
		for id := range savedStreams {
			ids = append(ids, id)
		}
		sort.Strings(ids)

		var errs []error
		for _, id := range ids {
			saved := savedStreams[id]
			if out, err := c.runner("pactl", "set-sink-input-volume", id, saved); err != nil {
				errs = append(errs, fmt.Errorf("ducking: pactl restore sink-input %s volume %s: %w (%s)", id, saved, err, strings.TrimSpace(string(out))))
			}
		}
		if len(errs) > 0 {
			return errors.Join(errs...)
		}
		return nil
	}

	// Sink-level restore
	if c.savedVol == "" {
		return nil
	}
	saved := c.savedVol
	c.savedVol = ""

	targetSink := c.duckSink
	switch backend {
	case BackendWpctl:
		if targetSink == "" {
			targetSink = "@DEFAULT_AUDIO_SINK@"
		}
		if out, err := c.runner("wpctl", "set-volume", targetSink, saved); err != nil {
			return fmt.Errorf("ducking: wpctl restore volume %s: %w (%s)", saved, err, strings.TrimSpace(string(out)))
		}
		return nil

	case BackendPactl:
		if targetSink == "" {
			targetSink = "@DEFAULT_SINK@"
		}
		if out, err := c.runner("pactl", "set-sink-volume", targetSink, saved); err != nil {
			return fmt.Errorf("ducking: pactl restore volume %s: %w (%s)", saved, err, strings.TrimSpace(string(out)))
		}
		return nil

	default:
		return fmt.Errorf("ducking: unknown backend %q", backend)
	}
}

func (c *CommandDucker) resolveBackend() Backend {
	if c.backend != BackendAuto && c.backend != "" {
		return c.backend
	}
	if len(c.duckStreams) > 0 {
		if _, err := exec.LookPath("pactl"); err == nil {
			return BackendPactl
		}
	}
	if _, err := exec.LookPath("wpctl"); err == nil {
		return BackendWpctl
	}
	return BackendPactl
}

type pactlSinkInput struct {
	id         string
	volume     string
	sink       string
	properties map[string]string
}

func (c *CommandDucker) matchesStreams(si pactlSinkInput) bool {
	if len(c.duckStreams) == 0 {
		return false
	}
	appName := si.properties["application.name"]
	mediaName := si.properties["media.name"]
	binaryName := si.properties["application.process.binary"]
	nodeName := si.properties["node.name"]

	for _, s := range c.duckStreams {
		target := strings.ToLower(strings.TrimSpace(s))
		if target == "" {
			continue
		}
		if (appName != "" && strings.Contains(strings.ToLower(appName), target)) ||
			(mediaName != "" && strings.Contains(strings.ToLower(mediaName), target)) ||
			(binaryName != "" && strings.Contains(strings.ToLower(binaryName), target)) ||
			(nodeName != "" && strings.Contains(strings.ToLower(nodeName), target)) {
			return true
		}
	}
	return false
}

func parsePactlSinkInputs(out string) []pactlSinkInput {
	var results []pactlSinkInput
	var current *pactlSinkInput

	lines := strings.Split(out, "\n")
	for _, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "Sink Input #") {
			if current != nil {
				results = append(results, *current)
			}
			id := strings.TrimPrefix(trimmed, "Sink Input #")
			current = &pactlSinkInput{
				id:         strings.TrimSpace(id),
				properties: make(map[string]string),
			}
			continue
		}

		if current == nil {
			continue
		}

		if strings.HasPrefix(trimmed, "Sink:") {
			current.sink = strings.TrimSpace(strings.TrimPrefix(trimmed, "Sink:"))
			continue
		}

		if strings.HasPrefix(trimmed, "Volume:") {
			vol, err := parsePactlVolume(trimmed)
			if err == nil {
				current.volume = vol
			}
			continue
		}

		if idx := strings.Index(trimmed, "="); idx != -1 {
			key := strings.TrimSpace(trimmed[:idx])
			val := strings.TrimSpace(trimmed[idx+1:])
			val = strings.Trim(val, `"`)
			current.properties[key] = val
		}
	}

	if current != nil {
		results = append(results, *current)
	}

	return results
}

var wpctlVolRe = regexp.MustCompile(`Volume:\s*([0-9.]+)`)

func parseWpctlVolume(out string) (string, error) {
	m := wpctlVolRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return "", errors.New("could not parse volume from wpctl output: " + strings.TrimSpace(out))
	}
	return m[1], nil
}

var pactlPercentRe = regexp.MustCompile(`(\d+%)`)

func parsePactlVolume(out string) (string, error) {
	matches := pactlPercentRe.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		fields := strings.Fields(out)
		for _, f := range fields {
			if strings.HasSuffix(f, "%") {
				return f, nil
			}
		}
		return "", errors.New("could not parse volume from pactl output: " + strings.TrimSpace(out))
	}
	return matches[0][1], nil
}
