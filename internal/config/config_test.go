package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsAreReasonable(t *testing.T) {
	// The cache and runtime defaults are XDG-derived, so they have to be pinned
	// or this asserts against whatever the developer's home happens to hold.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	d := Default()
	if d.TopMargin != 8 {
		t.Errorf("TopMargin = %d, want 8", d.TopMargin)
	}
	if d.Model != "base.en" {
		t.Errorf("Model = %q, want base.en", d.Model)
	}
	if !strings.HasSuffix(d.ModelDir, "/mavor/models") {
		t.Errorf("ModelDir = %q, want suffix /mavor/models", d.ModelDir)
	}
	if !strings.HasSuffix(d.Socket, "/mavor.sock") {
		t.Errorf("Socket = %q, want suffix /mavor.sock", d.Socket)
	}
	if d.Engine != "cli" {
		t.Errorf("Engine = %q, want cli", d.Engine)
	}
	if !strings.HasSuffix(d.ServerSocket, "/mavor-server.sock") {
		t.Errorf("ServerSocket = %q, want suffix /mavor-server.sock", d.ServerSocket)
	}
	if d.DuckAudio != false {
		t.Errorf("DuckAudio = %v, want false", d.DuckAudio)
	}
	// Recording should silence background media outright, not merely lower it;
	// a partial reduction is opt-in via duck_volume.
	if d.DuckVolume != "0%" {
		t.Errorf("DuckVolume = %q, want 0%% (mute)", d.DuckVolume)
	}
	if d.DuckSink != "" {
		t.Errorf("DuckSink = %q, want empty", d.DuckSink)
	}
	if len(d.DuckStreams) != 0 {
		t.Errorf("DuckStreams = %v, want empty/nil", d.DuckStreams)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.toml")
	got, err := Load(missing)
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if got.DuckSink != "" || len(got.DuckStreams) != 0 {
		t.Fatalf("unexpected duck config in defaults: %+v", got)
	}
	if got.TopMargin != Default().TopMargin || got.Model != Default().Model {
		t.Fatalf("got %+v, want %+v", got, Default())
	}
}

func TestLoadValidTOMLOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `top_margin = 64
model = "tiny.en"
model_dir = "/var/lib/mavor/models"
socket = "/run/user/1000/mavor.sock"
gpu_layers = 32
device = "vulkan"
threads = 8
engine = "server"
server_socket = "/run/user/1000/custom-server.sock"
duck_audio = true
duck_volume = "15%"
duck_sink = "alsa_output.pci-0000_00_1f.3.analog-stereo"
duck_streams = ["spotify", "firefox", "vlc"]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Mode:                 "batch",
		Preset:               "balanced",
		StreamingStrategy:    "auto",
		SilenceThresholdMS:   450,
		MinPhraseMS:          600,
		TopMargin:            64,
		Model:                "tiny.en",
		ModelDir:             "/var/lib/mavor/models",
		Socket:               "/run/user/1000/mavor.sock",
		GPULayers:            32,
		Device:               "vulkan",
		Threads:              8,
		Engine:               "server",
		ServerSocket:         "/run/user/1000/custom-server.sock",
		DuckAudio:            true,
		DuckVolume:           "15%",
		DuckSink:             "alsa_output.pci-0000_00_1f.3.analog-stereo",
		DuckStreams:          []string{"spotify", "firefox", "vlc"},
		SherpaModel:          "",
		SherpaModelType:      "auto",
		SherpaTokens:         "",
		SherpaEncoder:        "",
		SherpaDecoder:        "",
		SherpaJoiner:         "",
		SherpaProvider:       "cpu",
		SherpaHotwordsFile:   "",
		SherpaHotwordsScore:  1.5,
		SherpaDecodingMethod: "greedy_search",
	}
	if cfg.DuckSink != want.DuckSink {
		t.Errorf("DuckSink = %q, want %q", cfg.DuckSink, want.DuckSink)
	}
	if len(cfg.DuckStreams) != len(want.DuckStreams) {
		t.Fatalf("DuckStreams len = %d, want %d", len(cfg.DuckStreams), len(want.DuckStreams))
	}
	for i := range want.DuckStreams {
		if cfg.DuckStreams[i] != want.DuckStreams[i] {
			t.Errorf("DuckStreams[%d] = %q, want %q", i, cfg.DuckStreams[i], want.DuckStreams[i])
		}
	}
}

func TestPresetsAndSimpleConfig(t *testing.T) {
	t.Run("accurate preset selects large-v3-turbo", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		_ = os.WriteFile(path, []byte(`preset = "accurate"`), 0o644)
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Model != "large-v3-turbo" {
			t.Errorf("Model = %q, want large-v3-turbo", cfg.Model)
		}
	})

	t.Run("fast preset selects tiny.en", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		_ = os.WriteFile(path, []byte(`preset = "fast"`), 0o644)
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Model != "tiny.en" {
			t.Errorf("Model = %q, want tiny.en", cfg.Model)
		}
	})

	t.Run("streaming mode is parsed cleanly", func(t *testing.T) {
		tempDir := t.TempDir()
		path := filepath.Join(tempDir, "config.toml")
		_ = os.WriteFile(path, []byte("mode = \"streaming\"\n"), 0o644)
		cfg, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Mode != "streaming" {
			t.Errorf("Mode = %q, want streaming", cfg.Mode)
		}
		if cfg.Model != "base.en" {
			t.Errorf("Model = %q, want base.en", cfg.Model)
		}
	})
}

func TestLoadSherpaConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `engine = "sherpa"
sherpa_model = "parakeet-tdt-0.6b"
sherpa_model_type = "transducer"
sherpa_tokens = "~/models/tokens.txt"
sherpa_encoder = "~/models/encoder.onnx"
sherpa_decoder = "~/models/decoder.onnx"
sherpa_joiner = "~/models/joiner.onnx"
sherpa_provider = "cuda"
sherpa_hotwords_file = "~/models/hotwords.txt"
sherpa_hotwords_score = 2.5
sherpa_decoding_method = "modified_beam_search"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	home, _ := os.UserHomeDir()
	if cfg.Engine != "sherpa" {
		t.Errorf("Engine = %q, want sherpa", cfg.Engine)
	}
	if cfg.SherpaModel != "parakeet-tdt-0.6b" {
		t.Errorf("SherpaModel = %q, want parakeet-tdt-0.6b", cfg.SherpaModel)
	}
	if cfg.SherpaModelType != "transducer" {
		t.Errorf("SherpaModelType = %q, want transducer", cfg.SherpaModelType)
	}
	if cfg.SherpaTokens != filepath.Join(home, "models/tokens.txt") {
		t.Errorf("SherpaTokens = %q, want %q", cfg.SherpaTokens, filepath.Join(home, "models/tokens.txt"))
	}
	if cfg.SherpaEncoder != filepath.Join(home, "models/encoder.onnx") {
		t.Errorf("SherpaEncoder = %q, want %q", cfg.SherpaEncoder, filepath.Join(home, "models/encoder.onnx"))
	}
	if cfg.SherpaDecoder != filepath.Join(home, "models/decoder.onnx") {
		t.Errorf("SherpaDecoder = %q, want %q", cfg.SherpaDecoder, filepath.Join(home, "models/decoder.onnx"))
	}
	if cfg.SherpaJoiner != filepath.Join(home, "models/joiner.onnx") {
		t.Errorf("SherpaJoiner = %q, want %q", cfg.SherpaJoiner, filepath.Join(home, "models/joiner.onnx"))
	}
	if cfg.SherpaProvider != "cuda" {
		t.Errorf("SherpaProvider = %q, want cuda", cfg.SherpaProvider)
	}
	if cfg.SherpaHotwordsFile != filepath.Join(home, "models/hotwords.txt") {
		t.Errorf("SherpaHotwordsFile = %q, want %q", cfg.SherpaHotwordsFile, filepath.Join(home, "models/hotwords.txt"))
	}
	if cfg.SherpaHotwordsScore != 2.5 {
		t.Errorf("SherpaHotwordsScore = %f, want 2.5", cfg.SherpaHotwordsScore)
	}
	if cfg.SherpaDecodingMethod != "modified_beam_search" {
		t.Errorf("SherpaDecodingMethod = %q, want modified_beam_search", cfg.SherpaDecodingMethod)
	}
}

func TestLoadDuckConfig(t *testing.T) {
	cases := []struct {
		name        string
		toml        string
		wantAudio   bool
		wantVolume  string
		wantSink    string
		wantStreams []string
	}{
		{
			name: "custom sink only",
			toml: `duck_audio = true
duck_sink = "42"
`,
			wantAudio:   true,
			wantVolume:  "0%",
			wantSink:    "42",
			wantStreams: nil,
		},
		{
			name: "streams only",
			toml: `duck_audio = true
duck_streams = ["spotify", "zoom"]
`,
			wantAudio:   true,
			wantVolume:  "0%",
			wantSink:    "",
			wantStreams: []string{"spotify", "zoom"},
		},
		{
			name: "all duck options",
			toml: `duck_audio = true
duck_volume = "10%"
duck_sink = "alsa_output.usb-dac"
duck_streams = ["spotify", "firefox"]
`,
			wantAudio:   true,
			wantVolume:  "10%",
			wantSink:    "alsa_output.usb-dac",
			wantStreams: []string{"spotify", "firefox"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.toml), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.DuckAudio != tc.wantAudio {
				t.Errorf("DuckAudio = %v, want %v", cfg.DuckAudio, tc.wantAudio)
			}
			if cfg.DuckVolume != tc.wantVolume {
				t.Errorf("DuckVolume = %q, want %q", cfg.DuckVolume, tc.wantVolume)
			}
			if cfg.DuckSink != tc.wantSink {
				t.Errorf("DuckSink = %q, want %q", cfg.DuckSink, tc.wantSink)
			}
			if len(cfg.DuckStreams) != len(tc.wantStreams) {
				t.Fatalf("DuckStreams len = %d, want %d", len(cfg.DuckStreams), len(tc.wantStreams))
			}
			for i := range tc.wantStreams {
				if cfg.DuckStreams[i] != tc.wantStreams[i] {
					t.Errorf("DuckStreams[%d] = %q, want %q", i, cfg.DuckStreams[i], tc.wantStreams[i])
				}
			}
		})
	}
}

func TestLoadExpandsTildeAndEnv(t *testing.T) {
	t.Setenv("TEST_MAVOR_RUNTIME", "/tmp/test-run")
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `model_dir = "~/custom-models"
socket = "$TEST_MAVOR_RUNTIME/custom.sock"
server_socket = "$TEST_MAVOR_RUNTIME/custom-server.sock"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if cfg.ModelDir != filepath.Join(home, "custom-models") {
		t.Errorf("ModelDir = %q, want %q", cfg.ModelDir, filepath.Join(home, "custom-models"))
	}
	if cfg.Socket != "/tmp/test-run/custom.sock" {
		t.Errorf("Socket = %q, want /tmp/test-run/custom.sock", cfg.Socket)
	}
	if cfg.ServerSocket != "/tmp/test-run/custom-server.sock" {
		t.Errorf("ServerSocket = %q, want /tmp/test-run/custom-server.sock", cfg.ServerSocket)
	}
}

func TestLoadPartialTOMLPreservesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`top_margin = 16`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := Default()
	if cfg.TopMargin != 16 {
		t.Errorf("TopMargin = %d, want 16", cfg.TopMargin)
	}
	if cfg.Model != d.Model || cfg.ModelDir != d.ModelDir || cfg.Socket != d.Socket || cfg.Engine != d.Engine || cfg.ServerSocket != d.ServerSocket {
		t.Errorf("non-overridden fields changed: %+v vs default %+v", cfg, d)
	}
}

func TestLoadInvalidTOMLReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`top_margin = "not a number"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error on invalid TOML")
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

func TestDefaultLogFileHonorsXDGStateHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)

	want := filepath.Join(base, "mavor", "daemon.log")
	if got := Default().LogFile; got != want {
		t.Errorf("Default().LogFile = %q, want %q", got, want)
	}
}
