// Package daemon wires the FSM, audio recorder, transcriber, output
// dispatcher, overlay, and IPC server into a single long-lived process. It
// is the seam between the small interface packages and `cmd/mavor`.
//
// The daemon does not embed a specific Overlay implementation; callers pass
// one in (Noop in tests, the wlr-layer-shell overlay in production). That
// keeps the daemon's tests fast and free of cgo.
//
// Architecture and invariants: docs/reference/how-mavor-works.md
package daemon

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/ipc"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
	"github.com/mschulkind-oss/mavor/internal/state"
)

type Daemon struct {
	socket            string
	machine           *state.Machine
	recorder          audio.Recorder
	transcriber       speech.Transcriber
	output            output.Dispatcher
	overlay           overlay.Overlay
	ducker            audio.Ducker
	logger            *slog.Logger
	errorDuration     time.Duration
	streamingStrategy string
	mode              string
	history           TranscriptRecorder
	silenceThreshold  time.Duration
	minPhraseDuration time.Duration

	levelCancel   context.CancelFunc
	levelMu       sync.Mutex
	streamCancel  context.CancelFunc
	streamMu      sync.Mutex
	streamHistory string
}

type Config struct {
	Socket            string
	Recorder          audio.Recorder
	Transcriber       speech.Transcriber
	Output            output.Dispatcher
	Overlay           overlay.Overlay
	Ducker            audio.Ducker
	Logger            *slog.Logger
	ErrorDuration     time.Duration
	StreamingStrategy string
	Mode              string
	History           TranscriptRecorder
	SilenceThreshold  time.Duration
	MinPhraseDuration time.Duration
}

func New(c Config) *Daemon {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	errDur := c.ErrorDuration
	if errDur == 0 {
		errDur = 1500 * time.Millisecond
	}
	ducker := c.Ducker
	if ducker == nil {
		ducker = &audio.NoopDucker{}
	}
	silenceThresh := c.SilenceThreshold
	if silenceThresh == 0 {
		silenceThresh = 450 * time.Millisecond
	}
	minPhrase := c.MinPhraseDuration
	if minPhrase == 0 {
		minPhrase = 600 * time.Millisecond
	}
	mode := c.Mode
	if mode == "" {
		mode = "streaming"
	}
	strat := c.StreamingStrategy
	if strat == "" {
		strat = "auto"
	}
	return &Daemon{
		socket:            c.Socket,
		machine:           state.New(),
		recorder:          c.Recorder,
		transcriber:       c.Transcriber,
		output:            c.Output,
		overlay:           c.Overlay,
		ducker:            ducker,
		logger:            c.Logger,
		errorDuration:     errDur,
		streamingStrategy: strat,
		mode:              mode,
		history:           c.History,
		silenceThreshold:  silenceThresh,
		minPhraseDuration: minPhrase,
	}
}

// Run blocks serving IPC and reacting to state changes until ctx is
// cancelled. It is the daemon's main loop.
func (d *Daemon) Run(ctx context.Context) error {
	// Subscribe the side-effect handler BEFORE the IPC server starts so the
	// first toggle can't race the listener registration.
	wg := &sync.WaitGroup{}
	unsub := d.machine.Subscribe(func(s state.State) {
		d.onTransition(ctx, s, wg)
	})
	defer unsub()

	srv := ipc.NewServer(d.socket, d.handleRequest)
	err := srv.Serve(ctx)

	// Wait for any in-flight transcription pipeline before returning so we
	// don't leave a goroutine writing to wl-copy after the binary exits.
	wg.Wait()
	d.stopLevelMonitoring()
	d.stopStreamingMonitoring()
	_ = d.ducker.Restore()
	_ = d.overlay.Close()
	if closer, ok := d.transcriber.(io.Closer); ok {
		_ = closer.Close()
	}
	return err
}

func (d *Daemon) handleRequest(req ipc.Request) ipc.Response {
	d.logger.Info("ipc: request", "action", req.Action, "state_before", d.machine.State())
	switch req.Action {
	case "toggle":
		next, changed := d.machine.Apply(state.EventToggle)
		d.logger.Info("ipc: toggle result", "state_after", next, "changed", changed)
		return ipc.Response{State: next.String()}
	case "start":
		next, changed := d.machine.Apply(state.EventRecordStart)
		d.logger.Info("ipc: start result", "state_after", next, "changed", changed)
		return ipc.Response{State: next.String()}
	case "stop":
		next, changed := d.machine.Apply(state.EventRecordStop)
		d.logger.Info("ipc: stop result", "state_after", next, "changed", changed)
		return ipc.Response{State: next.String()}
	case "status":
		return ipc.Response{State: d.machine.State().String()}
	default:
		d.logger.Warn("ipc: unknown action", "action", req.Action)
		return ipc.Response{Error: fmt.Sprintf("unknown action %q", req.Action)}
	}
}

func (d *Daemon) reportError(reason string, err error) {
	d.logger.Error(reason, "err", err)
	if d.overlay != nil {
		_ = d.overlay.Show(overlay.Error)
	}
	if d.errorDuration > 0 {
		time.Sleep(d.errorDuration)
	}
	d.machine.Apply(state.EventTranscribeFailed)
}

// onTransition is the side-effect dispatcher. It runs OFF the FSM lock so
// it's free to call back into Apply (e.g. EventTranscribeDone).
func (d *Daemon) onTransition(ctx context.Context, s state.State, wg *sync.WaitGroup) {
	d.logger.Info("state: transitioned", "to", s)
	if err := d.overlay.Show(visualFor(s)); err != nil {
		d.logger.Warn("overlay show failed", "state", s, "err", err)
	} else {
		d.logger.Info("overlay: shown", "state", s, "visual", visualFor(s))
	}
	switch s {
	case state.Idle:
		d.stopLevelMonitoring()
		d.stopStreamingMonitoring()
		if err := d.ducker.Restore(); err != nil {
			d.logger.Warn("ducking: restore failed", "err", err)
		} else {
			d.logger.Info("ducking: volume restored")
		}
	case state.Recording:
		if err := d.ducker.Duck(); err != nil {
			d.logger.Warn("ducking: duck failed", "err", err)
		} else {
			d.logger.Info("ducking: volume ducked")
		}
		d.logger.Info("pipeline: starting recorder")
		if err := d.recorder.Start(ctx); err != nil {
			d.reportError("pipeline: recorder start failed — returning to idle", err)
			return
		}
		d.startLevelMonitoring(ctx)
		d.startStreamingMonitoring(ctx)
	case state.Transcribing:
		d.stopLevelMonitoring()
		d.stopStreamingMonitoring()
		// Capture is finished by now — only the tail is being decoded — so
		// bring background media back up rather than holding it down for the
		// length of the transcription.
		if err := d.ducker.Restore(); err != nil {
			d.logger.Warn("ducking: restore failed", "err", err)
		} else {
			d.logger.Info("ducking: volume restored")
		}
		d.logger.Info("pipeline: spawning transcription goroutine")
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.runTranscription(ctx)
		}()
	}
}

func (d *Daemon) startLevelMonitoring(ctx context.Context) {
	d.levelMu.Lock()
	if d.levelCancel != nil {
		d.levelCancel()
	}
	ctxLevel, cancel := context.WithCancel(ctx)
	d.levelCancel = cancel
	d.levelMu.Unlock()

	go func() {
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctxLevel.Done():
				return
			case <-ticker.C:
				if d.recorder != nil && d.overlay != nil {
					_ = d.overlay.SetLevel(d.recorder.Level())
				}
			}
		}
	}()
}

func (d *Daemon) stopLevelMonitoring() {
	d.levelMu.Lock()
	if d.levelCancel != nil {
		d.levelCancel()
		d.levelCancel = nil
	}
	d.levelMu.Unlock()
	if d.overlay != nil {
		_ = d.overlay.SetLevel(0.0)
	}
}

// startStreamingMonitoring drives the overlay's live text preview while the
// user speaks. It never emits output: partial results are provisional, and the
// cleaned-up transcript is inserted once, when transcription completes.
func (d *Daemon) startStreamingMonitoring(ctx context.Context) {
	if d.mode == "batch" {
		d.logger.Info("streaming: preview disabled (mode=batch)")
		return
	}
	st, isStreaming := d.transcriber.(speech.StreamTranscriber)

	d.streamMu.Lock()
	if d.streamCancel != nil {
		d.streamCancel()
	}
	ctxStream, cancel := context.WithCancel(ctx)
	d.streamCancel = cancel
	d.streamHistory = ""
	d.streamMu.Unlock()

	if isStreaming {
		d.logger.Info("streaming: initializing transducer stream session")
		if err := st.StartStream(ctxStream); err != nil {
			d.logger.Warn("streaming: start stream failed", "err", err)
			return
		}

		go func() {
			ticker := time.NewTicker(30 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctxStream.Done():
					return
				case <-ticker.C:
					if cr, ok := d.recorder.(audio.ChunkReader); ok {
						chunk, err := cr.ReadChunk()
						if err != nil {
							d.logger.Debug("streaming: read chunk failed", "err", err)
							continue
						}
						if len(chunk) > 0 {
							partial, err := st.FeedChunk(ctxStream, chunk)
							if err != nil {
								d.logger.Warn("streaming: feed chunk failed", "err", err)
								continue
							}
							if partial != "" && d.overlay != nil {
								_ = d.overlay.SetText(partial)
							}
						}
					}
				}
			}
		}()
		return
	}

	// VAD-Segmented Batch Streaming (e.g. Whisper Server or CLI)
	if d.streamingStrategy == "vad_batch" || d.streamingStrategy == "auto" {
		d.logger.Info("streaming: initializing VAD-segmented batch streaming",
			"silence_threshold", d.silenceThreshold,
			"min_phrase", d.minPhraseDuration)

		go func() {
			ticker := time.NewTicker(30 * time.Millisecond)
			defer ticker.Stop()

			var accumulatedSamples []int16
			var speechFrames int
			var silenceFrames int

			for {
				select {
				case <-ctxStream.Done():
					return
				case <-ticker.C:
					cr, ok := d.recorder.(audio.ChunkReader)
					if !ok {
						continue
					}
					chunk, err := cr.ReadChunk()
					if err != nil || len(chunk) == 0 {
						continue
					}

					sampleCount := len(chunk) / 2
					frameSamples := make([]int16, sampleCount)
					for i := 0; i < sampleCount; i++ {
						frameSamples[i] = int16(binary.LittleEndian.Uint16(chunk[i*2 : i*2+2]))
					}
					accumulatedSamples = append(accumulatedSamples, frameSamples...)

					rms := audio.CalculateRMS(frameSamples)
					if rms >= audio.SpeechRMSThreshold {
						speechFrames++
						silenceFrames = 0
					} else if speechFrames*30 >= int(d.minPhraseDuration.Milliseconds()) {
						silenceFrames++
						if time.Duration(silenceFrames*30)*time.Millisecond >= d.silenceThreshold {
							phrase := accumulatedSamples
							accumulatedSamples = nil
							speechFrames = 0
							silenceFrames = 0

							go func(p []int16) {
								if len(p) == 0 {
									return
								}
								tempWav := filepath.Join(os.TempDir(), fmt.Sprintf("mavor-slice-%d.wav", time.Now().UnixNano()))
								if err := audio.WriteWAV(tempWav, p, audio.DefaultSampleRate); err != nil {
									d.logger.Warn("streaming: write slice WAV failed", "err", err)
									return
								}
								defer os.Remove(tempWav)

								txt, err := d.transcriber.Transcribe(ctxStream, tempWav)
								if err != nil {
									d.logger.Warn("streaming: transcribe slice failed", "err", err)
									return
								}
								txt = strings.TrimSpace(txt)
								if txt != "" {
									d.streamMu.Lock()
									if d.streamHistory != "" {
										d.streamHistory += " "
									}
									d.streamHistory += txt
									d.streamMu.Unlock()

									d.logger.Info("streaming: recognized phrase slice", "text", txt)
									if d.overlay != nil {
										d.streamMu.Lock()
										preview := d.streamHistory
										d.streamMu.Unlock()
										_ = d.overlay.SetText(preview)
									}
								}
							}(phrase)
						}
					}
				}
			}
		}()
	}
}

func (d *Daemon) stopStreamingMonitoring() {
	d.streamMu.Lock()
	if d.streamCancel != nil {
		d.streamCancel()
		d.streamCancel = nil
	}
	d.streamMu.Unlock()

	if st, ok := d.transcriber.(speech.StreamTranscriber); ok {
		if finalText, err := st.StopStream(context.Background()); err == nil && finalText != "" {
			d.logger.Info("streaming: final stream text", "text", finalText)
		}
	}
	if d.overlay != nil {
		_ = d.overlay.SetText("")
	}
}

func (d *Daemon) runTranscription(ctx context.Context) {
	d.logger.Info("pipeline: stopping recorder for transcription")
	wav, err := d.recorder.Stop()
	if err != nil {
		d.reportError("pipeline: recorder.Stop failed — aborting", err)
		return
	}
	if wav != "" {
		defer func() {
			_ = os.Remove(wav)
			_ = os.Remove(wav + ".txt")
		}()
	}
	d.logger.Info("pipeline: recorder.Stop ok", "wav", wav)

	// VAD Silence Pre-Filter: if audio has no detectable speech, skip Whisper
	if wav != "" {
		if hasSpeech, vadErr := audio.DetectSpeech(wav, 150*time.Millisecond); vadErr == nil && !hasSpeech {
			d.logger.Info("pipeline: silence detected by VAD pre-filter — skipping transcription")
			d.machine.Apply(state.EventTranscribeDone)
			return
		}
	}

	text, err := d.transcriber.Transcribe(ctx, wav)
	if err != nil {
		d.reportError("pipeline: transcribe failed — aborting", err)
		return
	}
	d.logger.Info("pipeline: transcript received", "text_len", len(text))
	if text == "" {
		d.logger.Warn("pipeline: empty transcript — skipping emit (whisper found no speech?)")
		d.machine.Apply(state.EventTranscribeDone)
		return
	}
	// Record before typing: a failed or mis-targeted emit is exactly when the
	// user needs the transcript back, so it must already be on disk.
	if d.history != nil {
		if err := d.history.Append(history.Entry{Text: text, At: time.Now()}); err != nil {
			d.logger.Warn("history: append failed", "err", err)
		}
	}

	d.logger.Info("pipeline: dispatching output")
	if err := d.output.Emit(ctx, text); err != nil {
		// We still complete the FSM transition; the user already heard
		// themselves and clipboard fallback may have worked.
		d.logger.Warn("pipeline: output dispatch reported error (continuing)", "err", err)
	} else {
		d.logger.Info("pipeline: output dispatch ok")
	}
	d.machine.Apply(state.EventTranscribeDone)
	d.logger.Info("pipeline: cycle complete (back to idle)")
}

func visualFor(s state.State) overlay.Visual {
	switch s {
	case state.Recording:
		return overlay.Recording
	case state.Transcribing:
		return overlay.Transcribing
	}
	return overlay.Hidden
}

// TranscriptRecorder persists completed transcripts for later recovery. It is
// an interface so the daemon can be tested without touching the filesystem.
type TranscriptRecorder interface {
	Append(history.Entry) error
}

// ErrNotRunning is returned by `mavor toggle` when the daemon socket isn't
// reachable. cmd/mavor translates this to an actionable user message.
var ErrNotRunning = errors.New("daemon: not running (socket unreachable)")
