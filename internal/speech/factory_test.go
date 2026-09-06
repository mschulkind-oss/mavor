package speech

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
)

// whisperConfig builds a config for a whisper model with a stub GGML file on
// disk, which is what Resolve checks for. The file's contents never matter:
// the model is opened by whisper.cpp, in another process.
func whisperConfig(t *testing.T, name string) config.Config {
	t.Helper()
	modelDir := t.TempDir()
	if err := os.WriteFile(WhisperModelPath(modelDir, name), []byte("fake-model"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Model = name
	cfg.Paths.Models = modelDir
	return cfg
}

// A whisper model runs on whisper.cpp, and by default in a warm supervised
// server: a measured 207 ms to 1.45 s per utterance cheaper than reloading
// the model each time. Nothing in the config says so — it follows from the
// model.
func TestWhisperModelDefaultsToASupervisedWarmServer(t *testing.T) {
	// The warm placement is only available where the binary that holds the
	// model is: with no whisper-server on PATH the daemon deliberately
	// downgrades to a subprocess (see AdjustForEnvironment). This test is
	// about the derivation, so it guarantees the environment rather than
	// inheriting whatever the host happens to have installed.
	pathWith(t, "whisper-server")
	cfg := whisperConfig(t, "whisper-base.en")

	res, err := Resolve(cfg)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Runtime != models.RuntimeWhisper {
		t.Errorf("Runtime = %q, want %q", res.Runtime, models.RuntimeWhisper)
	}
	if res.Placement != models.PlacementLocalServer {
		t.Errorf("Placement = %q, want %q", res.Placement, models.PlacementLocalServer)
	}
	if res.Reason == "" {
		t.Error("Reason is empty; doctor prints it to explain the placement")
	}

	transcriber, err := FactoryFor(cfg, res, slog.Default())
	if err != nil {
		t.Fatalf("FactoryFor: %v", err)
	}
	st, ok := transcriber.(*ServerTranscriber)
	if !ok {
		t.Fatalf("got %T, want *ServerTranscriber", transcriber)
	}
	if st.Supervisor == nil {
		t.Fatal("a local server placement must supervise the child that holds the model")
	}
	if st.Supervisor.cfg.ModelPath != res.ModelPath {
		t.Errorf("supervisor ModelPath = %q, want %q", st.Supervisor.cfg.ModelPath, res.ModelPath)
	}
	if st.Supervisor.cfg.Threads != cfg.Advanced.Threads {
		t.Errorf("supervisor Threads = %d, want the configured %d", st.Supervisor.cfg.Threads, cfg.Advanced.Threads)
	}
}

// A sherpa model is linked into the daemon and stays resident. There is no
// server to run and no process to spawn.
func TestSherpaModelDefaultsToInProcess(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	modelDir := sherpaTransducerDir(t)
	cfg := config.Default()
	cfg.Model = modelDir
	cfg.Paths.Models = t.TempDir()

	res, err := Resolve(cfg)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Runtime != models.RuntimeSherpa {
		t.Errorf("Runtime = %q, want %q", res.Runtime, models.RuntimeSherpa)
	}
	if res.Placement != models.PlacementInProcess {
		t.Errorf("Placement = %q, want %q", res.Placement, models.PlacementInProcess)
	}
	if res.ModelDir != modelDir {
		t.Errorf("ModelDir = %q, want %q", res.ModelDir, modelDir)
	}

	transcriber, err := FactoryFor(cfg, res, slog.Default())
	if err != nil {
		t.Fatalf("FactoryFor: %v", err)
	}
	if _, ok := transcriber.(*SherpaTranscriber); !ok {
		t.Fatalf("got %T, want *SherpaTranscriber", transcriber)
	}
}

// subprocess is the one placement a user can ask for, and it means a fresh
// whisper-cli per utterance.
func TestSubprocessPlacementBuildsTheCLI(t *testing.T) {
	cfg := whisperConfig(t, "whisper-base.en")
	cfg.Advanced.Placement = "subprocess"
	cfg.Advanced.Threads = 4

	transcriber, err := Factory(cfg, slog.Default())
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	cli, ok := transcriber.(*WhisperCli)
	if !ok {
		t.Fatalf("got %T, want *WhisperCli", transcriber)
	}
	if want := WhisperModelPath(cfg.Paths.Models, cfg.Model); cli.ModelPath != want {
		t.Errorf("ModelPath = %q, want %q", cli.ModelPath, want)
	}
	if cli.Threads != 4 {
		t.Errorf("Threads = %d, want 4", cli.Threads)
	}
	// An unset gpu key means "auto": whisper.cpp decides, mavor does not
	// pass -ng.
	if cli.NoGPU {
		t.Error("NoGPU = true with gpu = \"auto\", want false")
	}
}

// A sherpa model has no per-utterance command to spawn, so asking for one is
// a configuration error rather than something quietly ignored.
func TestSubprocessPlacementIsRefusedForASherpaModel(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := config.Default()
	cfg.Model = sherpaTransducerDir(t)
	cfg.Paths.Models = t.TempDir()
	cfg.Advanced.Placement = "subprocess"

	_, err := Resolve(cfg)
	if err == nil {
		t.Fatal("Resolve() = nil error for subprocess on a sherpa model, want a refusal")
	}
	if !strings.Contains(err.Error(), "subprocess") {
		t.Errorf("error %q does not name the placement that was refused", err)
	}
}

// The two placements a user cannot ask for are derived, and naming one is the
// same mistake as naming a value that does not exist.
func TestUnaskablePlacementsAreRefused(t *testing.T) {
	for _, placement := range []string{"local-server", "in-process", "remote", "server", "cli"} {
		cfg := whisperConfig(t, "whisper-base.en")
		cfg.Advanced.Placement = placement
		if _, err := Resolve(cfg); err == nil {
			t.Errorf("placement = %q was accepted, want a refusal", placement)
		}
	}
}

// advanced.server names a whisper server someone else runs. It implies a
// remote placement and there is nothing local to check.
func TestServerURLMakesThePlacementRemote(t *testing.T) {
	cfg := config.Default()
	cfg.Model = "whisper-base.en"
	cfg.Paths.Models = t.TempDir()
	cfg.Advanced.Server = "http://127.0.0.1:8080"

	res, err := Resolve(cfg)
	if err != nil {
		t.Fatalf("Resolve: %v, want a remote placement to need no local model", err)
	}
	if res.Placement != models.PlacementRemote {
		t.Errorf("Placement = %q, want %q", res.Placement, models.PlacementRemote)
	}

	transcriber, err := FactoryFor(cfg, res, slog.Default())
	if err != nil {
		t.Fatalf("FactoryFor: %v", err)
	}
	st, ok := transcriber.(*ServerTranscriber)
	if !ok {
		t.Fatalf("got %T, want *ServerTranscriber", transcriber)
	}
	if st.Endpoint != "http://127.0.0.1:8080" {
		t.Errorf("Endpoint = %q, want the configured URL", st.Endpoint)
	}
	if st.Supervisor != nil {
		t.Error("a remote server is not mavor's to supervise")
	}
}

func TestGPUOffReachesBothWhisperPlacements(t *testing.T) {
	pathWith(t, "whisper-server")
	cfg := whisperConfig(t, "whisper-base.en")
	cfg.Advanced.GPU = "off"

	cfg.Advanced.Placement = "subprocess"
	cli, err := Factory(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if !cli.(*WhisperCli).NoGPU {
		t.Error("whisper-cli NoGPU = false with gpu = \"off\", want true")
	}

	cfg.Advanced.Placement = "auto"
	srv, err := Factory(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if !srv.(*ServerTranscriber).Supervisor.cfg.NoGPU {
		t.Error("supervisor NoGPU = false with gpu = \"off\", want true")
	}
}

func TestGPUAutoLeavesGPUEnabled(t *testing.T) {
	cfg := whisperConfig(t, "whisper-base.en")
	cfg.Advanced.GPU = "auto"
	cfg.Advanced.Placement = "subprocess"

	transcriber, err := Factory(cfg, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if transcriber.(*WhisperCli).NoGPU {
		t.Error("NoGPU = true with gpu = \"auto\", want false")
	}
}

// A model named in the config is the model that runs, or the daemon does not
// start. It is never a quiet substitution.
func TestMissingWhisperModelIsFatalAndNamesTheFile(t *testing.T) {
	cfg := config.Default()
	cfg.Model = "whisper-base.en"
	cfg.Paths.Models = t.TempDir()

	_, err := Factory(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("Factory() = nil error for a model that is not on disk, want an error")
	}
	got := err.Error()
	if !strings.Contains(got, "whisper-base.en") {
		t.Errorf("error %q does not name the model", got)
	}
	if !strings.Contains(got, cfg.Paths.Models) {
		t.Errorf("error %q does not name the directory searched", got)
	}
	if !strings.Contains(got, "mavor models pull") {
		t.Errorf("error %q does not tell the user how to fix it", got)
	}
}

// A name the catalog does not carry is looked up as a directory under the
// sherpa model dir, and when that is absent too the error names the entries
// closest to what was written. A typo must not become a download of something
// else, and it must not become a bare "not found" either.
func TestModelOutsideTheCatalogAndOffDiskNamesTheNearestEntries(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := config.Default()
	cfg.Model = "moonshin-tiny"
	cfg.Paths.Models = t.TempDir()

	_, err := Factory(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("Factory() = nil error for a model that is in neither the catalog nor the cache")
	}
	got := err.Error()
	if !strings.Contains(got, "moonshin-tiny") {
		t.Errorf("error %q does not name what was written", got)
	}
	if !strings.Contains(got, "moonshine-tiny") {
		t.Errorf("error %q does not name the nearest catalog entry", got)
	}
}

// A catalog name that simply has not been downloaded gets the command that
// downloads it, not a list of names it might have meant.
func TestCatalogModelNotYetInstalledSaysToPullIt(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := config.Default()
	cfg.Model = "parakeet-tdt-0.6b"
	cfg.Paths.Models = t.TempDir()

	_, err := Factory(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("Factory() = nil error for a catalogued model that is not installed")
	}
	if got := err.Error(); !strings.Contains(got, "mavor models pull parakeet-tdt-0.6b") {
		t.Errorf("error = %q, want it to name the command that installs the model", got)
	}
}

// mavor is a cgo program and the in-process sherpa-onnx recognizers are always
// linked in, so the package-level builders the transcriber reaches for are set
// by the time any test runs. Before the build collapsed to cgo these were nil
// in the default build and a stub returned "not compiled in"; that variant no
// longer exists, and this test is what says so.
func TestSherpaRecognizersAreAlwaysLinkedIn(t *testing.T) {
	if DefaultOfflineRecognizerBuilder == nil {
		t.Error("DefaultOfflineRecognizerBuilder = nil, want the cgo builder")
	}
	if DefaultOnlineRecognizerBuilder == nil {
		t.Error("DefaultOnlineRecognizerBuilder = nil, want the cgo builder")
	}
}

// The failure a user can still hit is a model that is not there. It must be
// reported as a missing model, never as a missing build variant.
func TestSherpaMissingModelBlamesTheModel(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := config.Default()
	cfg.Model = "no-such-model"
	cfg.Paths.Models = t.TempDir()

	_, err := Factory(cfg, slog.Default())
	if err == nil {
		t.Fatal("Factory() = nil error for a sherpa model that is not on disk, want an error")
	}
	for _, banned := range []string{"not supported in this build", "not compiled in", "-tags sherpa"} {
		if strings.Contains(err.Error(), banned) {
			t.Errorf("error %q still describes the deleted pure-Go build (%q)", err, banned)
		}
	}
}

// ResolveSherpaModelDir carries the same instruction as every other
// model-not-found path in this package: an error that names a command is an
// instruction the user will follow, so it has to name a binary that exists.
func TestSherpaModelNotFoundErrorNamesTheRealBinary(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := config.Default()
	cfg.Model = "parakeet-tdt-0.6b"
	cfg.Paths.Models = t.TempDir()

	_, err := ResolveSherpaModelDir(cfg)
	if err == nil {
		t.Fatal("ResolveSherpaModelDir() = nil error for a model that is not on disk, want an error")
	}
	if got := err.Error(); !strings.Contains(got, "mavor models pull") {
		t.Errorf("error = %q, want it to tell the user to run `mavor models pull`", got)
	}
}

// The sherpa search order must only name directories this project actually
// writes to. `mavor models pull` puts models under the configured model dir
// (internal/config.DefaultModelDir), so a candidate path under any other
// project directory can never hit, and reports a path the user cannot act on.
func TestSherpaCandidatePathsAreAllMavorOwned(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	cfg := config.Default()
	cfg.Model = "parakeet-tdt-0.6b"
	cfg.Paths.Models = filepath.Join(t.TempDir(), "models")

	_, err := ResolveSherpaModelDir(cfg)
	if err == nil {
		t.Fatal("want an error listing the candidate paths")
	}
	for _, candidate := range strings.Fields(err.Error()) {
		if strings.HasPrefix(candidate, dataHome) && !strings.Contains(candidate, "/mavor/") {
			t.Errorf("candidate %q is under XDG_DATA_HOME but not under mavor/", candidate)
		}
	}
}

// sherpaTransducerDir writes the file layout of a transducer model — the
// three ONNX files and its tokens — which is enough to construct a
// transcriber. The model is opened by Start, not by Factory.
func sherpaTransducerDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stub"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
