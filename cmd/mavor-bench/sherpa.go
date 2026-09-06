package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// sherpaRunner drives the in-process sherpa-onnx recognizers. Its methods run
// inside a worker process rather than the parent (see worker.go), so each one
// measures a single model in a process that loaded nothing else — which is
// what makes the memory figure a real high-water mark and what stops a model
// sherpa-onnx aborts on from ending the sweep.
type sherpaRunner struct {
	modelDir string
	threads  int
}

// config builds the mavor configuration that selects one sherpa model, the
// same way the daemon would. Going through config.Config rather than
// constructing a recognizer directly means the benchmark exercises the model
// resolution and type detection a user actually hits, so a model that mavor
// misclassifies shows up here as a wrong number or a failure rather than
// being quietly bypassed.
func (s sherpaRunner) config(model string) config.Config {
	cfg := config.Default()
	cfg.Model = model
	cfg.Paths.Models = s.modelDir
	cfg.Advanced.Threads = s.threads
	return cfg
}

// batchOnce loads the model, transcribes the whole file in one call, and
// reports wall time split into load and inference. The split matters: a
// 600 MB transducer that loads in two seconds and decodes in eighty
// milliseconds is a very different proposition for a daemon that keeps it
// warm than for a one-shot CLI, and a single total hides that.
//
// This runs in a worker process (see worker.go), so it does not measure
// memory itself — the parent reads the child's peak RSS from getrusage.
func (s sherpaRunner) batchOnce(ctx context.Context, model, wavPath string) (text string, load, infer time.Duration, err error) {
	cfg := s.config(model)
	t, err := speech.NewSherpaTranscriber(cfg, quietLogger())
	if err != nil {
		return "", 0, 0, fmt.Errorf("build transcriber: %w", err)
	}
	defer t.Close()

	loadStart := time.Now()
	if err := t.Start(ctx); err != nil {
		return "", 0, 0, fmt.Errorf("load model: %w", err)
	}
	load = time.Since(loadStart)

	inferStart := time.Now()
	text, err = t.Transcribe(ctx, wavPath)
	infer = time.Since(inferStart)
	if err != nil {
		return "", load, infer, fmt.Errorf("transcribe: %w", err)
	}
	return strings.TrimSpace(text), load, infer, nil
}

// streamChunkMS is how much audio each FeedChunk call carries. 100 ms is a
// realistic dictation cadence — long enough that per-call overhead is not the
// thing being measured, short enough to resolve a time-to-first-token worth
// reporting.
const streamChunkMS = 100

// runStreaming feeds the file in chunks, as the daemon does while you speak,
// and records when the first partial text comes back. This is the only
// measurement that answers "does this model feel live", and it is why
// streaming and batch are separate rows rather than one number per model.
//
// Audio is fed as fast as the recognizer accepts it rather than paced to
// real time: the question is whether the model can keep up with speech, and
// pacing the feed to wall-clock would measure the sleep, not the model.
func (s sherpaRunner) streamOnce(ctx context.Context, model, wavPath string) (text string, load, firstToken, total time.Duration, err error) {
	cfg := s.config(model)
	t, err := speech.NewSherpaTranscriber(cfg, quietLogger())
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("build transcriber: %w", err)
	}
	defer t.Close()

	sampleRate, samples, err := speech.ReadWAVAudio(wavPath)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("read wav: %w", err)
	}

	// Load before the clock starts. Time to first token is meant to answer
	// "does this feel live while I speak", and a daemon has the model warm
	// long before anyone speaks — folding a two-second model load into it
	// would make every streaming model look unusable for a reason the user
	// never experiences. Load is measured, just separately.
	loadStart := time.Now()
	if err := t.Start(ctx); err != nil {
		return "", 0, 0, 0, fmt.Errorf("load model: %w", err)
	}
	load = time.Since(loadStart)

	start := time.Now()
	if err := t.StartStream(ctx); err != nil {
		return "", load, 0, 0, fmt.Errorf("start stream: %w", err)
	}

	samplesPerChunk := sampleRate * streamChunkMS / 1000
	for i := 0; i < len(samples); i += samplesPerChunk {
		end := min(i+samplesPerChunk, len(samples))
		partial, err := t.FeedChunk(ctx, pcm16LE(samples[i:end]))
		if err != nil {
			return "", load, 0, time.Since(start), fmt.Errorf("feed chunk: %w", err)
		}
		if firstToken == 0 && strings.TrimSpace(partial) != "" {
			firstToken = time.Since(start)
		}
	}

	text, err = t.StopStream(ctx)
	total = time.Since(start)
	if err != nil {
		return "", load, firstToken, total, fmt.Errorf("stop stream: %w", err)
	}
	return strings.TrimSpace(text), load, firstToken, total, nil
}

// pcm16LE converts the float samples ReadWAVAudio returns back to the
// little-endian signed 16-bit bytes FeedChunk expects, which is the format
// the recorder produces in production.
func pcm16LE(samples []float32) []byte {
	out := make([]byte, len(samples)*2)
	for i, f := range samples {
		v := int32(f * 32767)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		u := uint16(int16(v))
		out[i*2] = byte(u)
		out[i*2+1] = byte(u >> 8)
	}
	return out
}

// quietLogger discards engine chatter. The recognizers log per-decode at info
// level, and a 24-model sweep would bury the progress output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
