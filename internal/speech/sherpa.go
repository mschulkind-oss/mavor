package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// SherpaModelType represents supported Sherpa ONNX model architectures.
type SherpaModelType string

const (
	ModelTypeAuto         SherpaModelType = "auto"
	ModelTypeTransducer   SherpaModelType = "transducer"    // Parakeet-TDT, Zipformer Transducer
	ModelTypeMoonshine    SherpaModelType = "moonshine"     // Useful Sensors Moonshine
	ModelTypeZipformerCTC SherpaModelType = "zipformer_ctc" // Zipformer CTC
	ModelTypeSenseVoice   SherpaModelType = "sensevoice"    // Alibaba SenseVoice
	ModelTypeParaformer   SherpaModelType = "paraformer"    // FunASR Paraformer
	ModelTypeNemoCTC      SherpaModelType = "nemo_ctc"      // NeMo CTC
	ModelTypeWhisper      SherpaModelType = "whisper"       // Whisper ONNX
)

// TransducerModelConfig configures offline transducer models (e.g. Parakeet-TDT, Zipformer Transducer).
type TransducerModelConfig struct {
	Encoder string
	Decoder string
	Joiner  string
}

// MoonshineModelConfig configures offline Moonshine models (v1 and v2).
type MoonshineModelConfig struct {
	Preprocessor    string
	Encoder         string
	UncachedDecoder string
	CachedDecoder   string
	MergedDecoder   string
}

// ZipformerCTCModelConfig configures offline Zipformer CTC models.
type ZipformerCTCModelConfig struct {
	Model string
}

// SenseVoiceModelConfig configures offline SenseVoice models.
type SenseVoiceModelConfig struct {
	Model                       string
	Language                    string
	UseInverseTextNormalization int
}

// ParaformerModelConfig configures offline Paraformer models.
type ParaformerModelConfig struct {
	Encoder string
	Decoder string
	Model   string
}

// NemoCTCModelConfig configures offline NeMo CTC models.
type NemoCTCModelConfig struct {
	Model string
}

// WhisperModelConfig configures offline Whisper ONNX models.
type WhisperModelConfig struct {
	Encoder  string
	Decoder  string
	Language string
	Task     string
}

// OnlineTransducerConfig configures online/streaming transducer models.
type OnlineTransducerConfig struct {
	Encoder string
	Decoder string
	Joiner  string
}

// OnlineZipformerCTCConfig configures online/streaming Zipformer CTC models.
type OnlineZipformerCTCConfig struct {
	Model string
}

// OnlineParaformerConfig configures online/streaming Paraformer models.
type OnlineParaformerConfig struct {
	Encoder string
	Decoder string
}

// SherpaOfflineConfig contains complete parameters for instantiating an OfflineRecognizer.
type SherpaOfflineConfig struct {
	ModelType      SherpaModelType
	Transducer     TransducerModelConfig
	Moonshine      MoonshineModelConfig
	ZipformerCTC   ZipformerCTCModelConfig
	SenseVoice     SenseVoiceModelConfig
	Paraformer     ParaformerModelConfig
	NemoCTC        NemoCTCModelConfig
	Whisper        WhisperModelConfig
	Tokens         string
	NumThreads     int
	Provider       string
	DecodingMethod string
	MaxActivePaths int
	HotwordsFile   string
	HotwordsScore  float32
	BlankPenalty   float32
	RuleFsts       string
	RuleFars       string
	SampleRate     int
	FeatureDim     int
}

// SherpaOnlineConfig contains complete parameters for instantiating an OnlineRecognizer.
type SherpaOnlineConfig struct {
	ModelType      SherpaModelType
	Transducer     OnlineTransducerConfig
	ZipformerCTC   OnlineZipformerCTCConfig
	Paraformer     OnlineParaformerConfig
	Tokens         string
	NumThreads     int
	Provider       string
	DecodingMethod string
	MaxActivePaths int
	HotwordsFile   string
	HotwordsScore  float32
	BlankPenalty   float32
	RuleFsts       string
	RuleFars       string
	SampleRate     int
	FeatureDim     int
}

// SherpaRecognizer is the common recognizer backend interface.
// It is implemented by real CGO recognizers (offline or online) and test mocks.
type SherpaRecognizer interface {
	DecodeAudio(ctx context.Context, sampleRate int, samples []float32) (string, error)
	Close() error
}

// SherpaStreamRecognizer is an optional interface implemented by recognizers that support chunk streaming.
type SherpaStreamRecognizer interface {
	StartStream(ctx context.Context) error
	FeedChunk(ctx context.Context, chunk []byte) (string, error)
	StopStream(ctx context.Context) (string, error)
}

// RecognizerBuilderFunc builds a SherpaRecognizer from configuration.
type RecognizerBuilderFunc func(cfg config.Config, sc SherpaOfflineConfig, logger *slog.Logger) (SherpaRecognizer, error)

// DefaultOfflineRecognizerBuilder is initialized by CGO build or stub.
var DefaultOfflineRecognizerBuilder RecognizerBuilderFunc

// SherpaTranscriber implements the Transcriber and StreamTranscriber interfaces for in-process sherpa-onnx inference.
type SherpaTranscriber struct {
	Config            config.Config
	SherpaConfig      SherpaOfflineConfig
	ModelDir          string
	Logger            *slog.Logger
	Recognizer        SherpaRecognizer
	RecognizerBuilder RecognizerBuilderFunc

	mu      sync.Mutex
	started bool
}

// StartStream initializes a new streaming recognition session if supported by backend.
func (s *SherpaTranscriber) StartStream(ctx context.Context) error {
	s.mu.Lock()
	if !s.started || s.Recognizer == nil {
		s.mu.Unlock()
		if err := s.Start(ctx); err != nil {
			return err
		}
		s.mu.Lock()
	}
	rec := s.Recognizer
	s.mu.Unlock()

	if sr, ok := rec.(SherpaStreamRecognizer); ok {
		return sr.StartStream(ctx)
	}
	return nil
}

// FeedChunk sends raw PCM audio bytes (16kHz mono s16le) to the active streaming recognizer.
func (s *SherpaTranscriber) FeedChunk(ctx context.Context, chunk []byte) (string, error) {
	s.mu.Lock()
	rec := s.Recognizer
	s.mu.Unlock()

	if sr, ok := rec.(SherpaStreamRecognizer); ok {
		return sr.FeedChunk(ctx, chunk)
	}
	return "", nil
}

// StopStream concludes the active streaming session and returns any final accumulated text.
func (s *SherpaTranscriber) StopStream(ctx context.Context) (string, error) {
	s.mu.Lock()
	rec := s.Recognizer
	s.mu.Unlock()

	if sr, ok := rec.(SherpaStreamRecognizer); ok {
		return sr.StopStream(ctx)
	}
	return "", nil
}

// NewSherpaTranscriber constructs a SherpaTranscriber with resolved model directory and configuration.
func NewSherpaTranscriber(cfg config.Config, logger *slog.Logger) (*SherpaTranscriber, error) {
	if logger == nil {
		logger = slog.Default()
	}

	modelDir, err := ResolveSherpaModelDir(cfg)
	if err != nil {
		return nil, fmt.Errorf("speech: resolve sherpa model directory: %w", err)
	}

	sc, err := BuildSherpaOfflineConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("speech: build sherpa offline config: %w", err)
	}

	return &SherpaTranscriber{
		Config:            cfg,
		SherpaConfig:      sc,
		ModelDir:          modelDir,
		Logger:            logger,
		RecognizerBuilder: DefaultOfflineRecognizerBuilder,
	}, nil
}

// Start warms up and initializes the underlying sherpa recognizer.
func (s *SherpaTranscriber) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started && s.Recognizer != nil {
		return nil
	}

	if s.Recognizer == nil {
		if s.RecognizerBuilder == nil {
			return fmt.Errorf("speech: sherpa recognizer not initialized (engine requires -tags sherpa or injected recognizer)")
		}
		rec, err := s.RecognizerBuilder(s.Config, s.SherpaConfig, s.Logger)
		if err != nil {
			return fmt.Errorf("speech: initialize sherpa recognizer: %w", err)
		}
		s.Recognizer = rec
	}

	s.started = true
	if s.Logger != nil {
		s.Logger.Info("speech: sherpa recognizer started",
			"model_type", s.SherpaConfig.ModelType,
			"model_dir", s.ModelDir,
			"threads", s.SherpaConfig.NumThreads,
			"provider", s.SherpaConfig.Provider,
		)
	}
	return nil
}

// Close releases resources held by the SherpaRecognizer.
func (s *SherpaTranscriber) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var err error
	if s.Recognizer != nil {
		err = s.Recognizer.Close()
		s.Recognizer = nil
	}
	s.started = false
	return err
}

// IsStarted returns whether the transcriber has been started.
func (s *SherpaTranscriber) IsStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

// Transcribe reads the audio file at wavPath and returns the transcribed text.
func (s *SherpaTranscriber) Transcribe(ctx context.Context, wavPath string) (string, error) {
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	s.mu.Lock()
	if !s.started || s.Recognizer == nil {
		s.mu.Unlock()
		if err := s.Start(ctx); err != nil {
			return "", err
		}
		s.mu.Lock()
	}
	rec := s.Recognizer
	s.mu.Unlock()

	sampleRate, samples, err := ReadWAVAudio(wavPath)
	if err != nil {
		return "", fmt.Errorf("speech: read wav %s: %w", wavPath, err)
	}

	durationSec := float64(len(samples)) / float64(sampleRate)
	log.Info("speech: running sherpa inference",
		"wav", wavPath,
		"sample_rate", sampleRate,
		"sample_count", len(samples),
		"duration_sec", fmt.Sprintf("%.2f", durationSec),
	)

	start := time.Now()
	text, err := rec.DecodeAudio(ctx, sampleRate, samples)
	elapsed := time.Since(start)

	if err != nil {
		return "", fmt.Errorf("speech: sherpa decode %s: %w", wavPath, err)
	}

	trimmed := strings.TrimSpace(text)
	log.Info("speech: sherpa transcript ready",
		"wav", wavPath,
		"duration_ms", elapsed.Milliseconds(),
		"text_len", len(trimmed),
		"text_preview", truncate(trimmed, 200),
	)

	return trimmed, nil
}

// ResolveSherpaModelDir locates the model directory according to standard resolution order:
// 1. cfg.SherpaModel or cfg.Model as explicit path if exists
// 2. $XDG_DATA_HOME/mavor/models/sherpa/<model>
// 3. $XDG_DATA_HOME/mavor/models/<model>
// 4. cfg.ModelDir/sherpa/<model>
// 5. cfg.ModelDir/<model>
// 6. $XDG_DATA_HOME/mavor/models/sherpa/ (base)
// 7. cfg.ModelDir/sherpa/ (base) or cfg.ModelDir
func ResolveSherpaModelDir(cfg config.Config) (string, error) {
	modelName := cfg.SherpaModel
	if modelName == "" {
		modelName = cfg.Model
	}

	// 1. Check if modelName is an existing directory
	if modelName != "" {
		expanded := config.ExpandPath(modelName)
		if fi, err := os.Stat(expanded); err == nil && fi.IsDir() {
			return expanded, nil
		}
	}

	dataHome := config.XDGDataHome()
	var specificCandidates []string
	if modelName != "" {
		specificCandidates = append(specificCandidates,
			filepath.Join(dataHome, "mavor", "models", "sherpa", modelName),
			filepath.Join(dataHome, "mavor", "models", modelName),
		)
		if cfg.ModelDir != "" {
			specificCandidates = append(specificCandidates,
				filepath.Join(cfg.ModelDir, "sherpa", modelName),
				filepath.Join(cfg.ModelDir, modelName),
			)
		}
	}

	for _, dir := range specificCandidates {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir, nil
		}
	}

	var baseCandidates []string
	baseCandidates = append(baseCandidates,
		filepath.Join(dataHome, "mavor", "models", "sherpa"),
	)
	if cfg.ModelDir != "" {
		baseCandidates = append(baseCandidates,
			filepath.Join(cfg.ModelDir, "sherpa"),
			cfg.ModelDir,
		)
	}

	for _, dir := range baseCandidates {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			if containsSherpaModelFiles(dir) {
				return dir, nil
			}
		}
	}

	allCandidates := append(specificCandidates, baseCandidates...)
	return "", fmt.Errorf("speech: sherpa model %q not found in candidate paths %v — run `mavor models pull %s`", modelName, allCandidates, modelName)
}

func containsSherpaModelFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	hasTokens := false
	hasOnnx := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".onnx") {
			hasOnnx = true
		}
		if name == "tokens.txt" || name == "bpe.model" || name == "vocab.txt" {
			hasTokens = true
		}
	}
	return hasOnnx && hasTokens
}

// DetectSherpaModelType inspects the directory contents and model name to determine model architecture.
func DetectSherpaModelType(modelDir string, modelName string) (SherpaModelType, error) {
	cleanName := strings.ToLower(modelName)

	if findFile(modelDir, "preprocess.onnx", "preprocessor.onnx", "preprocess.int8.onnx", "preprocessor.int8.onnx", "merged_decoder.onnx", "uncached_decoder.onnx") != "" ||
		strings.Contains(cleanName, "moonshine") {
		return ModelTypeMoonshine, nil
	}

	if findFile(modelDir, "joiner.onnx", "joiner.int8.onnx", "join.onnx") != "" ||
		strings.Contains(cleanName, "parakeet") ||
		strings.Contains(cleanName, "transducer") {
		return ModelTypeTransducer, nil
	}

	if strings.Contains(cleanName, "sensevoice") || findFile(modelDir, "sensevoice.onnx") != "" {
		return ModelTypeSenseVoice, nil
	}

	if strings.Contains(cleanName, "zipformer") {
		if findFile(modelDir, "joiner.onnx", "joiner.int8.onnx") != "" {
			return ModelTypeTransducer, nil
		}
		return ModelTypeZipformerCTC, nil
	}

	encoder := findFile(modelDir, "encoder.onnx", "encoder.int8.onnx")
	decoder := findFile(modelDir, "decoder.onnx", "decoder.int8.onnx")
	if encoder != "" && decoder != "" {
		if strings.Contains(cleanName, "whisper") {
			return ModelTypeWhisper, nil
		}
		return ModelTypeParaformer, nil
	}

	if findFile(modelDir, "model.onnx", "model.int8.onnx") != "" {
		return ModelTypeZipformerCTC, nil
	}

	return ModelTypeTransducer, nil
}

// resolveDecoding picks the decoding method and hotword boost for a sherpa
// recognizer. sherpa-onnx honours a hotwords file only under
// modified_beam_search — greedy_search ignores it without complaint — so
// configuring hotwords selects the beam search unless the user asked for a
// specific method themselves. Without hotwords, greedy_search stays the
// default: it is faster and the beam buys nothing.
func resolveDecoding(cfg config.Config) (method string, hotwordsScore float32) {
	method = cfg.SherpaDecodingMethod
	if method == "" {
		if cfg.SherpaHotwordsFile != "" {
			method = "modified_beam_search"
		} else {
			method = "greedy_search"
		}
	}

	hotwordsScore = cfg.SherpaHotwordsScore
	if hotwordsScore == 0 && cfg.SherpaHotwordsFile != "" {
		hotwordsScore = 1.5
	}
	return method, hotwordsScore
}

// BuildSherpaOfflineConfig constructs a SherpaOfflineConfig from Config and model directory.
func BuildSherpaOfflineConfig(cfg config.Config) (SherpaOfflineConfig, error) {
	modelDir, err := ResolveSherpaModelDir(cfg)
	if err != nil {
		return SherpaOfflineConfig{}, err
	}

	modelName := cfg.SherpaModel
	if modelName == "" {
		modelName = cfg.Model
	}

	modelType := SherpaModelType(strings.ToLower(strings.TrimSpace(cfg.SherpaModelType)))
	if modelType == "" || modelType == ModelTypeAuto {
		detected, err := DetectSherpaModelType(modelDir, modelName)
		if err != nil {
			return SherpaOfflineConfig{}, err
		}
		modelType = detected
	}

	tokens := cfg.SherpaTokens
	if tokens == "" {
		tokens = findFile(modelDir, "tokens.txt", "bpe.model", "vocab.txt", "tokens.json")
	}
	if tokens == "" {
		return SherpaOfflineConfig{}, fmt.Errorf("speech: sherpa tokens file (tokens.txt/bpe.model) not found in %s", modelDir)
	}

	threads := cfg.Threads
	if threads <= 0 {
		threads = 4
	}

	provider := cfg.SherpaProvider
	if provider == "" {
		provider = "cpu"
	}

	decodingMethod, hotwordsScore := resolveDecoding(cfg)

	sc := SherpaOfflineConfig{
		ModelType:      modelType,
		Tokens:         tokens,
		NumThreads:     threads,
		Provider:       provider,
		DecodingMethod: decodingMethod,
		HotwordsFile:   cfg.SherpaHotwordsFile,
		HotwordsScore:  hotwordsScore,
		SampleRate:     16000,
		FeatureDim:     80,
	}

	switch modelType {
	case ModelTypeTransducer:
		encoder := cfg.SherpaEncoder
		if encoder == "" {
			encoder = findFile(modelDir, "encoder.onnx", "encoder.int8.onnx", "encode.onnx")
		}
		decoder := cfg.SherpaDecoder
		if decoder == "" {
			decoder = findFile(modelDir, "decoder.onnx", "decoder.int8.onnx", "decode.onnx")
		}
		joiner := cfg.SherpaJoiner
		if joiner == "" {
			joiner = findFile(modelDir, "joiner.onnx", "joiner.int8.onnx", "join.onnx")
		}

		if encoder == "" || decoder == "" || joiner == "" {
			return SherpaOfflineConfig{}, fmt.Errorf("speech: transducer model in %s requires encoder, decoder, and joiner onnx files (found: encoder=%q, decoder=%q, joiner=%q)", modelDir, encoder, decoder, joiner)
		}
		sc.Transducer = TransducerModelConfig{
			Encoder: encoder,
			Decoder: decoder,
			Joiner:  joiner,
		}

	case ModelTypeMoonshine:
		pre := findFile(modelDir, "preprocess.onnx", "preprocessor.onnx", "preprocess.int8.onnx")
		enc := cfg.SherpaEncoder
		if enc == "" {
			enc = findFile(modelDir, "encode.onnx", "encoder.onnx", "encode.int8.onnx", "encoder.int8.onnx")
		}
		uncached := cfg.SherpaDecoder
		if uncached == "" {
			uncached = findFile(modelDir, "uncached_decode.onnx", "uncached_decoder.onnx", "uncached_decode.int8.onnx")
		}
		cached := findFile(modelDir, "cached_decode.onnx", "cached_decoder.onnx", "cached_decode.int8.onnx")
		merged := findFile(modelDir, "merged_decoder.onnx", "merged_decode.onnx", "merged_decoder.int8.onnx")

		if enc == "" {
			return SherpaOfflineConfig{}, fmt.Errorf("speech: moonshine model in %s missing encoder onnx file", modelDir)
		}
		if merged == "" && (uncached == "" || cached == "") {
			return SherpaOfflineConfig{}, fmt.Errorf("speech: moonshine model in %s requires either merged_decoder or (uncached_decoder + cached_decoder)", modelDir)
		}

		sc.Moonshine = MoonshineModelConfig{
			Preprocessor:    pre,
			Encoder:         enc,
			UncachedDecoder: uncached,
			CachedDecoder:   cached,
			MergedDecoder:   merged,
		}

	case ModelTypeZipformerCTC, ModelTypeNemoCTC:
		modelFile := cfg.SherpaEncoder
		if modelFile == "" {
			modelFile = findFile(modelDir, "model.onnx", "model.int8.onnx")
		}
		if modelFile == "" {
			return SherpaOfflineConfig{}, fmt.Errorf("speech: CTC model in %s missing model.onnx", modelDir)
		}
		sc.ZipformerCTC = ZipformerCTCModelConfig{Model: modelFile}
		sc.NemoCTC = NemoCTCModelConfig{Model: modelFile}

	case ModelTypeSenseVoice:
		modelFile := cfg.SherpaEncoder
		if modelFile == "" {
			modelFile = findFile(modelDir, "model.onnx", "model.int8.onnx", "sensevoice.onnx")
		}
		if modelFile == "" {
			return SherpaOfflineConfig{}, fmt.Errorf("speech: sensevoice model in %s missing model.onnx", modelDir)
		}
		sc.SenseVoice = SenseVoiceModelConfig{
			Model:                       modelFile,
			Language:                    "auto",
			UseInverseTextNormalization: 1,
		}

	case ModelTypeParaformer:
		enc := cfg.SherpaEncoder
		if enc == "" {
			enc = findFile(modelDir, "encoder.onnx", "encoder.int8.onnx", "model.onnx")
		}
		dec := cfg.SherpaDecoder
		if dec == "" {
			dec = findFile(modelDir, "decoder.onnx", "decoder.int8.onnx")
		}
		sc.Paraformer = ParaformerModelConfig{
			Encoder: enc,
			Decoder: dec,
			Model:   enc,
		}

	case ModelTypeWhisper:
		enc := cfg.SherpaEncoder
		if enc == "" {
			enc = findFile(modelDir, "encoder.onnx", "encoder.int8.onnx")
		}
		dec := cfg.SherpaDecoder
		if dec == "" {
			dec = findFile(modelDir, "decoder.onnx", "decoder.int8.onnx")
		}
		sc.Whisper = WhisperModelConfig{
			Encoder:  enc,
			Decoder:  dec,
			Language: "en",
			Task:     "transcribe",
		}
	}

	return sc, nil
}

// BuildSherpaOnlineConfig constructs a SherpaOnlineConfig from Config and model directory.
func BuildSherpaOnlineConfig(cfg config.Config) (SherpaOnlineConfig, error) {
	modelDir, err := ResolveSherpaModelDir(cfg)
	if err != nil {
		return SherpaOnlineConfig{}, err
	}

	tokens := cfg.SherpaTokens
	if tokens == "" {
		tokens = findFile(modelDir, "tokens.txt", "bpe.model", "vocab.txt")
	}
	if tokens == "" {
		return SherpaOnlineConfig{}, fmt.Errorf("speech: online tokens file not found in %s", modelDir)
	}

	threads := cfg.Threads
	if threads <= 0 {
		threads = 4
	}

	provider := cfg.SherpaProvider
	if provider == "" {
		provider = "cpu"
	}

	decodingMethod, hotwordsScore := resolveDecoding(cfg)

	encoder := cfg.SherpaEncoder
	if encoder == "" {
		encoder = findFile(modelDir, "encoder.onnx", "encoder.int8.onnx")
	}
	decoder := cfg.SherpaDecoder
	if decoder == "" {
		decoder = findFile(modelDir, "decoder.onnx", "decoder.int8.onnx")
	}
	joiner := cfg.SherpaJoiner
	if joiner == "" {
		joiner = findFile(modelDir, "joiner.onnx", "joiner.int8.onnx")
	}

	return SherpaOnlineConfig{
		ModelType: ModelTypeTransducer,
		Transducer: OnlineTransducerConfig{
			Encoder: encoder,
			Decoder: decoder,
			Joiner:  joiner,
		},
		Tokens:         tokens,
		NumThreads:     threads,
		Provider:       provider,
		DecodingMethod: decodingMethod,
		HotwordsFile:   cfg.SherpaHotwordsFile,
		HotwordsScore:  hotwordsScore,
		SampleRate:     16000,
		FeatureDim:     80,
	}, nil
}

func findFile(dir string, candidates ...string) string {
	for _, c := range candidates {
		p := filepath.Join(dir, c)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// ReadWAVAudio reads a standard PCM or IEEE float WAV file and returns sampleRate and mono []float32 samples in [-1.0, 1.0].
func ReadWAVAudio(wavPath string) (int, []float32, error) {
	data, err := os.ReadFile(wavPath)
	if err != nil {
		return 0, nil, fmt.Errorf("open %s: %w", wavPath, err)
	}

	if len(data) < 12 {
		return 0, nil, fmt.Errorf("wav file %s too short (%d bytes)", wavPath, len(data))
	}

	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, nil, fmt.Errorf("%s is not a valid RIFF/WAVE file", wavPath)
	}

	r := bytes.NewReader(data[12:])
	var audioFormat uint16
	var numChannels uint16
	var sampleRate uint32
	var bitsPerSample uint16
	var rawData []byte
	foundFmt := false
	foundData := false

	for {
		var chunkHeader struct {
			ID   [4]byte
			Size uint32
		}
		if err := binary.Read(r, binary.LittleEndian, &chunkHeader); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, nil, fmt.Errorf("read chunk header: %w", err)
		}

		chunkID := string(chunkHeader.ID[:])
		chunkSize := int(chunkHeader.Size)

		if chunkID == "fmt " {
			var fmtChunk struct {
				AudioFormat   uint16
				NumChannels   uint16
				SampleRate    uint32
				ByteRate      uint32
				BlockAlign    uint16
				BitsPerSample uint16
			}
			if err := binary.Read(r, binary.LittleEndian, &fmtChunk); err != nil {
				return 0, nil, fmt.Errorf("read fmt chunk: %w", err)
			}
			audioFormat = fmtChunk.AudioFormat
			numChannels = fmtChunk.NumChannels
			sampleRate = fmtChunk.SampleRate
			bitsPerSample = fmtChunk.BitsPerSample
			foundFmt = true

			// Skip any remaining bytes in fmt chunk (e.g. cbSize)
			remaining := chunkSize - 16
			if remaining > 0 {
				if _, err := r.Seek(int64(remaining), io.SeekCurrent); err != nil {
					return 0, nil, err
				}
			}
		} else if chunkID == "data" {
			rawData = make([]byte, chunkSize)
			if _, err := io.ReadFull(r, rawData); err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
					return 0, nil, fmt.Errorf("read data chunk: %w", err)
				}
			}
			foundData = true
			break
		} else {
			// Skip other chunks (e.g. LIST, JUNK)
			if _, err := r.Seek(int64(chunkSize), io.SeekCurrent); err != nil {
				return 0, nil, err
			}
		}

		// WAV chunk padding to 2 bytes
		if chunkSize%2 != 0 {
			if _, err := r.Seek(1, io.SeekCurrent); err != nil {
				return 0, nil, err
			}
		}
	}

	if !foundFmt {
		return 0, nil, fmt.Errorf("%s missing fmt chunk", wavPath)
	}
	if !foundData || len(rawData) == 0 {
		return int(sampleRate), nil, nil
	}
	if numChannels == 0 {
		return 0, nil, fmt.Errorf("invalid channel count 0")
	}

	bytesPerSample := int(bitsPerSample / 8)
	if bytesPerSample == 0 {
		bytesPerSample = 2 // fallback 16-bit
	}
	totalSamples := len(rawData) / bytesPerSample
	frames := totalSamples / int(numChannels)
	samples := make([]float32, frames)

	switch {
	case audioFormat == 1 && bitsPerSample == 16: // 16-bit signed PCM
		for f := 0; f < frames; f++ {
			var sum float32
			for ch := 0; ch < int(numChannels); ch++ {
				idx := (f*int(numChannels) + ch) * 2
				if idx+2 <= len(rawData) {
					val := int16(binary.LittleEndian.Uint16(rawData[idx : idx+2]))
					sum += float32(val) / 32768.0
				}
			}
			samples[f] = sum / float32(numChannels)
		}

	case audioFormat == 1 && bitsPerSample == 24: // 24-bit signed PCM
		for f := 0; f < frames; f++ {
			var sum float32
			for ch := 0; ch < int(numChannels); ch++ {
				idx := (f*int(numChannels) + ch) * 3
				if idx+3 <= len(rawData) {
					b0 := rawData[idx]
					b1 := rawData[idx+1]
					b2 := rawData[idx+2]
					val := int32(b0) | (int32(b1) << 8) | (int32(int8(b2)) << 16)
					sum += float32(val) / 8388608.0
				}
			}
			samples[f] = sum / float32(numChannels)
		}

	case audioFormat == 1 && bitsPerSample == 32: // 32-bit signed PCM
		for f := 0; f < frames; f++ {
			var sum float32
			for ch := 0; ch < int(numChannels); ch++ {
				idx := (f*int(numChannels) + ch) * 4
				if idx+4 <= len(rawData) {
					val := int32(binary.LittleEndian.Uint32(rawData[idx : idx+4]))
					sum += float32(val) / 2147483648.0
				}
			}
			samples[f] = sum / float32(numChannels)
		}

	case audioFormat == 3 && bitsPerSample == 32: // 32-bit IEEE float
		for f := 0; f < frames; f++ {
			var sum float32
			for ch := 0; ch < int(numChannels); ch++ {
				idx := (f*int(numChannels) + ch) * 4
				if idx+4 <= len(rawData) {
					bits := binary.LittleEndian.Uint32(rawData[idx : idx+4])
					sum += math.Float32frombits(bits)
				}
			}
			samples[f] = sum / float32(numChannels)
		}

	default:
		return 0, nil, fmt.Errorf("unsupported wav format: audioFormat=%d, bitsPerSample=%d", audioFormat, bitsPerSample)
	}

	return int(sampleRate), samples, nil
}

// MockSherpaRecognizer is a mock implementation of SherpaRecognizer for unit tests.
type MockSherpaRecognizer struct {
	Text           string
	Err            error
	CloseErr       error
	LastSampleRate int
	LastSamples    []float32
	Closed         bool
	DecodeFunc     func(ctx context.Context, sampleRate int, samples []float32) (string, error)
	StreamPartials []string
	StreamFinal    string
	streamIdx      int
	FeedFn         func(ctx context.Context, chunk []byte) (string, error)
}

func (m *MockSherpaRecognizer) DecodeAudio(ctx context.Context, sampleRate int, samples []float32) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.LastSampleRate = sampleRate
	m.LastSamples = samples
	if m.DecodeFunc != nil {
		return m.DecodeFunc(ctx, sampleRate, samples)
	}
	return m.Text, m.Err
}

func (m *MockSherpaRecognizer) StartStream(ctx context.Context) error {
	m.streamIdx = 0
	return nil
}

func (m *MockSherpaRecognizer) FeedChunk(ctx context.Context, chunk []byte) (string, error) {
	if m.FeedFn != nil {
		return m.FeedFn(ctx, chunk)
	}
	if len(m.StreamPartials) > 0 {
		if m.streamIdx < len(m.StreamPartials) {
			txt := m.StreamPartials[m.streamIdx]
			m.streamIdx++
			return txt, nil
		}
		return m.StreamPartials[len(m.StreamPartials)-1], nil
	}
	return m.Text, m.Err
}

func (m *MockSherpaRecognizer) StopStream(ctx context.Context) (string, error) {
	if m.StreamFinal != "" {
		return m.StreamFinal, m.Err
	}
	return m.Text, m.Err
}

func (m *MockSherpaRecognizer) Close() error {
	m.Closed = true
	return m.CloseErr
}
