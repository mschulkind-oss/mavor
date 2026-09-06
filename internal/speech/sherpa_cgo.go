package speech

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	sherpa_onnx "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
	"github.com/mschulkind-oss/mavor/internal/config"
)

func init() {
	DefaultOfflineRecognizerBuilder = newCGOOfflineRecognizer
	DefaultOnlineRecognizerBuilder = newCGOOnlineRecognizer
}

// boolToInt converts to the 0/1 int the sherpa-onnx C API takes.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func newSherpaTranscriber(cfg config.Config, logger *slog.Logger) (Transcriber, error) {
	return NewSherpaTranscriber(cfg, logger)
}

// cgoOfflineRecognizer wraps sherpa_onnx.OfflineRecognizer for in-process CGO execution.
type cgoOfflineRecognizer struct {
	impl *sherpa_onnx.OfflineRecognizer
	mu   sync.Mutex
}

func (r *cgoOfflineRecognizer) DecodeAudio(ctx context.Context, sampleRate int, samples []float32) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(samples) == 0 {
		return "", nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.impl == nil {
		return "", fmt.Errorf("speech: sherpa offline recognizer is closed")
	}

	stream := sherpa_onnx.NewOfflineStream(r.impl)
	if stream == nil {
		return "", fmt.Errorf("speech: failed to create sherpa offline stream")
	}
	defer sherpa_onnx.DeleteOfflineStream(stream)

	stream.AcceptWaveform(sampleRate, samples)
	r.impl.Decode(stream)

	res := stream.GetResult()
	if res == nil {
		return "", nil
	}
	return res.Text, nil
}

func (r *cgoOfflineRecognizer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.impl != nil {
		sherpa_onnx.DeleteOfflineRecognizer(r.impl)
		r.impl = nil
	}
	return nil
}

// cgoOnlineRecognizer wraps sherpa_onnx.OnlineRecognizer for in-process streaming CGO execution.
type cgoOnlineRecognizer struct {
	impl         *sherpa_onnx.OnlineRecognizer
	activeStream *sherpa_onnx.OnlineStream
	mu           sync.Mutex

	// samples is the int16-to-float32 conversion buffer, reused across
	// chunks. FeedChunk runs every 30ms for as long as anyone is speaking,
	// and AcceptWaveform copies what it is given into the stream's own
	// feature extractor before returning — the C API takes a const pointer
	// and consumes it synchronously — so the buffer is free to be refilled
	// on the next chunk. Guarded by mu, like everything else here.
	samples []float32
}

func (r *cgoOnlineRecognizer) DecodeAudio(ctx context.Context, sampleRate int, samples []float32) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(samples) == 0 {
		return "", nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.impl == nil {
		return "", fmt.Errorf("speech: sherpa online recognizer is closed")
	}

	stream := sherpa_onnx.NewOnlineStream(r.impl)
	if stream == nil {
		return "", fmt.Errorf("speech: failed to create sherpa online stream")
	}
	defer sherpa_onnx.DeleteOnlineStream(stream)

	stream.AcceptWaveform(sampleRate, samples)
	stream.InputFinished()

	for r.impl.IsReady(stream) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		r.impl.Decode(stream)
	}

	res := r.impl.GetResult(stream)
	if res == nil {
		return "", nil
	}
	return strings.TrimSpace(res.Text), nil
}

func (r *cgoOnlineRecognizer) StartStream(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.impl == nil {
		return fmt.Errorf("speech: sherpa online recognizer is closed")
	}

	if r.activeStream != nil {
		sherpa_onnx.DeleteOnlineStream(r.activeStream)
		r.activeStream = nil
	}

	stream := sherpa_onnx.NewOnlineStream(r.impl)
	if stream == nil {
		return fmt.Errorf("speech: failed to create sherpa online stream")
	}
	r.activeStream = stream
	return nil
}

func (r *cgoOnlineRecognizer) FeedChunk(ctx context.Context, chunk []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(chunk) == 0 {
		return "", nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.impl == nil || r.activeStream == nil {
		return "", fmt.Errorf("speech: online stream is not active")
	}

	numSamples := len(chunk) / 2
	if cap(r.samples) < numSamples {
		r.samples = make([]float32, numSamples)
	}
	samples := r.samples[:numSamples]
	for i := 0; i < numSamples; i++ {
		val := int16(binary.LittleEndian.Uint16(chunk[i*2 : i*2+2]))
		samples[i] = float32(val) / 32768.0
	}

	r.activeStream.AcceptWaveform(16000, samples)
	for r.impl.IsReady(r.activeStream) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		r.impl.Decode(r.activeStream)
	}

	res := r.impl.GetResult(r.activeStream)
	if res == nil {
		return "", nil
	}
	return strings.TrimSpace(res.Text), nil
}

func (r *cgoOnlineRecognizer) StopStream(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.impl == nil || r.activeStream == nil {
		return "", nil
	}
	defer func() {
		if r.activeStream != nil {
			sherpa_onnx.DeleteOnlineStream(r.activeStream)
			r.activeStream = nil
		}
	}()

	r.activeStream.InputFinished()
	for r.impl.IsReady(r.activeStream) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		r.impl.Decode(r.activeStream)
	}

	res := r.impl.GetResult(r.activeStream)
	if res == nil {
		return "", nil
	}
	return strings.TrimSpace(res.Text), nil
}

func (r *cgoOnlineRecognizer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.activeStream != nil {
		sherpa_onnx.DeleteOnlineStream(r.activeStream)
		r.activeStream = nil
	}

	if r.impl != nil {
		sherpa_onnx.DeleteOnlineRecognizer(r.impl)
		r.impl = nil
	}
	return nil
}

func newCGOOfflineRecognizer(_ config.Config, sc SherpaOfflineConfig, _ *slog.Logger) (SherpaRecognizer, error) {
	c := &sherpa_onnx.OfflineRecognizerConfig{
		FeatConfig: sherpa_onnx.FeatureConfig{
			SampleRate: sc.SampleRate,
			FeatureDim: sc.FeatureDim,
		},
		ModelConfig: sherpa_onnx.OfflineModelConfig{
			Transducer: sherpa_onnx.OfflineTransducerModelConfig{
				Encoder: sc.Transducer.Encoder,
				Decoder: sc.Transducer.Decoder,
				Joiner:  sc.Transducer.Joiner,
			},
			Paraformer: sherpa_onnx.OfflineParaformerModelConfig{
				Model: sc.Paraformer.Model,
			},
			NemoCTC: sherpa_onnx.OfflineNemoEncDecCtcModelConfig{
				Model: sc.NemoCTC.Model,
			},
			ZipformerCtc: sherpa_onnx.OfflineZipformerCtcModelConfig{
				Model: sc.ZipformerCTC.Model,
			},
			Moonshine: sherpa_onnx.OfflineMoonshineModelConfig{
				Preprocessor:    sc.Moonshine.Preprocessor,
				Encoder:         sc.Moonshine.Encoder,
				UncachedDecoder: sc.Moonshine.UncachedDecoder,
				CachedDecoder:   sc.Moonshine.CachedDecoder,
				MergedDecoder:   sc.Moonshine.MergedDecoder,
			},
			SenseVoice: sherpa_onnx.OfflineSenseVoiceModelConfig{
				Model:                       sc.SenseVoice.Model,
				Language:                    sc.SenseVoice.Language,
				UseInverseTextNormalization: sc.SenseVoice.UseInverseTextNormalization,
			},
			Whisper: sherpa_onnx.OfflineWhisperModelConfig{
				Encoder:  sc.Whisper.Encoder,
				Decoder:  sc.Whisper.Decoder,
				Language: sc.Whisper.Language,
				Task:     sc.Whisper.Task,
			},
			Canary: sherpa_onnx.OfflineCanaryModelConfig{
				Encoder: sc.Canary.Encoder,
				Decoder: sc.Canary.Decoder,
				SrcLang: sc.Canary.SrcLang,
				TgtLang: sc.Canary.TgtLang,
				UsePnc:  boolToInt(sc.Canary.UsePnc),
			},
			Tokens:     sc.Tokens,
			NumThreads: sc.NumThreads,
			Provider:   sc.Provider,
			// ModelType is deliberately NOT set from sc.ModelType.
			//
			// sherpa-onnx treats a non-empty model_type as an instruction to
			// use that reader, skipping its own detection. mavor's type names
			// are its own vocabulary and mostly are not sherpa's, so passing
			// them forced the wrong reader: a NeMo transducer read by the
			// generic transducer reader failed on a missing vocab_size, and a
			// zipformer CTC read as NeMo CTC failed the same way. Left empty,
			// sherpa-onnx infers the architecture from whichever sub-config
			// above is populated — which the detector now gets right — and
			// every one of these models loads.
			//
			// mavor's own model type still decides which sub-config to fill,
			// so it has not stopped mattering; it just is not sherpa's word.
		},
		DecodingMethod: sc.DecodingMethod,
		MaxActivePaths: sc.MaxActivePaths,
		HotwordsFile:   sc.HotwordsFile,
		HotwordsScore:  sc.HotwordsScore,
		BlankPenalty:   sc.BlankPenalty,
		RuleFsts:       sc.RuleFsts,
		RuleFars:       sc.RuleFars,
	}

	impl := sherpa_onnx.NewOfflineRecognizer(c)
	if impl == nil {
		return nil, fmt.Errorf("speech: failed to initialize sherpa offline recognizer (%s)", sc.ModelType)
	}

	return &cgoOfflineRecognizer{impl: impl}, nil
}

func newCGOOnlineRecognizer(_ config.Config, sc SherpaOnlineConfig, _ *slog.Logger) (SherpaRecognizer, error) {
	c := &sherpa_onnx.OnlineRecognizerConfig{
		FeatConfig: sherpa_onnx.FeatureConfig{
			SampleRate: sc.SampleRate,
			FeatureDim: sc.FeatureDim,
		},
		ModelConfig: sherpa_onnx.OnlineModelConfig{
			Transducer: sherpa_onnx.OnlineTransducerModelConfig{
				Encoder: sc.Transducer.Encoder,
				Decoder: sc.Transducer.Decoder,
				Joiner:  sc.Transducer.Joiner,
			},
			Paraformer: sherpa_onnx.OnlineParaformerModelConfig{
				Encoder: sc.Paraformer.Encoder,
				Decoder: sc.Paraformer.Decoder,
			},
			Zipformer2Ctc: sherpa_onnx.OnlineZipformer2CtcModelConfig{
				Model: sc.ZipformerCTC.Model,
			},
			Tokens:     sc.Tokens,
			NumThreads: sc.NumThreads,
			Provider:   sc.Provider,
			// Left empty for the same reason as the offline recognizer above.
		},
		DecodingMethod: sc.DecodingMethod,
		MaxActivePaths: sc.MaxActivePaths,
		HotwordsFile:   sc.HotwordsFile,
		HotwordsScore:  sc.HotwordsScore,
		BlankPenalty:   sc.BlankPenalty,
		RuleFsts:       sc.RuleFsts,
		RuleFars:       sc.RuleFars,
	}

	impl := sherpa_onnx.NewOnlineRecognizer(c)
	if impl == nil {
		return nil, fmt.Errorf("speech: failed to initialize sherpa online recognizer")
	}

	return &cgoOnlineRecognizer{impl: impl}, nil
}
