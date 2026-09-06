// Package models is mavor's model catalog: the set of speech models mavor can
// download, what each one is, and which inference runtime executes it.
//
// It lives under internal/ rather than in cmd/mavor because the runtime a
// model belongs to is a fact the daemon needs. `internal/speech` derives the
// runtime and its placement from the model name, and a catalog that only
// package main could see would have forced that fact to be duplicated
// somewhere it could drift.
package models

import (
	"fmt"
	"sort"
	"strings"
)

// KnownModel describes one downloadable speech model: where it comes from,
// how to unpack it, and the properties a user picks between when choosing one.
type KnownModel struct {
	Name        string // canonical name, as typed to `mavor models pull`
	Engine      string // "whisper" or "sherpa"
	Family      string // "Whisper", "NeMo", "Moonshine", "SenseVoice", "Zipformer"
	Description string
	URL         string
	Format      string // "raw", "tar.bz2", "tar.gz", "tgz", "tar"
	TargetDir   string // subfolder under model_dir/sherpa/

	// Filename is the name a whisper model has in the model cache: the
	// basename the URL serves, which is upstream's and not mavor's. It is
	// stated rather than derived from Name because the two deliberately
	// differ — the catalog calls the model whisper-base.en and the file on
	// disk is ggml-base.en.bin. Empty for sherpa models, which unpack into a
	// directory rather than landing as one file.
	Filename string

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

// Catalog is the set of distinct models mavor can download — one entry
// per artifact, and exactly one name per entry. Every name begins with its
// model family, so a listing sorts into families and a config value says which
// family of model the daemon will load without anyone having to look it up.
//
// The prefix names the model family, not the runtime: sherpa-onnx can also run
// Whisper in ONNX form, and such an entry would need a name of its own because
// whisper-base.en is taken by the GGML file that runs on whisper.cpp.
//
// Sizes were measured against the live URLs. Whisper artifacts come from the
// whisper.cpp GGML repository; sherpa artifacts from the sherpa-onnx release
// assets, which are pre-converted to ONNX and mostly INT8-quantized.
var Catalog = []KnownModel{
	// ---- OpenAI Whisper (GGML / whisper.cpp) -------------------------------
	// Whisper is encoder-decoder and transcribes in 30-second windows, so
	// none of these decode incrementally.
	{
		Name:   "whisper-tiny",
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Tiny, 39M parameters — fastest, least accurate",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.bin",
		Filename:     "ggml-tiny.bin",
		Format:       "raw",
		DownloadSize: 77691713,
		Languages:    "multi (99)",
		Speed:        "very fast",
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},
	{
		Name:   "whisper-tiny.en",
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Tiny, 39M parameters, English-only — what the test suite uses",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-tiny.en.bin",
		Filename:     "ggml-tiny.en.bin",
		Format:       "raw",
		DownloadSize: 77704715,
		Languages:    "en",
		Speed:        "very fast",
		MeasuredRTF:  0.061,
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},
	{
		Name:   "whisper-base",
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Base, 74M parameters",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.bin",
		Filename:     "ggml-base.bin",
		Format:       "raw",
		DownloadSize: 147951465,
		Languages:    "multi (99)",
		Speed:        "fast",
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},
	{
		Name:   "whisper-base.en",
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Base, 74M parameters, English-only — the default",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin",
		Filename:     "ggml-base.en.bin",
		Format:       "raw",
		DownloadSize: 147964211,
		Languages:    "en",
		Speed:        "fast",
		MeasuredRTF:  0.136,
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},
	{
		Name:   "whisper-small",
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Small, 244M parameters",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin",
		Filename:     "ggml-small.bin",
		Format:       "raw",
		DownloadSize: 487601967,
		Languages:    "multi (99)",
		Speed:        "moderate",
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},
	{
		Name:   "whisper-small.en",
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Small, 244M parameters, English-only",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.en.bin",
		Filename:     "ggml-small.en.bin",
		Format:       "raw",
		DownloadSize: 487614201,
		Languages:    "en",
		Speed:        "moderate",
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},
	{
		Name:   "whisper-medium",
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Medium, 769M parameters",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.bin",
		Filename:     "ggml-medium.bin",
		Format:       "raw",
		DownloadSize: 1533763059,
		Languages:    "multi (99)",
		Speed:        "slow",
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},
	{
		Name:   "whisper-medium.en",
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Medium, 769M parameters, English-only",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium.en.bin",
		Filename:     "ggml-medium.en.bin",
		Format:       "raw",
		DownloadSize: 1533774781,
		Languages:    "en",
		Speed:        "slow",
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},
	{
		Name:   "whisper-large-v3",
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Large v3, 1.55B parameters — most accurate, slowest",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3.bin",
		Filename:     "ggml-large-v3.bin",
		Format:       "raw",
		DownloadSize: 3095033483,
		Languages:    "multi (99)",
		Speed:        "very slow",
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},
	{
		Name:   "whisper-large-v3-turbo",
		Engine: "whisper", Family: "Whisper",
		Description:  "Whisper Large v3 Turbo, 809M parameters — large-v3 accuracy at a fraction of the decode cost",
		URL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin",
		Filename:     "ggml-large-v3-turbo.bin",
		Format:       "raw",
		DownloadSize: 1624555275,
		Languages:    "multi (99)",
		Speed:        "slow",
		MeasuredRTF:  1.519,
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},
	{
		Name:   "whisper-distil-large-v3",
		Engine: "whisper", Family: "Whisper",
		Description:  "Distil-Whisper Large v3, 756M parameters, English-only",
		URL:          "https://huggingface.co/distil-whisper/distil-large-v3-ggml/resolve/main/ggml-distil-large-v3.bin",
		Filename:     "ggml-distil-large-v3.bin",
		Format:       "raw",
		DownloadSize: 1519521155,
		Languages:    "en",
		Speed:        "slow",
		Vocabulary:   "initial prompt — the [vocabulary] table becomes whisper's --prompt, capped at 224 tokens",
	},

	// ---- NVIDIA NeMo (sherpa-onnx) -----------------------------------------
	{
		// Named for the architecture it is, after sitting in the catalog as
		// "parakeet" beside the unrelated parakeet-tdt-0.6b. TargetDir is
		// pinned to the old name because it defaults to the catalog name:
		// without the pin the rename would move the directory this model is
		// expected at and orphan a 450 MB download that is already on disk.
		Name:   "fastconformer-streaming",
		Engine: "sherpa", Family: "NeMo",
		TargetDir:    "parakeet",
		Description:  "NeMo FastConformer transducer, 80ms chunk — decodes while you speak",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-streaming-fast-conformer-transducer-en-80ms.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 450212918,
		Languages:    "en",
		Transducer:   true,
		Speed:        "fast",
		Vocabulary:   "hotwords supported (transducer)",
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
		Vocabulary:   "hotwords supported (transducer)",
	},
	{
		// Named for the artifact it actually downloads. The former name,
		// parakeet-tdt-1.1b, described a 1.1B model but fetched this 0.6B one.
		Name:   "parakeet-unified-en",
		Engine: "sherpa", Family: "NeMo",
		Description:  "NeMo Parakeet Unified 0.6B English, INT8, non-streaming",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-unified-en-0.6b-int8-non-streaming.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 501350460,
		Languages:    "en",
		Transducer:   true,
		Speed:        "moderate",
		Vocabulary:   "hotwords supported (transducer)",
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
		Name:   "canary-1b",
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
		Name:   "moonshine-tiny",
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
		Name:   "sensevoice-small",
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
		Name:   "zipformer-streaming",
		Engine: "sherpa", Family: "Zipformer",
		Description:  "Streaming Zipformer transducer — decodes while you speak",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-en-2023-06-26.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 310414022,
		Languages:    "en",
		Transducer:   true,
		Speed:        "fast",
		Vocabulary:   "hotwords supported (transducer)",
		Streaming:    true,
	},
	{
		Name:   "zipformer-streaming-20m",
		Engine: "sherpa", Family: "Zipformer",
		Description:  "Streaming Zipformer transducer, 20M parameters — small enough to run alongside another model as the live-preview source",
		URL:          "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-streaming-zipformer-en-20M-2023-02-17.tar.bz2",
		Format:       "tar.bz2",
		DownloadSize: 127887156,
		Languages:    "en",
		Transducer:   true,
		Speed:        "fast",
		Vocabulary:   "hotwords supported (transducer)",
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
		Vocabulary:   "hotwords supported (transducer)",
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

// index maps the catalog by name by name. There is exactly one name per
// model: a name that does not appear here does not resolve, and the caller
// says which real entries came closest rather than guessing a download.
var index = buildIndex()

func buildIndex() map[string]KnownModel {
	m := make(map[string]KnownModel, len(Catalog))
	for _, entry := range Catalog {
		spec := entry
		// A sherpa model unpacks into a directory named after the entry,
		// because that is what speech.ResolveSherpaModelDir looks for. An
		// entry that was renamed after the download already existed pins
		// TargetDir itself, and keeps the directory it had.
		if spec.Engine == "sherpa" && spec.TargetDir == "" {
			spec.TargetDir = spec.Name
		}
		m[spec.Name] = spec
	}
	return m
}

// Summary lists the downloadable names grouped by family. It is
// generated from modelCatalog so it cannot drift from what pull accepts.
func Summary() string {
	var families []string
	byFamily := map[string][]string{}
	for _, m := range Catalog {
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

// UnknownModelError reports a name the catalog does not carry, naming the
// entries closest to what was typed. There is no fallback download: one name
// per model means a typo is an error, and an error that lists real candidates
// is more use than one that silently fetches something else.
func UnknownModelError(name string) error {
	near := Nearest(name, 3)
	if len(near) == 0 {
		return fmt.Errorf("unknown model %q\n\n%s", name, Summary())
	}
	return fmt.Errorf("unknown model %q — did you mean %s?\n\n%s",
		name, strings.Join(quoteAll(near), " or "), Summary())
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}

// Nearest ranks the catalog by how close each name is to what was
// typed and returns the best few. Closeness is edit distance, with a name that
// contains the typed string pulled to the front so that "base.en" still finds
// "whisper-base.en" after the family prefixes landed — that substring case is
// exactly the mistake the rename creates.
func Nearest(name string, limit int) []string {
	type scored struct {
		name     string
		contains bool
		dist     int
	}
	lower := strings.ToLower(name)

	var all []scored
	for _, m := range Catalog {
		cand := strings.ToLower(m.Name)
		all = append(all, scored{
			name:     m.Name,
			contains: strings.Contains(cand, lower) || strings.Contains(lower, cand),
			dist:     editDistance(lower, cand),
		})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].contains != all[j].contains {
			return all[i].contains
		}
		return all[i].dist < all[j].dist
	})

	var out []string
	for _, s := range all {
		// A candidate that shares nothing with what was typed is noise; the
		// summary below the message already lists the whole catalog.
		if !s.contains && s.dist > len(lower) {
			break
		}
		out = append(out, s.name)
		if len(out) == limit {
			break
		}
	}
	return out
}

// editDistance is Levenshtein, iterative with a single row. The catalog has a
// couple of dozen short names, so the naive version is free.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, min(curr[j-1]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// Lookup returns the catalog entry for a name. There is exactly one name per
// model, so a name that does not resolve here is not a model mavor knows —
// the caller says which real entries came closest rather than guessing.
func Lookup(name string) (KnownModel, bool) {
	m, ok := index[name]
	return m, ok
}

// Count reports how many models the catalog carries.
func Count() int { return len(index) }
