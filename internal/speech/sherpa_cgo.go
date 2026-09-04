//go:build sherpa

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
	samples := make([]float32, numSamples)
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
			Tokens:     sc.Tokens,
			NumThreads: sc.NumThreads,
			Provider:   sc.Provider,
			ModelType:  string(sc.ModelType),
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
			ModelType:  string(sc.ModelType),
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
