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
	previewEnabled    bool
	previewMode       speech.PreviewMode
	companion         speech.StreamTranscriber
	history           TranscriptRecorder
	silenceThreshold  time.Duration
	minPhraseDuration time.Duration

	levelCancel  context.CancelFunc
	levelMu      sync.Mutex
	streamCancel context.CancelFunc

	// streamMu guards every field below it AND the overlay's text. The
	// overlay has one writer — the preview driver, between start and stop
	// (§10.3 of docs/design/configuration-surface.md) — and streamGen is how
	// that is enforced: it moves on when a recording stops, so a result still
	// in flight finds its generation gone and is dropped rather than painted.
	streamMu      sync.Mutex
	streamHistory string
	streamGen     uint64
	streamSource  speech.StreamTranscriber
}

type Config struct {
	Socket        string
	Recorder      audio.Recorder
	Transcriber   speech.Transcriber
	Output        output.Dispatcher
	Overlay       overlay.Overlay
	Ducker        audio.Ducker
	Logger        *slog.Logger
	ErrorDuration time.Duration

	// PreviewEnabled shows partial text in the overlay while the user
	// speaks. It never emits output: the transcript is typed once, when
	// transcription completes.
	PreviewEnabled bool

	// PreviewMode is where that text comes from, decided once at daemon
	// start by speech.ResolvePreview. The empty value means "read the main
	// model's partials if it has any, otherwise phrase mode", which is what
	// a caller that never resolved a preview gets.
	PreviewMode speech.PreviewMode

	// PreviewCompanion is the small streaming recognizer loaded alongside
	// the main model, read only when PreviewMode is speech.PreviewCompanion.
	// It paints the overlay and nothing else: it never reaches the output
	// emitter and never contributes to the final transcript.
	PreviewCompanion speech.StreamTranscriber

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
		previewEnabled:    c.PreviewEnabled,
		previewMode:       c.PreviewMode,
		companion:         c.PreviewCompanion,
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
	if closer, ok := d.companion.(io.Closer); ok {
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
	if !d.previewEnabled {
		d.logger.Info("streaming: preview disabled (preview.enabled = false)")
		return
	}
	src := d.previewStreamSource()

	d.streamMu.Lock()
	if d.streamCancel != nil {
		d.streamCancel()
	}
	ctxStream, cancel := context.WithCancel(ctx)
	d.streamCancel = cancel
	d.streamHistory = ""
	d.streamGen++
	gen := d.streamGen
	d.streamSource = src
	d.streamMu.Unlock()

	if src != nil {
		d.runStreamPreview(ctxStream, gen, src)
		return
	}
	d.runPhrasePreview(ctxStream, gen)
}

// previewStreamSource is the recognizer that will paint the overlay, or nil
// for phrase mode. Which one it is was decided at daemon start by
// speech.ResolvePreview, from the catalog and from what is installed; this
// only reads off the value that mode names.
func (d *Daemon) previewStreamSource() speech.StreamTranscriber {
	switch d.previewMode {
	case speech.PreviewCompanion:
		if d.companion == nil {
			d.logger.Warn("streaming: preview mode is companion but no companion was loaded — using phrase mode")
			return nil
		}
		return d.companion
	case speech.PreviewPhrases:
		return nil
	}
	// speech.PreviewMainModel, and the empty mode a caller that never
	// resolved a preview leaves behind: read the main model's own partials
	// when it has any, and fall back to phrases when it has not.
	if st, ok := d.transcriber.(speech.StreamTranscriber); ok {
		return st
	}
	return nil
}

// runStreamPreview feeds captured audio to a recognizer that decodes
// incrementally — the main model when it can do that, otherwise the companion
// loaded alongside it — and paints what it emits.
func (d *Daemon) runStreamPreview(ctx context.Context, gen uint64, src speech.StreamTranscriber) {
	d.logger.Info("streaming: initializing transducer stream session")
	if err := src.StartStream(ctx); err != nil {
		d.logger.Warn("streaming: start stream failed", "err", err)
		return
	}

	go func() {
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cr, ok := d.recorder.(audio.ChunkReader)
				if !ok {
					continue
				}
				chunk, err := cr.ReadChunk()
				if err != nil {
					d.logger.Debug("streaming: read chunk failed", "err", err)
					continue
				}
				if len(chunk) == 0 {
					continue
				}
				partial, err := src.FeedChunk(ctx, chunk)
				if err != nil {
					d.logger.Warn("streaming: feed chunk failed", "err", err)
					continue
				}
				if partial != "" {
					d.setPreview(ctx, gen, partial)
				}
			}
		}
	}()
}

// runPhrasePreview is the fallback of §6.2: no second model, so on a pause the
// audio since the last pause is transcribed with the MAIN model and appended
// to the preview. It is what runs when the main model does not decode
// incrementally and no companion is available — cheaper, slower, and prone to
// filling silence with words that were not said.
func (d *Daemon) runPhrasePreview(ctx context.Context, gen uint64) {
	d.logger.Info("streaming: initializing phrase-mode preview",
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
			case <-ctx.Done():
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
					continue
				}
				if speechFrames*30 < int(d.minPhraseDuration.Milliseconds()) {
					continue
				}
				silenceFrames++
				if time.Duration(silenceFrames*30)*time.Millisecond < d.silenceThreshold {
					continue
				}

				phrase := accumulatedSamples
				accumulatedSamples = nil
				speechFrames = 0
				silenceFrames = 0
				go d.transcribePhrase(ctx, gen, phrase)
			}
		}
	}()
}

// transcribePhrase decodes one phrase with the main model for the preview.
// A failure drops that phrase and nothing else: it must never abort the
// recording or the final transcript (§10.2).
func (d *Daemon) transcribePhrase(ctx context.Context, gen uint64, phrase []int16) {
	if len(phrase) == 0 {
		return
	}
	tempWav := filepath.Join(os.TempDir(), fmt.Sprintf("mavor-phrase-%d.wav", time.Now().UnixNano()))
	if err := audio.WriteWAV(tempWav, phrase, audio.DefaultSampleRate); err != nil {
		d.logger.Warn("streaming: write phrase WAV failed", "err", err)
		return
	}
	defer os.Remove(tempWav)

	txt, err := d.transcriber.Transcribe(ctx, tempWav)
	if err != nil {
		d.logger.Warn("streaming: transcribe phrase failed", "err", err)
		return
	}
	d.appendPhrase(ctx, gen, txt)
}

// appendPhrase adds one phrase-mode result to the preview. On stop all preview
// work is cancelled and its results discarded (§10.3), and a transcription
// still in flight is precisely the result that can outlive its recording: it
// is dropped here, not appended.
func (d *Daemon) appendPhrase(ctx context.Context, gen uint64, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	if !d.previewLive(ctx, gen) {
		d.logger.Debug("streaming: phrase result arrived after stop — dropped", "text", text)
		return
	}
	if d.streamHistory != "" {
		d.streamHistory += " "
	}
	d.streamHistory += text
	d.logger.Info("streaming: recognized phrase", "text", text)
	if d.overlay != nil {
		_ = d.overlay.SetText(d.streamHistory)
	}
}

// setPreview paints preview text for one recording, dropping anything that
// arrives after that recording stopped. Both preview mechanisms write the
// overlay through here, which is what makes the driver its one writer.
func (d *Daemon) setPreview(ctx context.Context, gen uint64, text string) {
	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	if !d.previewLive(ctx, gen) {
		d.logger.Debug("streaming: partial arrived after stop — dropped", "text", text)
		return
	}
	if d.overlay != nil {
		_ = d.overlay.SetText(text)
	}
}

// previewLive reports whether the recording that produced a preview result is
// still the current one. Callers hold streamMu.
func (d *Daemon) previewLive(ctx context.Context, gen uint64) bool {
	return gen == d.streamGen && ctx.Err() == nil
}

func (d *Daemon) stopStreamingMonitoring() {
	d.streamMu.Lock()
	if d.streamCancel != nil {
		d.streamCancel()
		d.streamCancel = nil
	}
	src := d.streamSource
	d.streamSource = nil
	// Moving the generation on under the same lock the writers take is what
	// discards the work still in flight: a phrase transcription or a partial
	// that lands after this finds its generation gone and paints nothing.
	d.streamGen++
	d.streamHistory = ""
	if d.overlay != nil {
		_ = d.overlay.SetText("")
	}
	d.streamMu.Unlock()

	if src != nil {
		if finalText, err := src.StopStream(context.Background()); err == nil && finalText != "" {
			// Logged and dropped. The preview never contributes to the
			// transcript, and a companion never influences it at all — the
			// text the user gets comes from the main model, once, in
			// runTranscription.
			d.logger.Info("streaming: final preview text discarded", "text", finalText)
		}
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
