package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsAreTheDocumentedOnes(t *testing.T) {
	d := Default()

	if d.Model != "whisper-base.en" {
		t.Errorf("Model = %q, want whisper-base.en", d.Model)
	}
	if !d.Preview.Enabled {
		t.Error("Preview.Enabled = false, want true — the overlay shows text while you speak by default")
	}
	if d.Preview.Source != "auto" {
		t.Errorf("Preview.Source = %q, want auto", d.Preview.Source)
	}
	if d.Preview.PauseMS != 450 || d.Preview.MinPhraseMS != 600 {
		t.Errorf("Preview pause/min-phrase = %d/%d ms, want 450/600", d.Preview.PauseMS, d.Preview.MinPhraseMS)
	}
	if d.Ducking.Enabled {
		t.Error("Ducking.Enabled = true, want false — mavor does not touch host audio unless asked")
	}
	if d.Ducking.Volume != "0%" {
		t.Errorf("Ducking.Volume = %q, want 0%%", d.Ducking.Volume)
	}
	if d.Vocabulary.Boost != 1.5 {
		t.Errorf("Vocabulary.Boost = %v, want 1.5", d.Vocabulary.Boost)
	}
	if d.Overlay.TopMargin != 8 {
		t.Errorf("Overlay.TopMargin = %d, want 8", d.Overlay.TopMargin)
	}
	if d.Advanced.Placement != "auto" || d.Advanced.GPU != "auto" {
		t.Errorf("Advanced placement/gpu = %q/%q, want auto/auto", d.Advanced.Placement, d.Advanced.GPU)
	}
	if d.Advanced.Server != "" {
		t.Errorf("Advanced.Server = %q, want empty — nothing is remote unless a URL says so", d.Advanced.Server)
	}
	if d.Advanced.Threads != PhysicalCores() {
		t.Errorf("Advanced.Threads = %d, want the physical core count %d", d.Advanced.Threads, PhysicalCores())
	}
	for name, got := range map[string]string{
		"Paths.Models": d.Paths.Models,
		"Paths.Log":    d.Paths.Log,
		"Paths.Socket": d.Paths.Socket,
	} {
		if got == "" {
			t.Errorf("%s is empty, want a default path", name)
		}
	}
}

// Resolve on a Config that already came from Default() must be a no-op, or
// the round trip through the scaffolded file cannot be exact.
func TestResolveIsIdempotentOnDefaults(t *testing.T) {
	d := Default()
	got := d
	got.Resolve()
	if !reflect.DeepEqual(got, d) {
		t.Errorf("Resolve() changed Default():\n got %+v\nwant %+v", got, d)
	}
}

func TestLoadMissingFileUsesEveryDefault(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("Load on a missing file: %v, want no error", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("Load on a missing file = %+v, want Default() %+v", cfg, Default())
	}
}

func TestLoadFileReportsThatTheFileIsAbsent(t *testing.T) {
	f, err := LoadFile(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Exists {
		t.Error("Exists = true for a file that is not there")
	}
	if f.SchemaLooksStale() {
		t.Error("SchemaLooksStale() = true for a file that is not there")
	}
}

func TestLoadReadsEveryTable(t *testing.T) {
	path := writeConfig(t, `
model = "parakeet-tdt-0.6b"

[preview]
enabled = false
source = "phrases"
pause_ms = 300
min_phrase_ms = 900

[ducking]
enabled = true
volume = "25%"
apps = ["spotify", "firefox"]
sink = "alsa_output.pci-0000_00_1f.3.analog-stereo"

[vocabulary]
words = ["mavor", "wlroots"]
file = "/tmp/vocab.txt"
boost = 2.5

[overlay]
top_margin = 32

[advanced]
placement = "subprocess"
server = "http://127.0.0.1:8080"
threads = 3
gpu = "off"

[paths]
models = "/models"
log = "/logs/mavor.log"
socket = "/run/mavor.sock"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Model != "parakeet-tdt-0.6b" {
		t.Errorf("Model = %q", cfg.Model)
	}
	if cfg.Preview.Enabled {
		t.Error("Preview.Enabled = true, want the file's false")
	}
	if cfg.Preview.Source != "phrases" || cfg.Preview.PauseMS != 300 || cfg.Preview.MinPhraseMS != 900 {
		t.Errorf("Preview = %+v", cfg.Preview)
	}
	if !cfg.Ducking.Enabled || cfg.Ducking.Volume != "25%" || cfg.Ducking.Sink == "" {
		t.Errorf("Ducking = %+v", cfg.Ducking)
	}
	if !reflect.DeepEqual(cfg.Ducking.Apps, []string{"spotify", "firefox"}) {
		t.Errorf("Ducking.Apps = %v", cfg.Ducking.Apps)
	}
	if !reflect.DeepEqual(cfg.Vocabulary.Words, []string{"mavor", "wlroots"}) {
		t.Errorf("Vocabulary.Words = %v", cfg.Vocabulary.Words)
	}
	if cfg.Vocabulary.File != "/tmp/vocab.txt" || cfg.Vocabulary.Boost != 2.5 {
		t.Errorf("Vocabulary = %+v", cfg.Vocabulary)
	}
	if cfg.Overlay.TopMargin != 32 {
		t.Errorf("Overlay.TopMargin = %d, want 32", cfg.Overlay.TopMargin)
	}
	if cfg.Advanced.Placement != "subprocess" || cfg.Advanced.Server != "http://127.0.0.1:8080" ||
		cfg.Advanced.Threads != 3 || cfg.Advanced.GPU != "off" {
		t.Errorf("Advanced = %+v", cfg.Advanced)
	}
	if cfg.Paths.Models != "/models" || cfg.Paths.Log != "/logs/mavor.log" || cfg.Paths.Socket != "/run/mavor.sock" {
		t.Errorf("Paths = %+v", cfg.Paths)
	}
}

// The old schema resolved preset onto model and clobbered a model the user had
// written by hand whenever it matched the default string. preset is gone, and
// a model named in the file is the model that runs.
func TestAnExplicitModelSurvivesResolve(t *testing.T) {
	for _, name := range []string{"whisper-base.en", "whisper-tiny.en", "parakeet-tdt-0.6b"} {
		path := writeConfig(t, "model = "+strconv.Quote(name)+"\n")
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Model != name {
			t.Errorf("model = %q in the file loaded as %q", name, cfg.Model)
		}
	}
}

func TestPartialFileKeepsEveryOtherDefault(t *testing.T) {
	path := writeConfig(t, "[overlay]\ntop_margin = 16\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.Overlay.TopMargin = 16
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("one key in the file changed more than that key:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestUnknownKeyWarnsButDoesNotFail(t *testing.T) {
	path := writeConfig(t, "model = \"whisper-tiny.en\"\nengine = \"cli\"\n\n[preview]\nenabled = true\nmode = \"batch\"\n")
	f, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v, want an unknown key to be a warning and not an error", err)
	}
	if !reflect.DeepEqual(f.UnknownKeys, []string{"engine", "preview.mode"}) {
		t.Errorf("UnknownKeys = %v, want [engine preview.mode]", f.UnknownKeys)
	}
	if f.Model != "whisper-tiny.en" {
		t.Errorf("Model = %q — the known keys around an unknown one must still land", f.Model)
	}
	if f.SchemaLooksStale() {
		t.Error("SchemaLooksStale() = true for a file whose other keys are fine")
	}
}

// A file written against the pre-rewrite schema has no key mavor recognizes.
// doctor says so plainly rather than reporting "3 unknown keys", because the
// user's whole configuration is being ignored.
func TestFileOfOnlyOldKeysReadsAsAStaleSchema(t *testing.T) {
	path := writeConfig(t, "mode = \"streaming\"\npreset = \"fast\"\nengine = \"sherpa\"\nsherpa_model = \"zipformer-streaming\"\n")
	f, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if !f.SchemaLooksStale() {
		t.Fatalf("SchemaLooksStale() = false for a file of only old keys (unknown=%v known=%d)", f.UnknownKeys, f.KnownKeys)
	}
	if len(f.UnknownKeys) != 4 {
		t.Errorf("UnknownKeys = %v, want all four named", f.UnknownKeys)
	}
	if !reflect.DeepEqual(f.Config, Default()) {
		t.Error("a file of only old keys must leave every default in place")
	}
}

func TestUnknownTableCountsAsUnknown(t *testing.T) {
	path := writeConfig(t, "model = \"whisper-tiny.en\"\n\n[sherpa]\nprovider = \"cuda\"\ndecoding_method = \"greedy_search\"\n")
	f, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(f.UnknownKeys) == 0 {
		t.Fatal("an unknown table was not reported")
	}
	if f.KnownKeys != 1 {
		t.Errorf("KnownKeys = %d, want 1 — model is the only key the schema has here", f.KnownKeys)
	}
}

func TestThreadsAtOrBelowZeroAutodetects(t *testing.T) {
	for _, v := range []string{"0", "-4"} {
		path := writeConfig(t, "[advanced]\nthreads = "+v+"\n")
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Advanced.Threads != PhysicalCores() {
			t.Errorf("threads = %s loaded as %d, want the autodetected %d", v, cfg.Advanced.Threads, PhysicalCores())
		}
	}
}

// PhysicalCores counts distinct core_id values, which is how Linux says how
// many cores are behind the logical CPUs. The fake topology here is four
// hyperthreads on two cores.
func TestPhysicalCoresCountsDistinctCoreIDs(t *testing.T) {
	root := t.TempDir()
	for cpu, coreID := range map[string]string{"cpu0": "0", "cpu1": "1", "cpu2": "0", "cpu3": "1"} {
		dir := filepath.Join(root, cpu, "topology")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "core_id"), []byte(coreID+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := cpuTopologyRoot
	cpuTopologyRoot = root
	defer func() { cpuTopologyRoot = old }()

	if got := PhysicalCores(); got != 2 {
		t.Errorf("PhysicalCores() = %d for four hyperthreads on two cores, want 2", got)
	}
}

func TestPhysicalCoresFallsBackWhenTopologyIsUnreadable(t *testing.T) {
	old := cpuTopologyRoot
	cpuTopologyRoot = filepath.Join(t.TempDir(), "no-such-sysfs")
	defer func() { cpuTopologyRoot = old }()

	if got := PhysicalCores(); got < 1 {
		t.Errorf("PhysicalCores() = %d with no topology to read, want at least 1", got)
	}
}

func TestNegativeTopMarginClampsToZero(t *testing.T) {
	path := writeConfig(t, "[overlay]\ntop_margin = -20\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Overlay.TopMargin != 0 {
		t.Errorf("top_margin = -20 loaded as %d, want 0", cfg.Overlay.TopMargin)
	}
}

func TestNonPositivePreviewTimingsUseTheDefaults(t *testing.T) {
	path := writeConfig(t, "[preview]\npause_ms = 0\nmin_phrase_ms = -1\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Preview.PauseMS != 450 {
		t.Errorf("pause_ms = 0 loaded as %d, want 450", cfg.Preview.PauseMS)
	}
	if cfg.Preview.MinPhraseMS != 600 {
		t.Errorf("min_phrase_ms = -1 loaded as %d, want 600", cfg.Preview.MinPhraseMS)
	}
}

func TestLoadExpandsTildeAndEnvInEveryPath(t *testing.T) {
	t.Setenv("TEST_MAVOR_RUNTIME", "/tmp/test-run")
	path := writeConfig(t, `
[vocabulary]
file = "~/words.txt"

[paths]
models = "~/custom-models"
log = "$TEST_MAVOR_RUNTIME/mavor.log"
socket = "$TEST_MAVOR_RUNTIME/custom.sock"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, "custom-models"); cfg.Paths.Models != want {
		t.Errorf("Paths.Models = %q, want %q", cfg.Paths.Models, want)
	}
	if want := filepath.Join(home, "words.txt"); cfg.Vocabulary.File != want {
		t.Errorf("Vocabulary.File = %q, want %q", cfg.Vocabulary.File, want)
	}
	if cfg.Paths.Log != "/tmp/test-run/mavor.log" {
		t.Errorf("Paths.Log = %q, want /tmp/test-run/mavor.log", cfg.Paths.Log)
	}
	if cfg.Paths.Socket != "/tmp/test-run/custom.sock" {
		t.Errorf("Paths.Socket = %q, want /tmp/test-run/custom.sock", cfg.Paths.Socket)
	}
}

// `mavor config show` marshals the resolved config; saving that output as the
// config file and loading it again must produce the same values. This is
// §10.6 of the design, and it is what stops a marshalled default from being
// unreadable on the way back in.
func TestConfigShowOutputRoundTrips(t *testing.T) {
	original, err := Load(writeConfig(t, "model = \"whisper-small.en\"\n\n[ducking]\nenabled = true\napps = [\"spotify\"]\n\n[vocabulary]\nwords = [\"mavor\"]\n"))
	if err != nil {
		t.Fatal(err)
	}

	body, err := toml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	reloaded, err := Load(writeConfig(t, string(body)))
	if err != nil {
		t.Fatalf("reload of `config show` output: %v\n%s", err, body)
	}
	if !reflect.DeepEqual(reloaded, original) {
		t.Errorf("`config show` output does not reload to the same config:\n got %+v\nwant %+v\nfile:\n%s", reloaded, original, body)
	}
}

func TestDefaultConfigRoundTrips(t *testing.T) {
	body, err := toml.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(writeConfig(t, string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, Default()) {
		t.Errorf("marshalled defaults did not reload to Default():\n got %+v\nwant %+v\nfile:\n%s", reloaded, Default(), body)
	}
}

// A marshalled config must not carry a key the loader then rejects: that is
// the same drift class as the scaffolded template, one round trip later.
func TestConfigShowOutputHasNoUnknownKeys(t *testing.T) {
	body, err := toml.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile(writeConfig(t, string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.UnknownKeys) > 0 {
		t.Errorf("marshalling Default() produced keys the loader does not know: %v", f.UnknownKeys)
	}
}

func TestLoadInvalidTOMLReturnsError(t *testing.T) {
	path := writeConfig(t, "[overlay]\ntop_margin = \"not a number\"\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected a parse error on a value of the wrong type")
	} else if !strings.Contains(err.Error(), "config: ") {
		t.Errorf("error %q does not carry the package prefix", err)
	}
}

func TestPathHonorsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	if got := Path(); got != "/custom/xdg/mavor/config.toml" {
		t.Fatalf("Path = %q, want /custom/xdg/mavor/config.toml", got)
	}
}

// DefaultModelDir and defaultLogFile derive from the XDG base directories, so
// each has to be pinned in a test or it reads the developer's real home.

func TestDefaultModelDirHonorsXDGCacheHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", base)

	want := filepath.Join(base, "mavor", "models")
	if got := DefaultModelDir(); got != want {
		t.Errorf("DefaultModelDir() = %q, want %q", got, want)
	}
}

func TestDefaultLogPathHonorsXDGStateHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)

	want := filepath.Join(base, "mavor", "daemon.log")
	if got := Default().Paths.Log; got != want {
		t.Errorf("Default().Paths.Log = %q, want %q", got, want)
	}
}

func TestGPUDefaultsToAutoWhenUnset(t *testing.T) {
	cfg, err := Load(writeConfig(t, `model = "whisper-tiny.en"`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Advanced.GPU != "auto" {
		t.Errorf("GPU = %q with no gpu key, want auto", cfg.Advanced.GPU)
	}
	if cfg.GPUOff() {
		t.Error("GPUOff() = true with no gpu key, want false")
	}
}

func TestGPUOffIsRead(t *testing.T) {
	cfg, err := Load(writeConfig(t, "[advanced]\ngpu = \"off\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GPUOff() {
		t.Errorf("GPUOff() = false for gpu = %q, want true", cfg.Advanced.GPU)
	}
}

// A Config literal that never went through Resolve still has to mean "auto",
// because that is how every test and every in-process caller builds one.
func TestZeroValueGPUMeansAuto(t *testing.T) {
	if (Config{}).GPUOff() {
		t.Error("GPUOff() = true for the zero Config, want false (auto)")
	}
}

func TestGPUOffIgnoresCaseAndSpacing(t *testing.T) {
	for _, v := range []string{"off", "OFF", " Off "} {
		if !(Config{Advanced: Advanced{GPU: v}}).GPUOff() {
			t.Errorf("GPUOff() = false for gpu = %q, want true", v)
		}
	}
}
