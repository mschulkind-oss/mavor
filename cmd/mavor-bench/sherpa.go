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

// streamChunkMS is how much audio each FeedChunk call carries: the daemon's
// preview tick, so this measures the cadence the model is actually driven at.
//
// It was 100 ms, chosen so that per-call overhead would not be "the thing
// being measured". That reasoning had the question backwards. The daemon
// drives the preview from a 30 ms ticker (internal/daemon/daemon.go), so
// per-call overhead at 30 ms is not noise around the measurement — it is part
// of what the user waits for, and a model whose overhead only becomes visible
// below 100 ms would have looked fine here and felt slow in use. The report
// also told the reader these chunks were "the way the daemon does", which was
// simply untrue while the two numbers disagreed.
//
// Keep this equal to that ticker. If the daemon's tick changes, this changes.
const streamChunkMS = 30

// streamCadence describes how the preview text arrived rather than when it
// started. Time to first token answers "did anything appear"; nothing in this
// harness used to answer "and did it then keep coming".
//
// It had to. The default preview companion was changed to a more accurate
// model, the report scored it well on first token, and a user reported within
// a day that the preview "comes in chunks rather than continuously" — a
// regression the benchmark had no column for. These are that column.
type streamCadence struct {
	// Updates is the number of FeedChunk calls whose text differed from the
	// previous one, i.e. the number of times the overlay would repaint.
	Updates int

	// MeanGapMS and MaxGapMS are distances in AUDIO time — position in the
	// stream — not wall clock. See cadenceTracker for why that is the only
	// honest unit here.
	MeanGapMS float64
	MaxGapMS  float64

	// SlowestChunkMS is the one wall-clock figure, because it is the one that
	// stalls the daemon: FeedChunk runs on the preview goroutine between
	// 30 ms ticks, so a call longer than a tick means captured audio queues up
	// behind the recognizer.
	SlowestChunkMS float64

	// SlowChunks counts how many calls overran that tick. JSON only: the
	// slowest call is what predicts a felt stall, but one slow call is a
	// hiccup and four hundred is a model that can never keep up, and only
	// whoever is diagnosing needs to tell those apart.
	SlowChunks int
}

// cadenceTracker accumulates the cadence figures one chunk at a time. It is a
// separate type from the feed loop so the arithmetic can be tested without a
// 600 MB model on disk — the gap accounting has three edge cases (no updates
// at all, the wait before the first one, the silence after the last one) and
// getting any of them wrong yields a plausible-looking number.
//
// Gaps are measured in audio time because the harness deliberately feeds as
// fast as the recognizer accepts (see streamOnce). A wall-clock gap would
// therefore report how fast this CPU is, not how this model behaves, and
// would shrink on a faster machine while the user's experience of lumpiness
// stayed identical.
type cadenceTracker struct {
	tickMS float64

	prevText  string
	seenFirst bool
	lastPosMS float64

	gapSum float64
	gapMax float64
	cad    streamCadence
}

func newCadenceTracker() *cadenceTracker {
	return &cadenceTracker{tickMS: streamChunkMS}
}

// observe records one FeedChunk call: posMS is how far into the audio the
// stream now is, text is what came back, and callMS is how long the call took
// on the wall clock.
func (c *cadenceTracker) observe(posMS float64, text string, callMS float64) {
	if callMS > c.cad.SlowestChunkMS {
		c.cad.SlowestChunkMS = callMS
	}
	if callMS > c.tickMS {
		c.cad.SlowChunks++
	}

	text = strings.TrimSpace(text)
	if text == c.prevText {
		return
	}
	// A change back to empty counts: the recognizer clearing its hypothesis
	// is a repaint the user sees, and skipping it would credit a model for
	// text that vanished.
	c.cad.Updates++
	if c.seenFirst {
		gap := posMS - c.lastPosMS
		c.gapSum += gap
		if gap > c.gapMax {
			c.gapMax = gap
		}
	}
	c.seenFirst = true
	c.lastPosMS = posMS
	c.prevText = text
}

// finish closes the last gap against the end of the audio and returns the
// figures. audioMS is the length of the stream that was fed.
//
// The wait BEFORE the first update is excluded — that is time to first token,
// which has its own column, and folding it in here would report the same
// latency twice. The silence AFTER the last update is included, and that is
// deliberate: a model that emits a few partials early, goes quiet, and
// delivers everything from StopStream is precisely the lumpy preview this
// measures, and it would otherwise score a flawless mean and max. On a
// fixture with trailing silence this adds the same constant to every model,
// so the rows stay comparable with each other.
func (c *cadenceTracker) finish(audioMS float64) streamCadence {
	if !c.seenFirst {
		return c.cad
	}
	if tail := audioMS - c.lastPosMS; tail > 0 {
		c.gapSum += tail
		if tail > c.gapMax {
			c.gapMax = tail
		}
	}
	c.cad.MaxGapMS = c.gapMax
	c.cad.MeanGapMS = c.gapSum / float64(c.cad.Updates)
	return c.cad
}

// streamResult is one streaming run. It is a struct rather than a fifth and
// sixth return value because the cadence figures travel with the timings
// everywhere they go, and a six-value signature is where a caller starts
// mixing up two adjacent time.Durations.
type streamResult struct {
	Text       string
	Load       time.Duration
	FirstToken time.Duration
	Total      time.Duration
	Cadence    streamCadence
}

// streamOnce feeds the file in chunks, as the daemon does while you speak,
// and records both when the first partial text comes back and how steadily
// the text kept coming after that. This is the only measurement that answers
// "does this model feel live", and it is why streaming and batch are separate
// rows rather than one number per model.
//
// Audio is fed as fast as the recognizer accepts it rather than paced to
// real time: the question is whether the model can keep up with speech, and
// pacing the feed to wall-clock would measure the sleep, not the model. That
// is also why the gap figures are in audio time — see cadenceTracker.
func (s sherpaRunner) streamOnce(ctx context.Context, model, wavPath string) (streamResult, error) {
	var res streamResult

	cfg := s.config(model)
	t, err := speech.NewSherpaTranscriber(cfg, quietLogger())
	if err != nil {
		return res, fmt.Errorf("build transcriber: %w", err)
	}
	defer t.Close()

	sampleRate, samples, err := speech.ReadWAVAudio(wavPath)
	if err != nil {
		return res, fmt.Errorf("read wav: %w", err)
	}

	// Load before the clock starts. Time to first token is meant to answer
	// "does this feel live while I speak", and a daemon has the model warm
	// long before anyone speaks — folding a two-second model load into it
	// would make every streaming model look unusable for a reason the user
	// never experiences. Load is measured, just separately.
	loadStart := time.Now()
	if err := t.Start(ctx); err != nil {
		return res, fmt.Errorf("load model: %w", err)
	}
	res.Load = time.Since(loadStart)

	start := time.Now()
	if err := t.StartStream(ctx); err != nil {
		return res, fmt.Errorf("start stream: %w", err)
	}

	tracker := newCadenceTracker()
	samplesPerChunk := sampleRate * streamChunkMS / 1000
	for i := 0; i < len(samples); i += samplesPerChunk {
		end := min(i+samplesPerChunk, len(samples))

		callStart := time.Now()
		partial, err := t.FeedChunk(ctx, pcm16LE(samples[i:end]))
		call := time.Since(callStart)
		if err != nil {
			res.Total = time.Since(start)
			return res, fmt.Errorf("feed chunk: %w", err)
		}

		// Position in the audio, computed from samples actually fed rather
		// than from the chunk index, so the short final chunk does not push
		// every gap out by a few milliseconds.
		posMS := float64(end) / float64(sampleRate) * 1000
		tracker.observe(posMS, partial, float64(call)/float64(time.Millisecond))

		if res.FirstToken == 0 && strings.TrimSpace(partial) != "" {
			res.FirstToken = time.Since(start)
		}
	}
	res.Cadence = tracker.finish(float64(len(samples)) / float64(sampleRate) * 1000)

	text, err := t.StopStream(ctx)
	res.Total = time.Since(start)
	if err != nil {
		return res, fmt.Errorf("stop stream: %w", err)
	}
	res.Text = strings.TrimSpace(text)
	return res, nil
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
