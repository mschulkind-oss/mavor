// Package config loads the daemon's TOML configuration from
// $XDG_CONFIG_HOME/mavor/config.toml. A missing file is not an error — the
// daemon falls back to Default() so first-run users get sane behavior
// without having to write a config file.
//
// The schema is one top-level key, `model`, plus six tables. It is described
// in full, with the reasoning, in docs/design/configuration-surface.md §8;
// the scaffolded `mavor config init` file is generated from Default() so the
// two cannot disagree.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// Default values that more than one place needs to state. Every one of them
// is also the value Default() carries; these constants exist so a clamp and
// the default cannot drift apart.
const (
	DefaultModel       = "whisper-base.en"
	DefaultPauseMS     = 450
	DefaultMinPhraseMS = 600
	DefaultTopMargin   = 8
	// DefaultPreviewWidth caps the preview at half the screen.
	DefaultPreviewWidth = 0.5
	DefaultDuckVolume   = "0%"
	DefaultBoost        = 1.5
)

// Config is the whole configuration. Field order follows the scaffolded file.
type Config struct {
	// Model is a catalog name, e.g. "whisper-base.en" or "parakeet-tdt-0.6b",
	// as `mavor models list` prints them. It is not a filename: a whisper
	// model keeps the name upstream serves it under, and
	// speech.WhisperModelPath maps between the two.
	//
	// The model decides the runtime — whisper models run on whisper.cpp,
	// everything else on ONNX Runtime through sherpa-onnx — which is why
	// there is no engine key. See internal/models.RuntimeFor.
	Model string `toml:"model"`

	Preview    Preview    `toml:"preview"`
	Ducking    Ducking    `toml:"ducking"`
	Vocabulary Vocabulary `toml:"vocabulary"`
	Logging    Logging    `toml:"logging"`
	Output     Output     `toml:"output"`
	Overlay    Overlay    `toml:"overlay"`
	Advanced   Advanced   `toml:"advanced"`
	Paths      Paths      `toml:"paths"`
}

// Logging configures how much the daemon says about what it is doing. Where
// it says it is Paths.Log, which stays with the other filesystem locations.
type Logging struct {
	// Verbose drops the daemon to debug level, which turns on the
	// per-frame and per-chunk detail the quiet levels leave out: overlay
	// surface sizes and repaint timings, preview chunk cadence and text
	// growth, and how long each stage of a dictation took.
	//
	// Off by default because it is genuinely noisy — the preview alone logs
	// on a 30 ms tick. Turn it on when something is wrong and you want the
	// next occurrence explained rather than reproduced. `mavor daemon -v`
	// does the same for one run; the flag wins when both are set.
	Verbose bool `toml:"verbose"`
}

// Output configures what mavor does with a finished transcript. Typing it into
// the focused window is not optional — it is the product — so the only choice
// here is what else happens.
type Output struct {
	// TypingDelayMS is the pause wtype leaves between keystrokes, in
	// milliseconds. Unset leaves wtype's own default alone, which is the
	// behaviour mavor has always had.
	//
	// It is a pointer so that "unset" and "zero" are different requests:
	// zero is a deliberate ask for no delay, and there would be no way to
	// express it otherwise. Typing is per-character and is usually the long
	// pole on a long dictation — the `emit_chars_per_sec` figure at the end
	// of each cycle is what says whether changing this helped.
	//
	// Lower is not always better: an application that drops synthetic
	// keystrokes will drop more of them the faster they arrive.
	TypingDelayMS *int `toml:"typing_delay_ms"`

	// Clipboard also copies each transcript, replacing whatever was on the
	// clipboard before.
	//
	// Off by default. It makes a keystroke that lands in the wrong window
	// recoverable, which is a real benefit, but it costs the user their
	// clipboard on every utterance without asking — and someone who
	// dictates into an editor while holding a URL to paste loses the URL.
	// A recovery path that destroys unrelated state is opt-in; `mavor
	// history --copy` recovers a transcript on demand without it.
	Clipboard bool `toml:"clipboard"`
}

// Preview configures the text shown in the overlay while you speak. It is
// never typed: the final transcript always comes from Model, produced once,
// when you release the key.
type Preview struct {
	// Enabled turns the live preview on. With it off the overlay shows only
	// that mavor is recording.
	Enabled bool `toml:"enabled"`

	// Source is where the preview text comes from: "auto", "phrases", or the
	// name of a model to run alongside Model as the preview source.
	Source string `toml:"source"`

	// PauseMS is how long a pause ends a phrase, in milliseconds. "phrases"
	// only.
	PauseMS int `toml:"pause_ms"`

	// MinPhraseMS is how much speech a phrase needs before a pause can end
	// it, in milliseconds. "phrases" only.
	MinPhraseMS int `toml:"min_phrase_ms"`
}

// Ducking lowers other audio while mavor is recording.
type Ducking struct {
	Enabled bool `toml:"enabled"`

	// Volume is what other audio is set to: a percentage ("0%", "25%") or a
	// fraction ("0", "0.25"). "0%" mutes.
	Volume string `toml:"volume"`

	// Apps names the application or media streams to duck. Empty means every
	// stream, which is the default.
	Apps []string `toml:"apps"`

	// Sink is a specific output to act on instead of the default one.
	Sink string `toml:"sink"`
}

// Vocabulary is the words the model gets wrong: names, jargon, commands.
// How it reaches a model depends on the model — a prompt for whisper, a
// hotwords file for a transducer, nothing at all for the rest — which is why
// the table is stated in runtime-neutral terms.
type Vocabulary struct {
	Words []string `toml:"words"`

	// File is a path to a file with one phrase per line, unioned with Words.
	File string `toml:"file"`

	// Boost is the per-token score added while decoding whenever a
	// hypothesis extends a listed phrase. Transducer models only. 1.5 to 3.0
	// is the useful range.
	Boost float32 `toml:"boost"`
}

// Overlay configures the on-screen HUD.
type Overlay struct {
	// PreviewWidth caps the live preview strip as a fraction of the screen
	// width, in (0,1]. The preview shows the tail of what you have said so
	// far on one line; without a cap a long dictation grows the overlay
	// past the edge of the screen, and every resize re-centres it, so it
	// walks sideways while you speak.
	//
	// 0.5 by default. Values outside (0,1] are clamped to it. On a
	// compositor that advertises no wl_output the screen width is unknown
	// and the cap falls back to a fixed budget.
	PreviewWidth float64 `toml:"preview_width"`

	// TopMargin is the gap (px) between the overlay and the top of the
	// usable area — which is below Waybar, not the screen edge.
	//
	// The overlay is a layer-shell surface that never sets an exclusive zone,
	// so the compositor places it inside the space other surfaces have
	// reserved. Waybar's height is read from Waybar's own exclusive zone by
	// the compositor; mavor never learns it and does not need to. A bar of any
	// height, or no bar at all, is handled without changing this value.
	TopMargin int `toml:"top_margin"`
}

// Advanced holds the settings mavor picks for you. A key belongs here only if
// mavor cannot compute the right value — see the design's principle P1.
type Advanced struct {
	// Placement is where the model's runtime executes: "auto", or
	// "subprocess" to spawn a fresh whisper-cli per utterance. The other
	// placements are derived from the model and cannot be asked for. See
	// internal/models.Select.
	Placement string `toml:"placement"`

	// Server is the URL of a whisper server you run yourself. Setting it
	// makes Placement irrelevant: audio goes to that URL.
	Server string `toml:"server"`

	// Threads is the number of CPU threads inference may use. It defaults to
	// this machine's physical core count, where the measured thread-scaling
	// curve flattens.
	Threads int `toml:"threads"`

	// GPU is "auto" or "off", and applies to whisper models only.
	// whisper.cpp uses whatever GPU backend its build loaded, for the whole
	// model or not at all, and the only control it offers is -ng/--no-gpu,
	// which "off" maps to. There is no layer count to set.
	//
	// sherpa models run on the CPU whatever this says, because the ONNX
	// Runtime vendored by the Go binding is a CPU-only build. `mavor doctor`
	// reports which backend actually loaded, which is the only reliable
	// answer.
	GPU string `toml:"gpu"`
}

// Paths is where mavor keeps its files.
type Paths struct {
	// Models is the model cache directory.
	Models string `toml:"models"`
	// Log is the daemon log destination.
	Log string `toml:"log"`
	// Socket is the daemon's IPC socket path.
	Socket string `toml:"socket"`
}

// Default is the configuration a machine with no config file runs. It is the
// single source of the defaults: `mavor config init` scaffolds its file from
// this value rather than from a second literal, which is what stopped the two
// drifting apart.
func Default() Config {
	return Config{
		Model: DefaultModel,
		Preview: Preview{
			Enabled:     true,
			Source:      "auto",
			PauseMS:     DefaultPauseMS,
			MinPhraseMS: DefaultMinPhraseMS,
		},
		Ducking: Ducking{
			Enabled: false,
			Volume:  DefaultDuckVolume,
		},
		Vocabulary: Vocabulary{
			Boost: DefaultBoost,
		},
		Logging: Logging{
			// Off: debug level logs on every preview tick and every
			// overlay repaint.
			Verbose: false,
		},
		Output: Output{
			// Off: see the field's own comment. Typing is the product;
			// clobbering the clipboard is a side effect nobody asked for.
			Clipboard: false,
		},
		Overlay: Overlay{
			TopMargin:    DefaultTopMargin,
			PreviewWidth: DefaultPreviewWidth,
		},
		Advanced: Advanced{
			Placement: "auto",
			Threads:   PhysicalCores(),
			GPU:       "auto",
		},
		Paths: Paths{
			Models: defaultModelDir(),
			Log:    defaultLogFile(),
			Socket: defaultSocket(),
		},
	}
}

// Resolve fills in every value a config file left empty or out of range. It is
// idempotent, and running it on a Config that came from Default() changes
// nothing — which is what makes the scaffolded-template test meaningful.
//
// The clamps here are the §10.1 table of the design: a degenerate value takes
// the default rather than being rejected, because none of them is worth
// refusing to start over.
func (c *Config) Resolve() {
	if c.Model == "" {
		c.Model = DefaultModel
	}

	if c.Preview.Source == "" {
		c.Preview.Source = "auto"
	}
	if c.Preview.PauseMS <= 0 {
		c.Preview.PauseMS = DefaultPauseMS
	}
	if c.Preview.MinPhraseMS <= 0 {
		c.Preview.MinPhraseMS = DefaultMinPhraseMS
	}

	if c.Ducking.Volume == "" {
		c.Ducking.Volume = DefaultDuckVolume
	}
	if len(c.Ducking.Apps) == 0 {
		c.Ducking.Apps = nil
	}

	if len(c.Vocabulary.Words) == 0 {
		c.Vocabulary.Words = nil
	}
	if c.Vocabulary.Boost <= 0 {
		c.Vocabulary.Boost = DefaultBoost
	}

	// A fraction outside (0,1] is meaningless rather than merely extreme —
	// zero would hide the preview and 2.0 would ask for twice the screen —
	// so both fall back to the default rather than being honoured.
	if c.Overlay.PreviewWidth <= 0 || c.Overlay.PreviewWidth > 1 {
		c.Overlay.PreviewWidth = DefaultPreviewWidth
	}
	if c.Overlay.TopMargin < 0 {
		c.Overlay.TopMargin = 0
	}

	if c.Advanced.Placement == "" {
		c.Advanced.Placement = "auto"
	}
	if c.Advanced.GPU == "" {
		c.Advanced.GPU = "auto"
	}
	if c.Advanced.Threads <= 0 {
		c.Advanced.Threads = PhysicalCores()
	}

	if c.Paths.Models == "" {
		c.Paths.Models = defaultModelDir()
	}
	if c.Paths.Log == "" {
		c.Paths.Log = defaultLogFile()
	}
	if c.Paths.Socket == "" {
		c.Paths.Socket = defaultSocket()
	}
}

// GPUOff reports whether the configuration forbids GPU use. Anything other
// than "off" — including the empty value a Config literal starts with — means
// "auto", so a caller that never ran Resolve still gets the default.
func (c Config) GPUOff() bool {
	return strings.EqualFold(strings.TrimSpace(c.Advanced.GPU), "off")
}

// Path returns the canonical config file location. Honors XDG_CONFIG_HOME.
func Path() string {
	return filepath.Join(xdgConfigHome(), "mavor", "config.toml")
}

// ExpandPath expands environment variables and converts a leading ~ to the user's home directory.
func ExpandPath(p string) string {
	if p == "" {
		return p
	}
	p = os.ExpandEnv(p)
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

// File is what Load learned about a config file besides the values in it. The
// extra facts exist for `mavor doctor`, which reports as an error what the
// daemon only warns about.
type File struct {
	Config

	// Path is the file that was read, whether or not it exists.
	Path string

	// Exists reports whether there was a file at all. A missing file is not
	// an error: every default applies.
	Exists bool

	// UnknownKeys are dotted key paths present in the file that the schema
	// does not have. They are warned about and otherwise ignored — the
	// schema was rewritten without compatibility aliases, so an old file's
	// keys land here.
	UnknownKeys []string

	// KnownKeys counts the keys in the file that the schema does have.
	KnownKeys int
}

// SchemaLooksStale reports a file whose every key is unknown, which is what a
// config written against the pre-rewrite schema looks like. `mavor doctor`
// says so plainly and points at `mavor config init --force`, because such a
// file silently contributes nothing.
func (f File) SchemaLooksStale() bool {
	return f.Exists && f.KnownKeys == 0 && len(f.UnknownKeys) > 0
}

// LoadFile reads path and reports what it found. If path is empty, Path() is
// used. A missing file is treated as "no overrides" and Default() is returned
// without error; an unknown key is recorded rather than refused.
func LoadFile(path string) (File, error) {
	if path == "" {
		path = Path()
	}
	out := File{Config: Default(), Path: path}

	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			out.Config.Resolve()
			return out, nil
		}
		return out, fmt.Errorf("config: read %s: %w", path, err)
	}
	out.Exists = true

	dec := toml.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out.Config); err != nil {
		var strict *toml.StrictMissingError
		if !errors.As(err, &strict) {
			return out, fmt.Errorf("config: parse %s: %w", path, err)
		}
		for _, e := range strict.Errors {
			out.UnknownKeys = append(out.UnknownKeys, strings.Join(e.Key(), "."))
		}
		sort.Strings(out.UnknownKeys)
	}
	out.KnownKeys = countKnownKeys(body, out.UnknownKeys)

	out.Config.Paths.Models = ExpandPath(out.Config.Paths.Models)
	out.Config.Paths.Log = ExpandPath(out.Config.Paths.Log)
	out.Config.Paths.Socket = ExpandPath(out.Config.Paths.Socket)
	out.Config.Vocabulary.File = ExpandPath(out.Config.Vocabulary.File)
	out.Config.Resolve()
	return out, nil
}

// Load reads a config file and returns just the values. It is what every
// command that only wants the settings calls.
//
// It says nothing about an unknown key. Warning is the caller's to do, once,
// somewhere a person will read it: the daemon logs the warnings at start (see
// File.LogWarnings) and `mavor doctor` reports them as an error. A `Load`
// that warned would repeat itself for every check doctor runs and for every
// `mavor status`.
func Load(path string) (Config, error) {
	f, err := LoadFile(path)
	return f.Config, err
}

// LogWarnings writes one warning per key the schema does not have, and one
// more for a file that is entirely stale. The daemon calls it at start, after
// its logger exists, because a key that is silently ignored is the failure
// mode this schema rewrite is most likely to produce.
func (f File) LogWarnings(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, k := range f.UnknownKeys {
		logger.Warn("config: unknown key, ignored", "key", k, "file", f.Path)
	}
	if f.SchemaLooksStale() {
		logger.Warn("config: no key in this file is part of the current schema — it predates the rewrite; `mavor config init --force` scaffolds the new one",
			"file", f.Path)
	}
}

// countKnownKeys counts the leaf keys in a TOML document that the schema
// recognizes: every leaf, minus the ones reported unknown and the ones nested
// inside an unknown table. It exists so `doctor` can tell a file with one
// stale key from a file that is entirely stale.
func countKnownKeys(body []byte, unknown []string) int {
	var doc map[string]any
	if err := toml.Unmarshal(body, &doc); err != nil {
		return 0
	}
	bad := make(map[string]bool, len(unknown))
	for _, u := range unknown {
		bad[u] = true
	}
	known := 0
	for _, leaf := range leafKeys(doc, "") {
		if isUnknownKey(leaf, bad) {
			continue
		}
		known++
	}
	return known
}

func isUnknownKey(leaf string, bad map[string]bool) bool {
	for prefix := leaf; prefix != ""; {
		if bad[prefix] {
			return true
		}
		i := strings.LastIndex(prefix, ".")
		if i < 0 {
			break
		}
		prefix = prefix[:i]
	}
	return false
}

// leafKeys flattens a decoded TOML document to dotted paths, one per value
// that is not itself a table. Sub-tables are traversed; arrays are leaves,
// because that is how the schema treats them.
func leafKeys(doc map[string]any, prefix string) []string {
	var out []string
	for k, v := range doc {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			out = append(out, leafKeys(sub, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

// PhysicalCores reports how many physical CPU cores this machine has, which
// is where the measured thread-scaling curve flattens: on a 6-core/12-thread
// machine, 6 threads was best or within noise for every model and 8 bought
// nothing.
//
// Linux publishes the topology under /sys, one core_id per logical CPU, so
// counting the distinct values counts the cores behind the hyperthreads. A
// kernel that does not publish it — or any other OS — falls back to the
// logical count, which is never zero.
func PhysicalCores() int {
	if n := countCoreIDs(); n > 0 {
		return n
	}
	if n := runtime.NumCPU(); n > 0 {
		return n
	}
	return 1
}

// cpuTopologyRoot is /sys/devices/system/cpu, as a variable so a test can
// point it at a directory it built.
var cpuTopologyRoot = "/sys/devices/system/cpu"

func countCoreIDs() int {
	matches, err := filepath.Glob(filepath.Join(cpuTopologyRoot, "cpu[0-9]*", "topology", "core_id"))
	if err != nil {
		return 0
	}
	seen := map[string]bool{}
	for _, m := range matches {
		body, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		seen[strings.TrimSpace(string(body))] = true
	}
	return len(seen)
}

// XDGDataHome returns the canonical data directory, honoring XDG_DATA_HOME.
func XDGDataHome() string {
	return xdgDataHome()
}

func xdgConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(homeDir(), ".config")
}

func xdgDataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(homeDir(), ".local", "share")
}

func xdgCacheHome() string {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return v
	}
	return filepath.Join(homeDir(), ".cache")
}

func xdgRuntimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return v
	}
	// Falls back to /tmp/mavor-<uid> so multi-user systems don't collide.
	return filepath.Join("/tmp", "mavor-"+strconv.Itoa(os.Getuid()))
}

// XDGCacheHome returns the canonical cache directory, honoring XDG_CACHE_HOME.
func XDGCacheHome() string {
	return xdgCacheHome()
}

// DefaultModelDir returns the default model cache directory (~/.cache/mavor/models).
func DefaultModelDir() string {
	return defaultModelDir()
}

// DefaultSocket returns the default daemon IPC socket path ($XDG_RUNTIME_DIR/mavor.sock).
func DefaultSocket() string {
	return defaultSocket()
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}

func defaultModelDir() string {
	return filepath.Join(xdgCacheHome(), "mavor", "models")
}

func defaultSocket() string {
	return filepath.Join(xdgRuntimeDir(), "mavor.sock")
}

func defaultLogFile() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(homeDir(), ".local", "state")
	}
	return filepath.Join(stateHome, "mavor", "daemon.log")
}
