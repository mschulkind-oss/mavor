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

	backend := c.resolveBackend()

	var err error
	switch {
	case c.ducked:
		// Being already ducked does not make this a no-op. The volume to put
		// back is still the one read before the first Duck — reading it again
		// now would read the ducked level and adopt it as the baseline, which
		// is the one way this can lose the user's volume for good — but the
		// mute itself always goes out, because a ducker that skipped it would
		// leave the sound down for a whole dictation after a restore that
		// never landed.
		err = c.remuteLocked(backend)
	case len(c.duckStreams) > 0:
		err = c.duckStreamsInternal(backend)
	default:
		err = c.duckSinkInternal(backend)
	}
	if err != nil {
		return err
	}

	// Say what was ducked, in the terms the config was written in. The
	// daemon's own line covers the whole set of duckers and a mixer that
	// ducked nothing can sit beside it, so this is the line that keeps a
	// silent half of the set from reading as a success.
	attrs := []any{"backend", string(backend), "volume", c.duckVolume}
	if c.duckSink != "" {
		attrs = append(attrs, "sink", c.duckSink)
	}
	if len(c.duckStreams) > 0 {
		attrs = append(attrs, "apps", c.duckStreams)
	}
	if c.savedVol != "" {
		attrs = append(attrs, "was", c.savedVol)
	}
	c.logger.Info("ducking: sound server ducked", attrs...)
	return nil
}

func (c *CommandDucker) duckSinkInternal(backend Backend) error {
	switch backend {
	case BackendWpctl:
		out, err := c.runner("wpctl", "get-volume", c.sinkTarget(backend))
		if err != nil {
			return fmt.Errorf("ducking: wpctl get-volume: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		vol, err := parseWpctlVolume(string(out))
		if err != nil {
			return fmt.Errorf("ducking: parse wpctl volume: %w", err)
		}
		c.savedVol = vol

	case BackendPactl:
		out, err := c.runner("pactl", "get-sink-volume", c.sinkTarget(backend))
		if err != nil {
			return fmt.Errorf("ducking: pactl get-sink-volume: %w (%s)", err, strings.TrimSpace(string(out)))
		}
		vol, err := parsePactlVolume(string(out))
		if err != nil {
			return fmt.Errorf("ducking: parse pactl volume: %w", err)
		}
		c.savedVol = vol

	default:
		return fmt.Errorf("ducking: unknown backend %q", backend)
	}
	if err := c.writeSinkLevelLocked(backend); err != nil {
		return err
	}
	c.ducked = true
	return nil
}

// sinkTarget is the sink a command acts on.
func (c *CommandDucker) sinkTarget(backend Backend) string {
	if c.duckSink != "" {
		return c.duckSink
	}
	if backend == BackendWpctl {
		return "@DEFAULT_AUDIO_SINK@"
	}
	return "@DEFAULT_SINK@"
}

// sinkLevel is the volume a sink is ducked to.
func (c *CommandDucker) sinkLevel(backend Backend) string {
	if c.duckVolume != "" {
		return c.duckVolume
	}
	if backend == BackendWpctl {
		return "0.2"
	}
	return "20%"
}

// streamLevel is the volume a stream is ducked to.
func (c *CommandDucker) streamLevel() string {
	if c.duckVolume != "" {
		return c.duckVolume
	}
	return "20%"
}

// writeSinkLevelLocked writes the duck level to the sink and reads nothing, so
// a dictation that has to mute again can reuse it without adopting the level it
// wrote as the value to put back.
func (c *CommandDucker) writeSinkLevelLocked(backend Backend) error {
	target, level := c.sinkTarget(backend), c.sinkLevel(backend)
	switch backend {
	case BackendWpctl:
		if out, err := c.runner("wpctl", "set-volume", target, level); err != nil {
			return fmt.Errorf("ducking: wpctl set-volume %s: %w (%s)", level, err, strings.TrimSpace(string(out)))
		}
		return nil

	case BackendPactl:
		if out, err := c.runner("pactl", "set-sink-volume", target, level); err != nil {
			return fmt.Errorf("ducking: pactl set-sink-volume %s: %w (%s)", level, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return fmt.Errorf("ducking: unknown backend %q", backend)
}

// remuteLocked re-sends the mute to whatever was ducked, keeping the values
// saved before the first Duck. A parameter that was never read is left alone —
// there is nothing Restore could put back — which is the same contract the
// mixer has.
func (c *CommandDucker) remuteLocked(backend Backend) error {
	if len(c.savedStreams) > 0 {
		var errs []error
		for _, id := range sortedKeys(c.savedStreams) {
			if out, err := c.runner("pactl", "set-sink-input-volume", id, c.streamLevel()); err != nil {
				errs = append(errs, fmt.Errorf("ducking: pactl set-sink-input-volume %s %s: %w (%s)",
					id, c.streamLevel(), err, strings.TrimSpace(string(out))))
			}
		}
		return errors.Join(errs...)
	}
	if c.savedVol == "" {
		return nil
	}
	return c.writeSinkLevelLocked(backend)
}

// sortedKeys keeps command order deterministic, so logs and fakes read the
// same way twice.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (c *CommandDucker) duckStreamsInternal(backend Backend) error {
	out, err := c.runner("pactl", "list", "sink-inputs")
	if err != nil {
		return fmt.Errorf("ducking: pactl list sink-inputs: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	sinkInputs := parsePactlSinkInputs(string(out))
	duckVol := c.streamLevel()

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
	if err := c.restoreLocked(); err != nil {
		return err
	}
	was := c.savedVol
	c.ducked = false
	c.savedVol = ""
	c.savedStreams = nil

	attrs := []any{"backend", string(c.resolveBackend())}
	if was != "" {
		attrs = append(attrs, "volume", was)
	}
	c.logger.Info("ducking: sound server restored", attrs...)
	return nil
}

// restoreLocked puts the saved volume back without changing the ducker's own
// state. What was saved is forgotten only once the writes have landed, in
// Restore, so a restore that fails can be retried with the user's volume
// rather than with the level this ducker wrote over it.
func (c *CommandDucker) restoreLocked() error {
	backend := c.resolveBackend()

	if len(c.savedStreams) > 0 {
		var errs []error
		for _, id := range sortedKeys(c.savedStreams) {
			saved := c.savedStreams[id]
			if out, err := c.runner("pactl", "set-sink-input-volume", id, saved); err != nil {
				errs = append(errs, fmt.Errorf("ducking: pactl restore sink-input %s volume %s: %w (%s)", id, saved, err, strings.TrimSpace(string(out))))
			}
		}
		return errors.Join(errs...)
	}

	// Sink-level restore. Nothing was read before the first Duck, so there is
	// nothing to put back.
	if c.savedVol == "" {
		return nil
	}
	saved := c.savedVol
	targetSink := c.sinkTarget(backend)
	switch backend {
	case BackendWpctl:
		if out, err := c.runner("wpctl", "set-volume", targetSink, saved); err != nil {
			return fmt.Errorf("ducking: wpctl restore volume %s: %w (%s)", saved, err, strings.TrimSpace(string(out)))
		}
		return nil

	case BackendPactl:
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
