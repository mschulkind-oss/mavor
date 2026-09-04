// Package config loads the daemon's TOML configuration from
// $XDG_CONFIG_HOME/mavor/config.toml. A missing file is not an error — the
// daemon falls back to Default() so first-run users get sane behavior
// without having to write a config file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

type Config struct {
	// Mode controls the live preview while you speak. Text is inserted once
	// either way — when transcription completes — because partial results are
	// provisional and inserting them would type the same words twice.
	// - "streaming" (default): show partial results in the overlay preview.
	// - "batch": no live preview; the overlay shows only the recording state.
	Mode string `toml:"mode"`

	// Preset specifies the quality/speed balance: "balanced" (default), "accurate", or "fast".
	// - "balanced": Whisper Base (100% accuracy, ~1.2s) or Parakeet-TDT (80ms streaming).
	// - "accurate": Whisper Large-v3 Turbo (maximum vocabulary nuance).
	// - "fast": Whisper Tiny (ultra-light, sub-0.7s) or Moonshine.
	Preset string `toml:"preset"`

	// StreamingStrategy specifies the streaming implementation: "auto" (default), "vad_batch", or "transducer".
	// - "auto": Uses Parakeet transducer if Sherpa is present; otherwise VAD-segmented batch.
	// - "vad_batch": Slices phrases on natural speech pauses and fires warm batch inference.
	// - "transducer": Causal 80ms chunk streaming with Sherpa-ONNX.
	StreamingStrategy string `toml:"streaming_strategy"`

	// SilenceThresholdMS specifies the pause duration in ms before slicing audio in VAD-batch mode.
	// Defaults to 450 ms.
	SilenceThresholdMS int `toml:"silence_threshold_ms"`

	// MinPhraseMS specifies the minimum speech duration before slicing audio in VAD-batch mode.
	// Defaults to 600 ms.
	MinPhraseMS int `toml:"min_phrase_ms"`

	// TopMargin is the gap (px) between the overlay and the top of the
	// usable area — which is below Waybar, not the screen edge.
	//
	// The overlay is a layer-shell surface that never sets an exclusive zone,
	// so the compositor places it inside the space other surfaces have
	// reserved. Waybar's height is read from Waybar's own exclusive zone by
	// the compositor; mavor never learns it and does not need to. A bar of any
	// height, or no bar at all, is handled without changing this value.
	TopMargin int `toml:"top_margin"`

	// Model is the whisper model name without the "ggml-" prefix or ".bin"
	// suffix, e.g. "base.en" or "tiny.en".
	Model string `toml:"model"`

	// ModelDir is where downloaded model files live.
	ModelDir string `toml:"model_dir"`

	// Socket is the daemon's IPC socket path.
	Socket string `toml:"socket"`

	// GPULayers is the number of model layers to offload to GPU via Vulkan/ROCm (-ngl).
	// 0 means CPU-only.
	GPULayers int `toml:"gpu_layers"`

	// Device specifies the compute device: "auto", "vulkan", "rocm", or "cpu".
	Device string `toml:"device"`

	// Threads is the number of CPU threads to use for inference (-t). Defaults to 4.
	Threads int `toml:"threads"`

	// Engine specifies the speech-to-text inference engine: "cli", "server",
	// or "sherpa".
	// Defaults to "cli".
	Engine string `toml:"engine"`

	// ServerSocket is the Unix domain socket path or HTTP URL for the warm server.
	// Defaults to "$XDG_RUNTIME_DIR/mavor-server.sock".
	ServerSocket string `toml:"server_socket"`

	// DuckAudio enables automatic audio playback ducking during recording.
	// Defaults to false so tests and standard setups never alter host audio unexpectedly.
	DuckAudio bool `toml:"duck_audio"`

	// LogFile specifies the daemon log destination. Defaults to ~/.local/state/mavor/daemon.log.
	LogFile string `toml:"log_file"`

	// DuckVolume is the volume background media is set to while recording,
	// as a percentage ("0%", "25%") or a fraction ("0", "0.25").
	// Defaults to "0%" — muted. Set a higher value to merely lower the volume
	// instead of silencing it.
	DuckVolume string `toml:"duck_volume"`

	// DuckSink specifies the target audio sink name or ID instead of the default sink.
	// Defaults to "" (default sink).
	DuckSink string `toml:"duck_sink"`

	// DuckStreams lists application names to duck (e.g. ["spotify", "firefox", "vlc"]).
	// When non-empty, only sink-inputs matching these application/media names are ducked,
	// preserving other streams (e.g. Discord, Zoom, voice calls).
	DuckStreams []string `toml:"duck_streams"`

	// SherpaModel is the model identifier or subfolder name under ModelDir/sherpa.
	// Examples: "parakeet-tdt-0.6b", "canary-1b", "moonshine-tiny", "moonshine-base", "sensevoice", "zipformer", "mms".
	SherpaModel string `toml:"sherpa_model"`

	// SherpaModelType specifies the model architecture:
	// - "auto": Inferred automatically from model directory contents (default).
	// - "transducer": Parakeet-TDT, FastConformer, Zipformer Transducer.
	// - "moonshine": Useful Sensors Moonshine Tiny/Base (v1/v2).
	// - "sensevoice": Alibaba SenseVoice-Small multilingual.
	// - "paraformer": Alibaba FunASR Paraformer, Canary enc-dec.
	// - "zipformer_ctc": Offline Zipformer CTC.
	// - "nemo_ctc": NVIDIA NeMo CTC / Parakeet CTC.
	// - "whisper": Whisper ONNX models.
	SherpaModelType string `toml:"sherpa_model_type"`

	// SherpaTokens is the path to tokens.txt or BPE vocab file.
	SherpaTokens string `toml:"sherpa_tokens"`

	// SherpaEncoder is the path to the encoder ONNX model file.
	SherpaEncoder string `toml:"sherpa_encoder"`

	// SherpaDecoder is the path to the decoder ONNX model file.
	SherpaDecoder string `toml:"sherpa_decoder"`

	// SherpaJoiner is the path to the joiner ONNX model file (for transducer models).
	SherpaJoiner string `toml:"sherpa_joiner"`

	// SherpaProvider specifies the ONNX Runtime execution provider: "cpu",
	// "cuda", or "coreml". There is no Vulkan execution provider in ONNX
	// Runtime. Note that the ONNX Runtime bundled with the sherpa-onnx Go
	// binding is a CPU-only build with no provider libraries, so "cuda"
	// needs a runtime built against CUDA to have any effect —
	// `mavor doctor` reports which one you have. Defaults to "cpu".
	SherpaProvider string `toml:"sherpa_provider"`

	// SherpaHotwordsFile is an optional path to a hotwords file for shallow fusion boosting.
	SherpaHotwordsFile string `toml:"sherpa_hotwords_file"`

	// SherpaHotwordsScore specifies the hotword boost score bonus.
	// Defaults to 1.5 when hotwords are used.
	SherpaHotwordsScore float32 `toml:"sherpa_hotwords_score"`

	// SherpaDecodingMethod specifies the decoding algorithm: "greedy_search" (default) or "modified_beam_search".
	SherpaDecodingMethod string `toml:"sherpa_decoding_method"`
}

func Default() Config {
	return Config{
		Mode:                 "streaming",
		Preset:               "balanced",
		StreamingStrategy:    "auto",
		SilenceThresholdMS:   450,
		MinPhraseMS:          600,
		TopMargin:            8,
		Model:                "base.en",
		ModelDir:             defaultModelDir(),
		Socket:               defaultSocket(),
		GPULayers:            0,
		Device:               "auto",
		Threads:              4,
		Engine:               "cli",
		LogFile:              defaultLogFile(),
		ServerSocket:         defaultServerSocket(),
		DuckAudio:            false,
		DuckVolume:           "0%",
		DuckSink:             "",
		DuckStreams:          nil,
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
}

// Resolve applies smart defaults based on Mode and Preset.
func (c *Config) Resolve() {
	if c.Mode == "" {
		c.Mode = "streaming"
	}
	if c.Preset == "" {
		c.Preset = "balanced"
	}
	if c.StreamingStrategy == "" {
		c.StreamingStrategy = "auto"
	}
	if c.SilenceThresholdMS <= 0 {
		c.SilenceThresholdMS = 450
	}
	if c.MinPhraseMS <= 0 {
		c.MinPhraseMS = 600
	}

	// Apply Preset to Model if model was not explicitly overridden
	if c.Model == "" || c.Model == "base.en" {
		switch c.Preset {
		case "accurate":
			c.Model = "large-v3-turbo"
		case "fast":
			c.Model = "tiny.en"
		case "balanced":
			c.Model = "base.en"
		}
	}
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

// Load reads from path. If path is empty, Path() is used. A missing file is
// treated as "no overrides" and Default() is returned without error.
func Load(path string) (Config, error) {
	if path == "" {
		path = Path()
	}
	cfg := Default()
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.Resolve()
			return cfg, nil
		}
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := toml.Unmarshal(body, &cfg); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.ModelDir = ExpandPath(cfg.ModelDir)
	cfg.Socket = ExpandPath(cfg.Socket)
	cfg.ServerSocket = ExpandPath(cfg.ServerSocket)
	cfg.SherpaTokens = ExpandPath(cfg.SherpaTokens)
	cfg.SherpaEncoder = ExpandPath(cfg.SherpaEncoder)
	cfg.SherpaDecoder = ExpandPath(cfg.SherpaDecoder)
	cfg.SherpaJoiner = ExpandPath(cfg.SherpaJoiner)
	cfg.SherpaHotwordsFile = ExpandPath(cfg.SherpaHotwordsFile)
	cfg.Resolve()
	return cfg, nil
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

// DefaultServerSocket returns the default warm server socket path ($XDG_RUNTIME_DIR/mavor-server.sock).
func DefaultServerSocket() string {
	return defaultServerSocket()
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

func defaultServerSocket() string {
	return filepath.Join(xdgRuntimeDir(), "mavor-server.sock")
}

func defaultLogFile() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(homeDir(), ".local", "state")
	}
	return filepath.Join(stateHome, "mavor", "daemon.log")
}
