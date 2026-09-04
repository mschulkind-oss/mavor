package main

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// KnownModel describes one downloadable speech model: where it comes from,
// how to unpack it, and the properties a user picks between when choosing one.
type KnownModel struct {
	Name        string   // canonical name, as typed to `mavor models pull`
	Aliases     []string // alternate names accepted for the same download
	Engine      string   // "whisper" or "sherpa"
	Family      string   // "Whisper", "NeMo", "Moonshine", "SenseVoice", "Zipformer"
	Description string
	URL         string
	Format      string // "raw", "tar.bz2", "tar.gz", "tgz", "tar"
	TargetDir   string // subfolder under model_dir/sherpa/

	// DownloadSize is the artifact size in bytes as served by the URL above,
	// measured rather than estimated. It is the download cost, not the size
	// in memory: the sherpa archives expand to roughly twice this.
	DownloadSize int64

	// Languages is what the model transcribes: "en", "multi (99)", or an
	// explicit list for the small multilingual models.
	Languages string

	// Streaming reports whether the model decodes incrementally as audio
	// arrives. Non-streaming models transcribe once the recording stops.
	Streaming bool

	// Transducer reports an RNN-T / TDT architecture. It decides whether the
	// model can take a hotwords file: sherpa-onnx implements contextual
	// biasing by boosting paths during transducer beam search, so the CTC and
	// encoder-decoder models cannot use one however they are configured.
	Transducer bool

	// Speed is a relative tier — "very fast" through "very slow" — ordering
	// the catalog by how long a transcription takes. It is a rough ordering
	// from architecture and parameter count, NOT a measurement. Where a real
	// benchmark exists, MeasuredRTF carries it.
	Speed string

	// MeasuredRTF is the real-time factor from docs/reports/, or 0 when the
	// model has not been benchmarked. Below 1.0 is faster than real time.
	// Measured with whisper-cli at 4 threads on a 12-core x86_64 CPU against
	// 20 s of speech, so treat it as one machine's number, not a spec.
	MeasuredRTF float64

	// Vocabulary describes what vocabulary biasing this model supports
	// through mavor, phrased for the listing.
	Vocabulary string
}

// modelCatalog is the set of distinct models mavor can download — one entry per
// artifact. Alternate names for the same download are Aliases, not entries, so
// the listing shows what a user is actually choosing between.
//
// Sizes were measured against the live URLs. Whisper artifacts come from the
// whisper.cpp GGML repository; sherpa artifacts from the sherpa-onnx release
// assets, which are pre-converted to ONNX and mostly INT8-quantized.
var modelCatalog = []KnownModel{
	// ---- OpenAI Whisper (GGML / whisper.cpp) -------------------------------
	// Whisper is encoder-decoder and transcribes in 30-second windows, so
	// none of these decode incrementally.
	{
		Name: "tiny", Aliases: []string{"whisper-tiny"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Tiny, 39M parameters — fastest, least accurate",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin",
		Format:       "raw",
		DownloadSize: 77691713,
		Languages:    "multi (99)",
		Speed:        "very fast",
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},
	{
		Name: "tiny.en", Aliases: []string{"whisper-tiny.en"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Tiny, 39M parameters, English-only — what the test suite uses",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.en.bin",
		Format:       "raw",
		DownloadSize: 77704715,
		Languages:    "en",
		Speed:        "very fast",
		MeasuredRTF:  0.061,
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},
	{
		Name: "base", Aliases: []string{"whisper-base"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Base, 74M parameters",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin",
		Format:       "raw",
		DownloadSize: 147951465,
		Languages:    "multi (99)",
		Speed:        "fast",
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},
	{
		Name: "base.en", Aliases: []string{"whisper-base.en"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Base, 74M parameters, English-only — the default",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin",
		Format:       "raw",
		DownloadSize: 147964211,
		Languages:    "en",
		Speed:        "fast",
		MeasuredRTF:  0.136,
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},
	{
		Name: "small", Aliases: []string{"whisper-small"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Small, 244M parameters",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin",
		Format:       "raw",
		DownloadSize: 487601967,
		Languages:    "multi (99)",
		Speed:        "moderate",
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},
	{
		Name: "small.en", Aliases: []string{"whisper-small.en"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Small, 244M parameters, English-only",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.en.bin",
		Format:       "raw",
		DownloadSize: 487614201,
		Languages:    "en",
		Speed:        "moderate",
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},
	{
		Name: "medium", Aliases: []string{"whisper-medium"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Medium, 769M parameters",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin",
		Format:       "raw",
		DownloadSize: 1533763059,
		Languages:    "multi (99)",
		Speed:        "slow",
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},
	{
		Name: "medium.en", Aliases: []string{"whisper-medium.en"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Medium, 769M parameters, English-only",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.en.bin",
		Format:       "raw",
		DownloadSize: 1533774781,
		Languages:    "en",
		Speed:        "slow",
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},
	{
		Name: "large-v3", Aliases: []string{"whisper-large-v3"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Large v3, 1.55B parameters — most accurate, slowest",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3.bin",
		Format:       "raw",
		DownloadSize: 3095033483,
		Languages:    "multi (99)",
		Speed:        "very slow",
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},
	{
		Name: "large-v3-turbo", Aliases: []string{"whisper-large-v3-turbo"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Large v3 Turbo, 809M parameters — large-v3 accuracy at a fraction of the decode cost",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin",
		Format:       "raw",
		DownloadSize: 1624555275,
		Languages:    "multi (99)",
		Speed:        "slow",
		MeasuredRTF:  1.519,
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},
	{
		Name: "distil-large-v3", Aliases: []string{"distil-whisper-large-v3"},
		Engine: "whisper", Family: "Whisper",
		Description:  "Distil-Whisper Large v3, 756M parameters, English-only",
		URL:          "https://huggingface.co/distil-whisper/distil-large-v3-ggml/resolve/main/ggml-distil-large-v3.bin",
		Format:       "raw",
		DownloadSize: 1519521155,
		Languages:    "en",
		Speed:        "slow",
		Vocabulary:   "none — mavor does not pass an initial prompt to whisper-cli",
	},

	// ---- NVIDIA NeMo (sherpa-onnx) -----------------------------------------
	{
		Name: "parakeet", Aliases: []string{"parakeet-tdt"},
		Engine: "sherpa", Family: "NeMo",
		Description:  "NeMo FastConformer transducer, 80ms chunk — decodes while you speak",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-streaming-fast-conformer-transducer-en-80ms.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 450212918,
		Languages:    "en",
		Transducer:   true,
		Speed:        "fast",
		Vocabulary:   "hotwords via sherpa_hotwords_file",
		Streaming:    true,
	},
	{
		Name:   "parakeet-tdt-0.6b",
		Engine: "sherpa", Family: "NeMo",
		Description:  "NeMo Parakeet TDT 0.6B v3, INT8 — 25 European languages",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 487170055,
		Languages:    "multi (25)",
		Transducer:   true,
		Speed:        "moderate",
		Vocabulary:   "hotwords via sherpa_hotwords_file",
	},
	{
		// Named for the artifact it actually downloads. The former name,
		// parakeet-tdt-1.1b, described a 1.1B model but fetched this 0.6B
		// one; it is kept as an alias so existing configs keep resolving.
		Name: "parakeet-unified-en", Aliases: []string{"parakeet-tdt-1.1b"},
		Engine: "sherpa", Family: "NeMo",
		Description:  "NeMo Parakeet Unified 0.6B English, INT8, non-streaming",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-unified-en-0.6b-int8-non-streaming.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 501350460,
		Languages:    "en",
		Transducer:   true,
		Speed:        "moderate",
		Vocabulary:   "hotwords via sherpa_hotwords_file",
	},
	{
		Name:   "parakeet-ctc",
		Engine: "sherpa", Family: "NeMo",
		Description:  "NeMo Conformer CTC English Large",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-ctc-en-conformer-large.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 610719312,
		Languages:    "en",
		Speed:        "moderate",
		Vocabulary:   "none — sherpa-onnx biasing needs a transducer",
	},
	{
		Name: "canary-1b", Aliases: []string{"canary"},
		Engine: "sherpa", Family: "NeMo",
		Description:  "NeMo Canary 1B v2, INT8 — transcribes and translates",
		URL:          "https://huggingface.co/Sarphix/canary-1b-v2-sherpa-onnx-int8/resolve/main/sherpa-onnx-nemo-canary-1b-v2-int8.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 1144946025,
		Languages:    "multi (25)",
		Speed:        "slow",
		Vocabulary:   "none — sherpa-onnx biasing needs a transducer",
	},
	{
		Name:   "canary-180m",
		Engine: "sherpa", Family: "NeMo",
		Description:  "NeMo Canary 180M Flash, INT8",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-canary-180m-flash-en-es-de-fr-int8.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 153692328,
		Languages:    "en, es, de, fr",
		Speed:        "moderate",
		Vocabulary:   "none — sherpa-onnx biasing needs a transducer",
	},

	// ---- Useful Sensors Moonshine (sherpa-onnx) ----------------------------
	{
		Name: "moonshine-tiny", Aliases: []string{"moonshine"},
		Engine: "sherpa", Family: "Moonshine",
		Description:  "Moonshine Tiny, 27M parameters, INT8 — built for short utterances",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-moonshine-tiny-en-int8.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 107600538,
		Languages:    "en",
		Speed:        "very fast",
		Vocabulary:   "none — sherpa-onnx biasing needs a transducer",
	},
	{
		Name:   "moonshine-base",
		Engine: "sherpa", Family: "Moonshine",
		Description:  "Moonshine Base, 62M parameters, INT8",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-moonshine-base-en-int8.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 250807309,
		Languages:    "en",
		Speed:        "fast",
		Vocabulary:   "none — sherpa-onnx biasing needs a transducer",
	},

	// ---- FunASR (sherpa-onnx) ----------------------------------------------
	{
		Name: "sensevoice-small", Aliases: []string{"sensevoice"},
		Engine: "sherpa", Family: "SenseVoice",
		Description:  "SenseVoice Small — five languages in one model",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 1047870769,
		Languages:    "zh, en, ja, ko, yue",
		Speed:        "moderate",
		Vocabulary:   "none — sherpa-onnx biasing needs a transducer",
	},
	{
		Name:   "paraformer",
		Engine: "sherpa", Family: "Paraformer",
		Description:  "Paraformer Chinese, non-streaming",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-paraformer-zh-2024-03-09.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 996591364,
		Languages:    "zh",
		Speed:        "moderate",
		Vocabulary:   "none — sherpa-onnx biasing needs a transducer",
	},

	// ---- Zipformer (sherpa-onnx) -------------------------------------------
	{
		Name: "zipformer-streaming", Aliases: []string{"zipformer"},
		Engine: "sherpa", Family: "Zipformer",
		Description:  "Streaming Zipformer transducer — decodes while you speak",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-en-2023-06-26.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 310414022,
		Languages:    "en",
		Transducer:   true,
		Speed:        "fast",
		Vocabulary:   "hotwords via sherpa_hotwords_file",
		Streaming:    true,
	},
	{
		Name:   "zipformer-offline",
		Engine: "sherpa", Family: "Zipformer",
		Description:  "Zipformer transducer, non-streaming",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-zipformer-en-2023-06-26.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 307666046,
		Languages:    "en",
		Transducer:   true,
		Speed:        "fast",
		Vocabulary:   "hotwords via sherpa_hotwords_file",
	},
	{
		Name:   "zipformer-ctc",
		Engine: "sherpa", Family: "Zipformer",
		Description:  "Zipformer CTC, non-streaming",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-zipformer-ctc-en-2023-10-02.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 383165059,
		Languages:    "en",
		Speed:        "fast",
		Vocabulary:   "none — sherpa-onnx biasing needs a transducer",
	},
}

// knownModels resolves every accepted name — canonical or alias — to its
// download. A sherpa model unpacks into a directory named after the name the
// user typed, because that is the name their config.toml will carry and what
// speech.ResolveSherpaModelDir looks for.
var knownModels = buildKnownModels()

func buildKnownModels() map[string]KnownModel {
	m := make(map[string]KnownModel, len(modelCatalog)*2)
	for _, entry := range modelCatalog {
		for _, key := range append([]string{entry.Name}, entry.Aliases...) {
			spec := entry
			if spec.Engine == "sherpa" {
				spec.TargetDir = key
			}
			m[key] = spec
		}
	}
	return m
}

func runModels(args []string) error {
	if len(args) == 0 {
		return runModelsList(false, false, false)
	}
	switch args[0] {
	case "pull":
		if len(args) < 2 {
			return fmt.Errorf("usage: mavor models pull <name>\n\n%s", catalogSummary())
		}
		return pullModel(args[1])
	case "list", "ls":
		installedOnly, verbose, asJSON := false, false, false
		for _, a := range args[1:] {
			switch a {
			case "--installed", "-i":
				installedOnly = true
			case "--verbose", "-v":
				verbose = true
			case "--json":
				asJSON = true
			default:
				return fmt.Errorf("unknown flag for 'mavor models list': %s", a)
			}
		}
		if asJSON && verbose {
			return errors.New("--json and --verbose are different renderings of the same data; pick one")
		}
		return runModelsList(installedOnly, verbose, asJSON)
	case "help", "-h", "--help":
		fmt.Printf(`usage: mavor models <command>

commands:
  list, ls            list every model mavor can download, with sizes, languages,
                      and which of them are already in the cache
      --installed,-i  restrict the listing to models already downloaded
      --verbose,-v    one block per model: speed, vocabulary biasing, GPU
      --json          the same catalog as JSON, for scripts and benchmarks
  pull <name>         download a model into the cache

%s
Examples:
  mavor models list
  mavor models list --installed
  mavor models pull base.en
  mavor models pull parakeet
`, catalogSummary())
		return nil
	default:
		return fmt.Errorf("unknown models command: %s (try 'mavor models help')", args[0])
	}
}

// catalogSummary lists the downloadable names grouped by family. It is
// generated from modelCatalog so it cannot drift from what pull accepts.
func catalogSummary() string {
	var families []string
	byFamily := map[string][]string{}
	for _, m := range modelCatalog {
		if _, seen := byFamily[m.Family]; !seen {
			families = append(families, m.Family)
		}
		byFamily[m.Family] = append(byFamily[m.Family], m.Name)
	}

	width := 0
	for _, f := range families {
		width = max(width, len(f)+1)
	}

	var b strings.Builder
	b.WriteString("Available models (`mavor models list` for sizes and languages):\n")
	for _, f := range families {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, f+":", strings.Join(byFamily[f], ", "))
	}
	return b.String()
}

func pullModel(name string) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ModelDir, 0o755); err != nil {
		return err
	}

	cleanName := strings.TrimPrefix(name, "sherpa/")

	if spec, ok := knownModels[cleanName]; ok {
		if spec.Engine == "sherpa" {
			targetDir := filepath.Join(cfg.ModelDir, "sherpa", spec.TargetDir)
			if spec.Format == "raw" {
				if err := os.MkdirAll(targetDir, 0o755); err != nil {
					return fmt.Errorf("create dir %s: %w", targetDir, err)
				}
				fileName := filepath.Base(spec.URL)
				dest := filepath.Join(targetDir, fileName)
				if _, err := os.Stat(dest); err == nil {
					fmt.Printf("already present: %s\n", dest)
					return nil
				}
				fmt.Printf("downloading %s (%s)\nURL: %s\n", cleanName, spec.Description, spec.URL)
				return downloadFile(spec.URL, dest)
			}

			// Archive format (tar.bz2, tar.gz, tgz, tar)
			if fi, err := os.Stat(targetDir); err == nil && fi.IsDir() {
				entries, _ := os.ReadDir(targetDir)
				if len(entries) > 0 {
					fmt.Printf("already present: %s\n", targetDir)
					return nil
				}
			}
			fmt.Printf("downloading %s (%s)\nURL: %s\n", cleanName, spec.Description, spec.URL)
			return downloadAndExtractArchive(spec.URL, spec.Format, targetDir)
		}

		// Whisper GGML model
		whisperModel := cleanWhisperName(cleanName)
		dest := filepath.Join(cfg.ModelDir, "ggml-"+whisperModel+".bin")
		if _, err := os.Stat(dest); err == nil {
			fmt.Printf("already present: %s\n", dest)
			return nil
		}
		fmt.Printf("downloading %s (%s)\nURL: %s\n", cleanName, spec.Description, spec.URL)
		return downloadFile(spec.URL, dest)
	}

	// Fallback: assume Whisper GGML model on Hugging Face
	whisperModel := cleanWhisperName(name)
	dest := filepath.Join(cfg.ModelDir, "ggml-"+whisperModel+".bin")
	if _, err := os.Stat(dest); err == nil {
		fmt.Printf("already present: %s\n", dest)
		return nil
	}
	url := "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-" + whisperModel + ".bin"
	fmt.Printf("downloading %s\nURL: %s\n", name, url)
	return downloadFile(url, dest)
}

func cleanWhisperName(name string) string {
	name = strings.TrimPrefix(name, "whisper-")
	name = strings.TrimPrefix(name, "whisper_")
	if name == "distil-whisper-large-v3" || name == "distil-whisper-large-v3.bin" {
		return "distil-large-v3"
	}
	name = strings.TrimPrefix(name, "ggml-")
	name = strings.TrimSuffix(name, ".bin")
	return name
}

func downloadAndExtractArchive(url, format, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", targetDir, err)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "mavor/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}

	var decompressed io.Reader
	switch format {
	case "tar.bz2", "bz2":
		decompressed = bzip2.NewReader(resp.Body)
	case "tar.gz", "tgz", "gz":
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return err
		}
		defer gz.Close()
		decompressed = gz
	case "tar":
		decompressed = resp.Body
	default:
		return fmt.Errorf("unsupported archive format: %s", format)
	}

	tarReader := tar.NewReader(decompressed)
	count := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		cleanPath := filepath.Clean(header.Name)
		if cleanPath == "." || cleanPath == "/" {
			continue
		}

		// Strip top-level archive directory wrapper if present
		parts := strings.Split(filepath.ToSlash(cleanPath), "/")
		var relPath string
		if len(parts) > 1 {
			relPath = filepath.Join(parts[1:]...)
		} else {
			relPath = parts[0]
		}

		if relPath == "" || relPath == "." {
			continue
		}

		destPath := filepath.Join(targetDir, relPath)

		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return fmt.Errorf("create directory %s: %w", destPath, err)
			}
			continue
		}

		if header.Typeflag == tar.TypeReg {
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				return fmt.Errorf("create dir %s: %w", filepath.Dir(destPath), err)
			}
			outFile, err := os.Create(destPath)
			if err != nil {
				return fmt.Errorf("create %s: %w", destPath, err)
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return fmt.Errorf("extract %s: %w", destPath, err)
			}
			outFile.Close()
			count++
		}
	}

	if count == 0 {
		return fmt.Errorf("archive from %s contained no regular files", url)
	}

	fmt.Printf("✅ Successfully extracted %d model files to %s\n", count, targetDir)
	return nil
}

// Markers in the STATUS column.
const (
	markerActive     = "\u2605" // the model the daemon will actually load
	markerDownloaded = "\u2713" // present in the model cache
	markerAbsent     = "\u2013" // not downloaded
)

func runModelsList(installedOnly, verbose, asJSON bool) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	if asJSON {
		return listCatalogJSON(os.Stdout, cfg, installedOnly)
	}
	if verbose {
		return listCatalogVerbose(os.Stdout, cfg, installedOnly)
	}
	return listCatalog(os.Stdout, cfg, installedOnly)
}

// installedModel is what the cache holds for one name.
type installedModel struct {
	size int64
}

// listCatalog prints every model mavor can download, with what is on disk
// marked. With installedOnly, it prints just the downloaded ones.
func listCatalog(w io.Writer, cfg config.Config, installedOnly bool) error {
	installed := scanInstalled(cfg)
	active := activeModelName(cfg)

	type row struct {
		name, engine, size, langs, stream, status, aliases string
	}
	var rows []row

	for _, m := range modelCatalog {
		// A model counts as downloaded under any of its names, since that is
		// the directory `mavor models pull` would have created.
		var got *installedModel
		for _, key := range append([]string{m.Name}, m.Aliases...) {
			if inst, ok := installed[key]; ok {
				got = &inst
				break
			}
		}
		if installedOnly && got == nil {
			continue
		}

		status := markerAbsent
		if got != nil {
			status = markerDownloaded + " " + formatFileSize(got.size)
		}
		if active != "" && (m.Name == active || slices.Contains(m.Aliases, active)) {
			status += "  " + markerActive
		}

		stream := "no"
		if m.Streaming {
			stream = "yes"
		}
		rows = append(rows, row{
			name:    m.Name,
			engine:  m.Engine,
			size:    formatFileSize(m.DownloadSize),
			langs:   m.Languages,
			stream:  stream,
			status:  status,
			aliases: strings.Join(m.Aliases, ", "),
		})
	}

	fmt.Fprintf(w, "Model cache: %s\n\n", cfg.ModelDir)

	if len(rows) == 0 {
		fmt.Fprintln(w, "No models downloaded yet. Get one with:")
		fmt.Fprintln(w, "    mavor models pull base.en        # 141 MB, English, the default")
		fmt.Fprintln(w, "    mavor models pull tiny.en        #  74 MB, English, fastest")
		fmt.Fprintln(w, "\nRun `mavor models list` to see everything available.")
		return nil
	}

	// Column widths sized to content so the table stays readable as the
	// catalog grows.
	wName, wEngine, wSize, wLangs, wStream, wStatus := len("NAME"), len("ENGINE"), len("SIZE"), len("LANGUAGES"), len("STREAM"), len("STATUS")
	for _, r := range rows {
		wName = max(wName, len(r.name))
		wEngine = max(wEngine, len(r.engine))
		wSize = max(wSize, len(r.size))
		wLangs = max(wLangs, len(r.langs))
		wStream = max(wStream, len(r.stream))
		wStatus = max(wStatus, runeLen(r.status))
	}

	line := func(name, engine, size, langs, stream, status, aliases string) {
		fmt.Fprintln(w, strings.TrimRight(strings.Join([]string{
			padRight(name, wName), padRight(engine, wEngine), padLeft(size, wSize),
			padRight(langs, wLangs), padRight(stream, wStream),
			padRight(status, wStatus), aliases,
		}, "  "), " "))
	}

	line("NAME", "ENGINE", "SIZE", "LANGUAGES", "STREAM", "STATUS", "ALIASES")
	for _, r := range rows {
		line(r.name, r.engine, r.size, r.langs, r.stream, r.status, r.aliases)
	}

	fmt.Fprintf(w, "\n%s active   %s downloaded   %s not downloaded\n", markerActive, markerDownloaded, markerAbsent)
	if !installedOnly {
		fmt.Fprintln(w, "SIZE is the download; sherpa archives expand to roughly twice that on disk.")
		fmt.Fprintln(w, "Download one with `mavor models pull <name>`.")
	}

	// Anything in the cache that the catalog does not know about — a
	// hand-placed or hand-converted model — still belongs in the listing.
	if extras := unknownInstalled(installed); len(extras) > 0 {
		fmt.Fprintln(w, "\nAlso in the cache, not from the catalog:")
		for _, name := range extras {
			fmt.Fprintf(w, "  %-24s %-9s  %s\n", name,
				formatFileSize(installed[name].size),
				describeSherpaModel(name, filepath.Join(cfg.ModelDir, "sherpa", name)))
		}
	}
	return nil
}

// listCatalogVerbose prints one block per model with every property the
// catalog carries. The table view has room for what you scan; this has room
// for the caveats — which biasing a model can take, what GPU support depends
// on, and whether a speed figure was measured or estimated.
func listCatalogVerbose(w io.Writer, cfg config.Config, installedOnly bool) error {
	installed := scanInstalled(cfg)
	active := activeModelName(cfg)

	fmt.Fprintf(w, "Model cache: %s\n", cfg.ModelDir)

	shown := 0
	for _, m := range modelCatalog {
		var got *installedModel
		for _, key := range append([]string{m.Name}, m.Aliases...) {
			if inst, ok := installed[key]; ok {
				got = &inst
				break
			}
		}
		if installedOnly && got == nil {
			continue
		}
		shown++

		state := markerAbsent + " not downloaded"
		if got != nil {
			state = fmt.Sprintf("%s downloaded (%s)", markerDownloaded, formatFileSize(got.size))
		}
		if active != "" && (m.Name == active || slices.Contains(m.Aliases, active)) {
			state += "   " + markerActive + " active"
		}

		fmt.Fprintf(w, "\n%s\n", m.Name)
		fmt.Fprintf(w, "  %s\n", m.Description)
		field(w, "engine", engineDetail(m.Engine))
		field(w, "download", formatFileSize(m.DownloadSize))
		field(w, "languages", m.Languages)
		field(w, "streaming", streamingDetail(m.Streaming))
		field(w, "speed", speedDetail(m))
		field(w, "vocabulary", m.Vocabulary)
		field(w, "gpu", gpuDetail(m.Engine))
		if len(m.Aliases) > 0 {
			field(w, "aliases", strings.Join(m.Aliases, ", "))
		}
		field(w, "status", state)
		field(w, "source", m.URL)
	}

	if shown == 0 {
		fmt.Fprintln(w, "\nNo models downloaded yet. Get one with `mavor models pull base.en`.")
		return nil
	}

	fmt.Fprint(w, "\n"+verboseFootnotes)
	return nil
}

// catalogJSON is the machine-readable form of the listing: the same rows the
// table renders, in the shape a benchmark harness or a script wants. It is a
// single object rather than JSON Lines because a consumer almost always wants
// the whole catalog at once, and because ModelDir belongs to the listing as a
// whole and not to any row in it.
type catalogJSON struct {
	ModelDir string             `json:"model_dir"`
	Models   []catalogModelJSON `json:"models"`
}

// catalogModelJSON is one model. Every field the catalog carries is here,
// including the ones only --verbose renders, so a consumer never has to parse
// the human tables to recover a property.
type catalogModelJSON struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	Engine      string   `json:"engine"`
	Family      string   `json:"family"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	DownloadS   int64    `json:"download_size"`
	Languages   string   `json:"languages"`
	Streaming   bool     `json:"streaming"`
	Transducer  bool     `json:"transducer"`
	Vocabulary  string   `json:"vocabulary"`

	// Speed is the relative tier and MeasuredRTF the benchmark, kept as
	// separate fields so a consumer cannot mistake one for the other. The
	// estimated flag says outright which it is looking at: a tier is an
	// architectural guess, and reporting it as a measurement is exactly the
	// error this output exists to prevent.
	Speed       string  `json:"speed"`
	MeasuredRTF float64 `json:"measured_rtf,omitempty"`
	SpeedIsEst  bool    `json:"speed_is_estimated"`

	// Installed reports the cache, not the catalog: whether this model is on
	// disk under any of its names, how big it is there, and whether it is the
	// one the daemon would load right now.
	Installed     bool  `json:"installed"`
	InstalledSize int64 `json:"installed_size,omitempty"`
	Active        bool  `json:"active"`
}

// listCatalogJSON writes the catalog as JSON. It shares scanInstalled and
// activeModelName with the table renderers so the three views cannot disagree
// about what is on disk.
func listCatalogJSON(w io.Writer, cfg config.Config, installedOnly bool) error {
	installed := scanInstalled(cfg)
	active := activeModelName(cfg)

	out := catalogJSON{ModelDir: cfg.ModelDir, Models: []catalogModelJSON{}}
	for _, m := range modelCatalog {
		var got *installedModel
		for _, key := range append([]string{m.Name}, m.Aliases...) {
			if inst, ok := installed[key]; ok {
				got = &inst
				break
			}
		}
		if installedOnly && got == nil {
			continue
		}

		row := catalogModelJSON{
			Name:        m.Name,
			Aliases:     m.Aliases,
			Engine:      m.Engine,
			Family:      m.Family,
			Description: m.Description,
			URL:         m.URL,
			DownloadS:   m.DownloadSize,
			Languages:   m.Languages,
			Streaming:   m.Streaming,
			Transducer:  m.Transducer,
			Vocabulary:  m.Vocabulary,
			Speed:       m.Speed,
			MeasuredRTF: m.MeasuredRTF,
			SpeedIsEst:  m.MeasuredRTF == 0,
			Installed:   got != nil,
			Active:      active != "" && (m.Name == active || slices.Contains(m.Aliases, active)),
		}
		if row.Aliases == nil {
			row.Aliases = []string{}
		}
		if got != nil {
			row.InstalledSize = got.size
		}
		out.Models = append(out.Models, row)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// verboseFootnotes carries the caveats the per-model fields cannot: what the
// speed tier is and is not, and that GPU support is a property of the build.
const verboseFootnotes = `speed is a relative tier across this catalog, estimated from architecture and
parameter count — not a measurement. The few figures marked "measured" come
from docs/reports/: whisper-cli at 4 threads on a 12-core x86_64 CPU over 20s
of speech. Your numbers will differ; an RTF below 1.0 is faster than real time.

gpu support depends on the build you are running, not on the model. Run
` + "`mavor doctor`" + ` to see what yours can actually use.
`

func field(w io.Writer, name, value string) {
	fmt.Fprintf(w, "  %-11s %s\n", name, value)
}

func engineDetail(engine string) string {
	if engine == "sherpa" {
		return "sherpa (in-process sherpa-onnx, CGO)"
	}
	return "whisper (whisper-cli subprocess or whisper-server)"
}

func streamingDetail(streaming bool) string {
	if streaming {
		return "yes — decodes incrementally while you speak"
	}
	return "no — transcribes once you stop speaking"
}

// speedDetail prefers a measured figure and says so, rather than letting an
// estimated tier read like a benchmark.
func speedDetail(m KnownModel) string {
	if m.MeasuredRTF > 0 {
		return fmt.Sprintf("%s · measured RTF %.3f (%.1fx real time)",
			m.Speed, m.MeasuredRTF, 1/m.MeasuredRTF)
	}
	return m.Speed + " (relative tier, not measured)"
}

func gpuDetail(engine string) string {
	if engine == "sherpa" {
		return "none in practice — the bundled ONNX Runtime is a CPU-only build"
	}
	return "offload via gpu_layers, if your whisper.cpp has a GPU backend"
}

// The status markers are multi-byte, so columns are padded by rune count.
// fmt's %-*s pads by bytes and would misalign every row containing one.
func runeLen(s string) int { return len([]rune(s)) }

func padRight(s string, w int) string {
	if n := w - runeLen(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func padLeft(s string, w int) string {
	if n := w - runeLen(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// scanInstalled reports what the model cache actually holds, keyed by the
// name a user would type: whisper models by their ggml-<name>.bin stem, sherpa
// models by their directory name.
func scanInstalled(cfg config.Config) map[string]installedModel {
	found := map[string]installedModel{}

	entries, err := os.ReadDir(cfg.ModelDir)
	if err != nil {
		return found
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasPrefix(name, "ggml-") && strings.HasSuffix(name, ".bin") {
			info, err := e.Info()
			if err != nil {
				continue
			}
			stem := strings.TrimSuffix(strings.TrimPrefix(name, "ggml-"), ".bin")
			found[stem] = installedModel{size: info.Size()}
			continue
		}
		if e.IsDir() && name != "sherpa" {
			dir := filepath.Join(cfg.ModelDir, name)
			if containsModelFiles(dir) {
				found[name] = installedModel{size: dirSize(dir)}
			}
		}
	}

	sherpaBase := filepath.Join(cfg.ModelDir, "sherpa")
	if subs, err := os.ReadDir(sherpaBase); err == nil {
		for _, se := range subs {
			if !se.IsDir() {
				continue
			}
			dir := filepath.Join(sherpaBase, se.Name())
			if entries, _ := os.ReadDir(dir); len(entries) == 0 {
				continue
			}
			found[se.Name()] = installedModel{size: dirSize(dir)}
		}
	}
	return found
}

// activeModelName is the model the daemon would load with the current config.
func activeModelName(cfg config.Config) string {
	if cfg.Engine == "sherpa" {
		if cfg.SherpaModel != "" {
			return cfg.SherpaModel
		}
	}
	return cfg.Model
}

// unknownInstalled lists cached models with no catalog entry, sorted so the
// output is stable.
func unknownInstalled(installed map[string]installedModel) []string {
	var extras []string
	for name := range installed {
		if _, known := knownModels[name]; !known {
			extras = append(extras, name)
		}
	}
	sort.Strings(extras)
	return extras
}

func formatFileSize(bytes int64) string {
	mb := float64(bytes) / (1024 * 1024)
	if mb >= 1024 {
		return fmt.Sprintf("%.2f GB", mb/1024)
	}
	return fmt.Sprintf("%.1f MB", mb)
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func describeSherpaModel(name, dirPath string) string {
	if km, ok := knownModels[name]; ok {
		if km.Family != "" {
			return fmt.Sprintf("Sherpa ONNX / %s", km.Family)
		}
		return fmt.Sprintf("Sherpa ONNX / %s", km.Description)
	}
	// Try inspecting directory files
	entries, err := os.ReadDir(dirPath)
	if err == nil {
		for _, e := range entries {
			lower := strings.ToLower(e.Name())
			if strings.Contains(lower, "moonshine") {
				return "Sherpa ONNX / Moonshine"
			}
			if strings.Contains(lower, "sensevoice") || strings.Contains(lower, "sense-voice") {
				return "Sherpa ONNX / SenseVoice"
			}
			if strings.Contains(lower, "zipformer") {
				return "Sherpa ONNX / Zipformer"
			}
			if strings.Contains(lower, "parakeet") {
				return "Sherpa ONNX / NeMo Parakeet"
			}
			if strings.Contains(lower, "canary") {
				return "Sherpa ONNX / NeMo Canary"
			}
			if strings.Contains(lower, "paraformer") {
				return "Sherpa ONNX / Paraformer"
			}
		}
	}
	return "Sherpa ONNX"
}

func containsModelFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			name := strings.ToLower(e.Name())
			if strings.HasSuffix(name, ".onnx") || strings.HasSuffix(name, ".bin") || strings.HasSuffix(name, ".pt") {
				return true
			}
		}
	}
	return false
}

func downloadFile(url, dest string) error {
	tmp := dest + ".part"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "mavor/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}
