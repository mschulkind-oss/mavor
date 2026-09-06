package speech

import (
	"path/filepath"
	"strings"
)

// whisperModelFiles maps a catalog name to the file the upstream download
// serves for it. The two differ on purpose: every catalog name begins with its
// model family, so the entry is `whisper-base.en`, while the GGML repository
// serves `ggml-base.en.bin` and a file already on disk keeps that name.
//
// This is a table rather than a rule because the mapping is a property of what
// upstream publishes, not of how mavor names things. It is the single place
// the two vocabularies meet: every caller that needs a whisper model's path
// goes through WhisperModelPath, so a name that is wrong here is wrong
// everywhere at once rather than in some code paths only.
var whisperModelFiles = map[string]string{
	"whisper-tiny":            "ggml-tiny.bin",
	"whisper-tiny.en":         "ggml-tiny.en.bin",
	"whisper-base":            "ggml-base.bin",
	"whisper-base.en":         "ggml-base.en.bin",
	"whisper-small":           "ggml-small.bin",
	"whisper-small.en":        "ggml-small.en.bin",
	"whisper-medium":          "ggml-medium.bin",
	"whisper-medium.en":       "ggml-medium.en.bin",
	"whisper-large-v3":        "ggml-large-v3.bin",
	"whisper-large-v3-turbo":  "ggml-large-v3-turbo.bin",
	"whisper-distil-large-v3": "ggml-distil-large-v3.bin",
}

// WhisperModelFile returns the on-disk basename for the whisper model named by
// a catalog name.
//
// A name with no catalog entry is a model the user placed or converted
// themselves. For those the name is assumed to be the GGML stem, with a
// leading "whisper-" dropped if they followed the catalog's naming, so
// "my-tune" resolves to "ggml-my-tune.bin".
func WhisperModelFile(name string) string {
	if file, ok := whisperModelFiles[name]; ok {
		return file
	}
	return "ggml-" + strings.TrimPrefix(name, "whisper-") + ".bin"
}

// WhisperModelPath returns the path the whisper model named by a catalog name
// occupies inside modelDir. Every caller that needs one uses this: the daemon,
// `mavor doctor`, `mavor models`, the benchmark harness and the integration
// harness all agree on where a model lives because they ask the same function.
func WhisperModelPath(modelDir, name string) string {
	return filepath.Join(modelDir, WhisperModelFile(name))
}

// IsWhisperModelFile reports whether a name in the model cache is a whisper
// GGML model file. The "ggml-" prefix and ".bin" suffix are upstream's naming
// convention, so the check lives here beside the table that depends on it.
func IsWhisperModelFile(name string) bool {
	return strings.HasPrefix(name, "ggml-") && strings.HasSuffix(name, ".bin")
}

// WhisperCatalogName is the inverse of WhisperModelFile: it reports the catalog
// name a file in the model cache belongs to, and whether the file is one the
// catalog knows about at all. A cached file with no catalog entry is still a
// model the user has, so callers list it under its own stem.
func WhisperCatalogName(file string) (string, bool) {
	for name, known := range whisperModelFiles {
		if known == file {
			return name, true
		}
	}
	return strings.TrimSuffix(strings.TrimPrefix(file, "ggml-"), ".bin"), false
}
