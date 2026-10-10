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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/ipc"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
	"github.com/mschulkind-oss/mavor/internal/state"
)

type Daemon struct {
	// recording is the single active capture, or nil. Cycles that have stopped
	// but not yet emitted live in the machine's Pending queue and the decode
	// worker's channel.
	recording *dictationCycle
	// decodeQueue feeds the one decode worker. A cycle is enqueued when its
	// capture has stopped and its WAV is on disk.
	decodeQueue chan *dictationCycle
	// dispatcher emits finished transcripts in spoken order.
	dispatcher *dispatcher

	provenanceMu              sync.Mutex
	warningWG                 sync.WaitGroup
	source, warning           string
	startupError              string
	warningUntil              time.Time
	warningGen                uint64
	initialize                func(context.Context) (Initialized, error)
	mainModel, companionModel string
	finalMode                 models.FinalMode
	finalCycle                *incrementalCycle

	// runCtx is the daemon's lifetime context, captured in Run so IPC control
	// requests can drive capture and transcription. Set before the server
	// accepts its first request.
	runCtx            context.Context
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
	silenceFilter     bool
	silenceThreshold  time.Duration
	minPhraseDuration time.Duration
	binaryPath        string
	upgradeInterval   time.Duration

	// controlMu keeps concurrent IPC control requests from advancing the FSM
	// while the previous transition is still starting or stopping capture.
	// Status requests do not take this lock so they remain responsive.
	controlMu sync.Mutex

	levelCancel  context.CancelFunc
	levelMu      sync.Mutex
	streamCancel context.CancelFunc

	// residencyProbe answers where the main model's GPU allocations live. It is
	// nil when there is nothing GPU-resident to ask about — a sherpa model runs
	// in-process on the CPU, a subprocess reloads per utterance, and a remote
	// server's model is on another machine — and it is never called from the
	// dictation path. Guarded by residencyMu, with the last reading it gave.
	residencyProbe func() (speech.Residency, speech.GPUMemory)
	residencyMu    sync.Mutex
	residency      speech.Residency
	residencyMem   speech.GPUMemory
	// residencyNote is the last string handed to the overlay, so a poll that
	// found no change does not repaint the strip.
	residencyNote string
	// residencyKick asks the sampler for a reading now. Buffered and written
	// without blocking: a reading already pending is the same reading.
	residencyKick chan struct{}

	// streamMu guards every field below it AND the overlay's text. There is one
	// live recording at a time, so the priming and feed machinery is
	// daemon-global; the per-recording facts a stopped cycle still needs are
	// copied onto its dictationCycle. liveID names the recording whose preview
	// may paint: it is the one-writer guard (§10.3 of
	// docs/design/configuration-surface.md), cleared when capture stops so a
	// result still in flight finds its id gone and is dropped rather than
	// painted.
	streamMu       sync.Mutex
	streamHistory  string
	liveID         uint64
	streamSource   speech.StreamTranscriber
	streamFeedDone chan struct{}

	// streamDrain is the most recently stopped recording's preview drain, kept
	// so the next recording can await it before reusing a recognizer. The cycle
	// being decoded reads its own drain from the cycle.
	streamDrain chan struct{}

	// backup is the companion stream finalizing this recording, if the preview
	// source is the companion. Only one recording uses it at a time: a new
	// recording whose earlier cycle still has a preview drain open skips its
	// preview (see startRecording), so a second companion stream never starts
	// while the first is still transcribing. Guarded by backupMu.
	backupMu sync.Mutex
	backup   *BackupCycle

	// lastStack dedups overlay ShowStack calls. The derived stack can move
	// Transcribing -> Recording -> Transcribing across stacked cycles, and a
	// repaint of the same rows would restart the pill's strips for no reason.
	// Guarded by visualMu, which is held across the paint: the dedup decision
	// and the ShowStack it authorizes are one critical section, or two painters
	// could record their stacks in one order and paint in the other.
	visualMu  sync.Mutex
	lastStack []overlay.Visual
}

type Initialized struct {
	Transcriber               speech.Transcriber
	PreviewMode               speech.PreviewMode
	PreviewCompanion          speech.StreamTranscriber
	MainModel, CompanionModel string
	// Residency reports where the main model's GPU allocations currently live,
	// or nil when there is nothing GPU-resident to track. The daemon samples it
	// on a timer while recording and reports the same fact in its status reply;
	// see speech.Residency.
	Residency func() (speech.Residency, speech.GPUMemory)
}

type Config struct {
	Initialize func(context.Context) (Initialized, error)
	FinalMode  models.FinalMode

	Socket        string
	Recorder      audio.Recorder
	Transcriber   speech.Transcriber
	Output        output.Dispatcher
	Overlay       overlay.Overlay
	Ducker        audio.Ducker
	Logger        *slog.Logger
	ErrorDuration time.Duration

	// PreviewEnabled shows provisional partial text in the overlay. Only a
	// finalized, completely covered companion may substitute for a qualified
	// GPU-enabled server request failure; partials never emit.
	PreviewEnabled bool

	// PreviewMode is where that text comes from, decided once at daemon
	// start by speech.ResolvePreview. The empty value means "read the main
	// model's partials if it has any, otherwise phrase mode", which is what
	// a caller that never resolved a preview gets.
	PreviewMode speech.PreviewMode

	// PreviewCompanion is the small streaming recognizer loaded alongside
	// the main model, read only when PreviewMode is speech.PreviewCompanion.
	// It paints provisional text; BackupCycle may finalize it for the narrowly
	// qualified request-failure exception. Normal main success always wins.
	PreviewCompanion speech.StreamTranscriber

	// SilenceFilter opts into the final-recording energy check. Preview
	// recognition can override rejection; false always sends audio to the model.
	SilenceFilter bool

	History TranscriptRecorder
	// SilenceThreshold controls preview phrase pauses, not final rejection.
	SilenceThreshold  time.Duration
	MinPhraseDuration time.Duration

	// BinaryPath is the file the daemon watches for a package upgrade,
	// exiting when it changes so a supervisor starts the new version. Empty
	// — the default, and what every test uses — disables the watch.
	//
	// It has to be a path that survives the upgrade rather than the running
	// executable, which is the file being replaced. See ErrBinaryReplaced.
	BinaryPath string

	// UpgradeCheckInterval is how often BinaryPath is stat'd, defaulting to
	// defaultUpgradeInterval.
	UpgradeCheckInterval time.Duration
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
	upgradeInterval := c.UpgradeCheckInterval
	if upgradeInterval <= 0 {
		upgradeInterval = defaultUpgradeInterval
	}
	return &Daemon{
		initialize:        c.Initialize,
		socket:            c.Socket,
		finalMode:         c.FinalMode,
		machine:           state.New(),
		decodeQueue:       make(chan *dictationCycle, state.MaxPending),
		dispatcher:        newDispatcher(),
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
		silenceFilter:     c.SilenceFilter,
		silenceThreshold:  silenceThresh,
		minPhraseDuration: minPhrase,
		binaryPath:        c.BinaryPath,
		upgradeInterval:   upgradeInterval,
		residencyKick:     make(chan struct{}, 1),
	}
}

// Run blocks serving IPC and reacting to state changes until ctx is
// cancelled. It is the daemon's main loop.
func (d *Daemon) Run(ctx context.Context) error {
	// The daemon's own cancel, so the upgrade watch can end the loop the way
	// a SIGTERM would rather than needing a second path out of Serve.
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	d.runCtx = ctx

	var replaced atomic.Bool
	if d.binaryPath != "" {
		go d.watchBinary(ctx, &replaced, stop)
	}

	// One residency sampler for the daemon's life, not one per dictation: the
	// probe reads the child's own /proc entries and the answer changes when the
	// model loads or when something else takes the card, which is not a
	// per-utterance fact.
	go d.watchResidency(ctx)

	// One decode worker and one ordered dispatcher for the daemon's life. The
	// worker decodes cycles one at a time; the dispatcher owns output, so a slow
	// Emit never stalls recognition of the next dictation.
	wg := &sync.WaitGroup{}
	wg.Add(2)
	go func() { defer wg.Done(); d.decodeWorker(ctx) }()
	go func() { defer wg.Done(); d.runDispatcher(ctx) }()

	var initWG sync.WaitGroup
	var initErr error
	if d.initialize != nil {
		d.machine.Apply(state.EventInitialize)
	}
	srv := ipc.NewServer(d.socket, d.handleRequest)
	err := srv.ServeReady(ctx, func() {
		if d.initialize == nil {
			return
		}
		initWG.Add(1)
		go func() {
			defer initWG.Done()
			// No budget on initialization. A model load is bounded by the disk and
			// the device it lands on, not by a number this code can pick: a 3 GB
			// read that takes 14 seconds on an idle machine takes minutes on a
			// saturated one, and the same model on the same machine does both. A
			// deadline here converts a slow load into a failed startup, and the
			// restart that follows re-reads the model from the resource that was
			// too slow — the failure feeds itself. See
			// Supervisor.waitForReady, which carries the same reasoning.
			//
			// Waiting is not unbounded in practice. A child that dies is a real
			// failure and still fails the daemon; a shutdown still cancels this
			// through ctx; and a user whose models are still loading sees the
			// initializing state and can restart the service themselves. That is
			// the escape hatch, and it does not fire by itself.
			d.syncHUD(ctx)
			loaded, e := d.initialize(ctx)
			if e == nil && loaded.Transcriber == nil {
				e = errors.New("initialization returned no main transcriber")
			}
			d.controlMu.Lock()
			if e == nil {
				e = ctx.Err()
			}
			if e == nil {
				d.transcriber = loaded.Transcriber
				d.companion = loaded.PreviewCompanion
				d.previewMode = loaded.PreviewMode
				d.mainModel = loaded.MainModel
				d.companionModel = loaded.CompanionModel
				d.setResidencyProbe(loaded.Residency)
				d.machine.Apply(state.EventReady)
				d.controlMu.Unlock()
				d.showDerived()
				// The model has just loaded: one reading now, so the status
				// reply and `mavor doctor` start from the truth rather than
				// from unknown until the first tick.
				d.kickResidency()
				return
			}
			closeInitialized(loaded)
			initErr = e
			d.provenanceMu.Lock()
			d.startupError = e.Error()
			d.provenanceMu.Unlock()
			if ctx.Err() == nil {
				d.machine.Apply(state.EventInitializeFailed)
				d.showError(e.Error())
				d.controlMu.Unlock()
				d.syncHUD(ctx)
				timer := time.NewTimer(d.errorDuration)
				select {
				case <-timer.C:
				case <-ctx.Done():
				}
				timer.Stop()
			} else {
				d.controlMu.Unlock()
			}
			stop()
		}()
	})
	stop()
	initWG.Wait()
	if initErr != nil {
		err = initErr
	}

	// Wait for any in-flight decode or output before returning so we don't
	// leave a goroutine writing to wl-copy after the binary exits.
	if d.incrementalFinal() {
		d.cancelFinalCycle()
	}
	if d.machine.Snapshot().Recording() {
		if wav, e := d.recorder.Stop(); e == nil {
			removeCapture(wav)
		}
	}
	wg.Wait()
	d.warningWG.Wait()
	d.stopLevelMonitoring()
	d.stopStreamingMonitoring(nil)
	// Join the preview drain before closing the recognizer. stopStreamingMonitoring
	// schedules src.StopStream on its own goroutine; with the preview on, src is
	// the transcriber itself, so closing it without this wait races Close against
	// a StopStream still decoding the tail. context.Background() is deliberate:
	// the run context is already cancelled and must not cut the join short.
	d.awaitStreamDrain(context.Background())
	d.cancelBackup()
	// Cycles the worker never picked up are abandoned; drop their capture files
	// rather than leaking them in the recording directory.
	for {
		select {
		case c := <-d.decodeQueue:
			removeCapture(c.wav)
			continue
		default:
		}
		break
	}
	// An error here now means the device would not confirm the restore —
	// i.e. it may still be muted — which is worth a log line on the way out
	// rather than silence. Ducking is keyed on an active capture, so restore
	// exactly when one was in progress.
	if d.machine.Snapshot().Recording() {
		if err := d.ducker.Restore(); err != nil {
			d.logger.Warn("ducking: restore on shutdown failed — background audio may still be ducked", "err", err)
		}
	}
	_ = d.overlay.Close()
	if closer, ok := d.transcriber.(io.Closer); ok {
		_ = closer.Close()
	}
	if closer, ok := d.companion.(io.Closer); ok {
		_ = closer.Close()
	}
	if err == nil && replaced.Load() {
		return ErrBinaryReplaced
	}
	return err
}

func (d *Daemon) handleRequest(req ipc.Request) (resp ipc.Response) {
	defer func() {
		snap := d.machine.Snapshot()
		if resp.State == "" {
			resp.State = snap.State().String()
		}
		resp.Recording = snap.RecordingID != 0
		resp.Transcribing = len(snap.Pending)
		resp.Source, resp.Warning = d.statusProvenance()
		resp.Residency = d.statusResidency()
		if resp.State == "failed" {
			d.provenanceMu.Lock()
			resp.Error = d.startupError
			d.provenanceMu.Unlock()
		}
	}()
	if req.Action == "toggle" || req.Action == "start" || req.Action == "stop" {
		d.controlMu.Lock()
		defer d.controlMu.Unlock()
	}
	// The blocked request owns presentation, not recording intent. Readiness and
	// failure use the same control lock, so this cannot repaint a finished load.
	if (req.Action == "start" || req.Action == "toggle") && d.machine.State() == state.Initializing {
		d.showAndSetText(overlay.Initializing, "Models still initializing; cannot record yet. Press again after ready.")
		return
	}
	d.logger.Info("ipc: request", "action", req.Action, "state_before", d.machine.State())
	switch req.Action {
	case "toggle":
		if d.machine.Snapshot().Recording() {
			resp.State = d.stopRecording().String()
		} else {
			resp.State = d.startRecording().String()
		}
	case "start":
		resp.State = d.startRecording().String()
	case "stop":
		resp.State = d.stopRecording().String()
	case "status":
		// resp.State is filled from the live snapshot by the deferred read.
	default:
		d.logger.Warn("ipc: unknown action", "action", req.Action)
		resp.Error = fmt.Sprintf("unknown action %q", req.Action)
	}
	return resp
}

// === overlay painting (one writer under visualMu) ===

// showDerived paints the ordered pill stack the current snapshot implies,
// unless a completed warning is still being held on screen. It skips a repaint
// of the same stack and logs nothing for it.
func (d *Daemon) showDerived() {
	snap := d.machine.Snapshot()
	d.provenanceMu.Lock()
	keepWarning := snap.State() == state.Idle && time.Now().Before(d.warningUntil)
	d.provenanceMu.Unlock()
	if keepWarning {
		return
	}
	d.showStack(overlayStack(snap))
}

// overlayStack derives the ordered pill rows a snapshot implies: the recording
// row first when a capture is live, then one transcribing row per stopped cycle
// awaiting output, oldest first. The overlay reserves height for three rows and
// collapses a longer stack into a "+N" count, so the daemon passes every row.
func overlayStack(snap state.Snapshot) []overlay.Visual {
	switch snap.Phase {
	case state.PhaseInitializing:
		return []overlay.Visual{overlay.Initializing}
	case state.PhaseFailed:
		return []overlay.Visual{overlay.Error}
	}
	pills := make([]overlay.Visual, 0, 1+len(snap.Pending))
	if snap.RecordingID != 0 {
		pills = append(pills, overlay.Recording)
	}
	for range snap.Pending {
		pills = append(pills, overlay.Transcribing)
	}
	if len(pills) == 0 {
		pills = append(pills, overlay.Hidden)
	}
	return pills
}

// showStack paints an ordered pill stack unless it is already the visible one.
// It is the one funnel for derived repaints, so it also clears a diagnostic
// subtitle when the last error or backup row leaves the stack.
//
// visualMu is held across the paint. The dedup and the ShowStack it authorizes
// must be one critical section: if two painters each recorded their stack and
// then painted in the other order, the on-screen rows would disagree with
// lastStack, and the repaint that would fix it is the one the dedup skips — a
// stale ERROR or BACKUP pill outliving errorDuration. Both backends' ShowStack
// only take their own lock and post a frame, so holding this across it never
// blocks on the compositor.
func (d *Daemon) showStack(pills []overlay.Visual) {
	if d.overlay == nil {
		return
	}
	d.visualMu.Lock()
	defer d.visualMu.Unlock()
	if sameStack(d.lastStack, pills) {
		return
	}
	diagnosticCleared := hasDiagnosticRow(d.lastStack) && !hasDiagnosticRow(pills)
	d.lastStack = append([]overlay.Visual(nil), pills...)
	if err := d.overlay.ShowStack(pills); err != nil {
		d.logger.Warn("overlay show failed", "pills", pills, "err", err)
		return
	}
	if diagnosticCleared {
		_ = d.overlay.SetText("")
	}
	d.logger.Info("overlay: shown", "pills", pills)
	d.kickResidency()
}

// showStackAndSetText repaints a stack with a subtitle regardless of the
// dedup, for the states where the text is the message. It shares visualMu with
// showStack so the stack and its subtitle are painted as one step, never
// interleaved with another painter.
func (d *Daemon) showStackAndSetText(pills []overlay.Visual, text string) {
	if d.overlay == nil {
		return
	}
	d.visualMu.Lock()
	defer d.visualMu.Unlock()
	d.lastStack = append([]overlay.Visual(nil), pills...)
	_ = d.overlay.ShowStack(pills)
	_ = d.overlay.SetText(text)
	d.kickResidency()
}

// showAndSetText repaints a single pill with a subtitle, for the demand-for-
// initialization notice and initialization failure where the text is the
// message.
func (d *Daemon) showAndSetText(v overlay.Visual, text string) {
	d.showStackAndSetText([]overlay.Visual{v}, text)
}

// showCycleDiagnostic paints a cycle's failure or backup warning alongside the
// other live rows rather than instead of them (§7.6): the recording row stays
// first when a capture is live, the diagnostic takes the failed cycle's row,
// and any other transcribing rows keep their place.
func (d *Daemon) showCycleDiagnostic(v overlay.Visual, cycleID uint64, text string) {
	d.showStackAndSetText(d.diagnosticStack(v, cycleID), text)
}

// diagnosticStack is overlayStack with one stopped cycle's row replaced by a
// diagnostic pill. A cycle already retired from the machine (an aborted capture
// never entered Pending) contributes its diagnostic as a fresh top row.
func (d *Daemon) diagnosticStack(v overlay.Visual, cycleID uint64) []overlay.Visual {
	snap := d.machine.Snapshot()
	pills := make([]overlay.Visual, 0, 1+len(snap.Pending))
	if snap.RecordingID != 0 {
		pills = append(pills, overlay.Recording)
	}
	placed := false
	for _, id := range snap.Pending {
		if id == cycleID && !placed {
			pills = append(pills, v)
			placed = true
			continue
		}
		pills = append(pills, overlay.Transcribing)
	}
	if !placed {
		pills = append([]overlay.Visual{v}, pills...)
	}
	return pills
}

// sameStack reports whether two pill stacks are identical.
func sameStack(a, b []overlay.Visual) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// hasDiagnosticRow reports whether a stack carries an error or backup-warning
// pill, the rows whose subtitle is a message rather than a live preview.
func hasDiagnosticRow(pills []overlay.Visual) bool {
	for _, v := range pills {
		if v == overlay.Error || v == overlay.Degraded {
			return true
		}
	}
	return false
}

func (d *Daemon) showError(text string) {
	d.logger.Error("pipeline error", "err", text)
	d.showAndSetText(overlay.Error, text)
}

// startRecording opens the recording slot. It runs under controlMu: capture is
// single-instance, so a start never interleaves with another start or a stop.
func (d *Daemon) startRecording() state.State {
	ctx := d.runCtx
	// Live-final modes do not stack (§8, ruling OQ-3): with streaming/segments
	// the main recognizer is still finishing the previous cycle, so a start
	// while one is transcribing stays today's no-op.
	if !d.stackingAllowed() && len(d.machine.Snapshot().Pending) > 0 {
		d.logger.Info("pipeline: start refused — live-final mode does not stack")
		return d.machine.State()
	}
	id, snap, changed := d.machine.RecordStart()
	if !changed {
		d.logger.Info("ipc: start result", "state_after", d.machine.State(), "changed", false)
		return d.machine.State()
	}
	cycle := &dictationCycle{id: id}
	d.recording = cycle
	d.dispatcher.register(id)
	d.provenanceMu.Lock()
	d.source, d.warning = "", ""
	d.warningUntil = time.Time{}
	d.warningGen++
	d.provenanceMu.Unlock()
	d.streamMu.Lock()
	d.streamHistory = ""
	d.streamMu.Unlock()
	// The snapshot now names this recording, so the derived stack paints the
	// recording row ahead of any earlier cycle still awaiting output.
	d.showDerived()
	if err := d.ducker.Duck(); err != nil {
		d.logger.Warn("ducking: duck failed", "err", err)
	} else {
		d.logger.Info("ducking: volume ducked")
	}
	d.logger.Info("pipeline: starting recorder", "cycle", id)
	if err := d.recorder.Start(ctx); err != nil {
		d.abortCycle(ctx, cycle, "pipeline: recorder start failed — returning to idle", err)
		return d.machine.State()
	}
	d.startLevelMonitoring(ctx)
	// A later recording gets a live preview only when no earlier cycle is still
	// awaiting output. That is the ruled feel (§5.5, OQ-6): capture is never
	// delayed for a preview, and the single recognizer is never fed a new stream
	// while an earlier cycle's final decode still holds it.
	if len(snap.Pending) > 0 {
		d.logger.Info("preview: skipped for this recording — an earlier dictation is still transcribing", "cycle", id)
		return d.machine.State()
	}
	if d.incrementalFinal() {
		if err := d.startFinalCycle(ctx, cycle); err != nil {
			if wav, e := d.recorder.Stop(); e == nil {
				removeCapture(wav)
			}
			d.abortCycle(ctx, cycle, "pipeline: final session start failed", err)
			return d.machine.State()
		}
	} else {
		d.startStreamingMonitoring(ctx, cycle)
	}
	return d.machine.State()
}

// stopRecording closes the recording slot, stops capture inline, and enqueues
// the cycle for the decode worker. Running under controlMu is what serializes
// the recorder hand-off: the next Start cannot run until Stop has returned.
func (d *Daemon) stopRecording() state.State {
	cycle := d.recording
	id, snap, changed := d.machine.RecordStop()
	if !changed {
		d.logger.Info("ipc: stop result", "state_after", d.machine.State(), "changed", false)
		return d.machine.State()
	}
	d.recording = nil
	d.stopLevelMonitoring()
	if d.incrementalFinal() {
		d.stopFinalPump()
	} else {
		d.stopStreamingMonitoring(cycle)
	}
	// Capture stop is part of this edge: the WAV is on disk before the cycle is
	// queued, so a new recording can never interleave Start/Stop.
	wav, err := d.recorder.Stop()
	// Capture is finished — only the tail is being decoded — so bring background
	// media back up rather than holding it down for the transcription. This is
	// the ruled feel (OQ-5): restored on key release, on every stop path, even
	// one whose WAV did not reap cleanly.
	if rerr := d.ducker.Restore(); rerr != nil {
		d.logger.Warn("ducking: restore failed", "err", rerr)
	} else {
		d.logger.Info("ducking: volume restored")
	}
	if err != nil {
		d.reportCycleError(d.runCtx, cycle, "pipeline: recorder.Stop failed — aborting", err)
		// The cycle is in Pending but produced no audio; completing it with an
		// empty outcome advances spoken order without typing anything.
		d.completeCycle(cycle, &dispatchOutcome{source: "main", model: d.mainModel}, true)
		return snap.State()
	}
	cycle.wav = wav
	d.showDerived()
	d.logger.Info("pipeline: queueing cycle for transcription", "cycle", id, "pending", len(snap.Pending))
	select {
	case d.decodeQueue <- cycle:
	case <-d.runCtx.Done():
	}
	return snap.State()
}

// abortCycle discards a recording that failed before it produced audio and
// returns the machine to where it was. The error pill lingers for errorDuration
// via the dispatcher's rerender, and the capture duck is undone because a
// recording was opened before the failure.
func (d *Daemon) abortCycle(ctx context.Context, cycle *dictationCycle, reason string, err error) {
	d.reportCycleError(ctx, cycle, reason, err)
	d.machine.AbortRecording()
	d.recording = nil
	if rerr := d.ducker.Restore(); rerr != nil {
		d.logger.Warn("ducking: restore failed", "err", rerr)
	}
	d.dispatcher.complete(cycle.id, &dispatchOutcome{keepError: true})
}

// reportCycleError paints a failure and records it as the daemon's last
// warning. The cycle still advances spoken order (the caller completes it), so
// one silent dictation cannot wedge the queue.
func (d *Daemon) reportCycleError(_ context.Context, cycle *dictationCycle, reason string, err error) {
	d.provenanceMu.Lock()
	d.warning = err.Error()
	d.provenanceMu.Unlock()
	d.logger.Error(reason, "err", err, "cycle", cycle.id)
	d.showCycleDiagnostic(overlay.Error, cycle.id, err.Error())
}

// decodeWorker is the single decode path: cycles are recognized one at a time,
// in the order their captures stopped.
func (d *Daemon) decodeWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case cycle := <-d.decodeQueue:
			d.decodeCycle(ctx, cycle)
		}
	}
}

// runDispatcher emits finished transcripts in spoken order, one at a time,
// separate from the decode worker so a slow Emit never stalls recognition. It
// retires each cycle from the machine only after its output is out, so Idle
// means everything has been typed, not merely decoded.
func (d *Daemon) runDispatcher(ctx context.Context) {
	for {
		if id, out, ok := d.dispatcher.pop(); ok {
			d.emitOutcome(ctx, out)
			d.machine.Finish(id)
			if out.keepError {
				d.scheduleRerender(ctx, d.errorDuration)
			} else {
				d.showDerived()
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-d.dispatcher.wake:
		}
	}
}

func (d *Daemon) emitOutcome(ctx context.Context, out *dispatchOutcome) {
	if out.text == "" {
		return
	}
	// Record before typing: a failed or mis-targeted emit is exactly when the
	// user needs the transcript back, so it must already be on disk — and in the
	// same order as Emit, so history and what was typed agree.
	if d.history != nil {
		if err := d.history.Append(history.Entry{Text: out.text, At: time.Now(), Source: out.source, Model: out.model, Warning: out.warning}); err != nil {
			d.logger.Warn("history: append failed", "err", err)
		}
	}
	d.logger.Info("pipeline: dispatching output")
	emitStart := time.Now()
	if err := d.output.Emit(ctx, out.text); err != nil {
		// We still complete the FSM transition; the user already heard
		// themselves and clipboard fallback may have worked.
		d.logger.Warn("pipeline: output dispatch reported error (continuing)", "err", err)
	} else {
		d.logger.Info("pipeline: output dispatch ok")
	}
	d.provenanceMu.Lock()
	d.source = out.source
	d.warning = out.warning
	d.provenanceMu.Unlock()
	if out.warning != "" {
		d.retainWarning(ctx)
	}
	var charsPerSec int64
	if emitMS := time.Since(emitStart).Milliseconds(); emitMS > 0 {
		charsPerSec = int64(len(out.text)) * 1000 / emitMS
	}
	d.logger.Info("pipeline: emit complete", "chars", len(out.text), "chars_per_sec", charsPerSec)
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

// residencyPollInterval is how often the daemon re-checks where the main
// model's weights live.
//
// Residency changes when the model loads or when another process puts the card
// under pressure, not between frames, so a second is far finer than the fact
// requires. Each sample is a bounded read of the child's own fdinfo entries: it
// never touches the GPU and it never waits on the child.
const residencyPollInterval = time.Second

// residencyGTTNotice is the overlay's one line for an evicted model. It says
// what changed rather than what to do about it: the strip is one short line
// under the pill and a remedy would neither fit nor be read at a glance.
const residencyGTTNotice = "GPU memory full — model in system RAM"

// watchResidency keeps the daemon's picture of the main model's GPU residency
// current for as long as it runs. It is ONE goroutine with a ticker rather than
// one per dictation, and it is the only writer of the overlay's residency note.
func (d *Daemon) watchResidency(ctx context.Context) {
	ticker := time.NewTicker(residencyPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.residencyKick:
		case <-ticker.C:
		}
		d.sampleResidency()
	}
}

// kickResidency asks for a reading now rather than at the next tick, so the
// note is decided with the change that made it relevant. It never blocks: a
// reading already pending is the same reading.
func (d *Daemon) kickResidency() {
	select {
	case d.residencyKick <- struct{}{}:
	default:
	}
}

// setResidencyProbe publishes the model's probe once, when initialization hands
// over the transcriber it belongs to.
func (d *Daemon) setResidencyProbe(p func() (speech.Residency, speech.GPUMemory)) {
	d.residencyMu.Lock()
	d.residencyProbe = p
	d.residencyMu.Unlock()
}

// sampleResidency takes one reading and paints it if it says something new.
// Every failure mode of the probe arrives here as ResidencyUnknown, which
// clears the note; nothing in this path returns an error, blocks, or holds a
// lock the FSM or the audio path needs.
func (d *Daemon) sampleResidency() {
	d.residencyMu.Lock()
	probe := d.residencyProbe
	d.residencyMu.Unlock()
	if probe == nil {
		return
	}
	res, mem := probe()
	note := residencyNotice(d.machine.State() == state.Recording, res)

	d.residencyMu.Lock()
	residencyChanged := res != d.residency
	noteChanged := note != d.residencyNote
	d.residency, d.residencyMem, d.residencyNote = res, mem, note
	d.residencyMu.Unlock()

	if residencyChanged {
		// Once per change, not per poll: the numbers are here for the one
		// question a mismatch would raise — what did the probe actually see.
		d.logger.Info("residency: main model GPU residency changed",
			"residency", res, "vram_bytes", mem.VRAM, "gtt_bytes", mem.GTT,
			"evicted_vram_bytes", mem.Evicted, "device", mem.Device)
	}
	if noteChanged && d.overlay != nil {
		_ = d.overlay.SetNotice(note)
	}
}

// residencyNotice is the overlay's line for a residency, or "" when there is
// nothing to say: nothing is recording, or the weights are where they belong
// (or where the probe cannot tell).
func residencyNotice(recording bool, r speech.Residency) string {
	if recording && r == speech.ResidencyGTT {
		return residencyGTTNotice
	}
	return ""
}

// statusResidency names where the main model's weights live, for the daemon's
// status reply and the `mavor doctor` line built from it. Unknown is reported
// as absent: there is nothing a caller can do with it.
func (d *Daemon) statusResidency() string {
	d.residencyMu.Lock()
	defer d.residencyMu.Unlock()
	if d.residency == speech.ResidencyUnknown {
		return ""
	}
	return d.residency.String()
}

// startStreamingMonitoring drives the overlay's live text preview while the
// user speaks. It never emits output: partial results are provisional, and the
// cleaned-up transcript is inserted once, when transcription completes.
func (d *Daemon) startStreamingMonitoring(ctx context.Context, c *dictationCycle) {
	if !d.previewEnabled && d.companion == nil {
		d.logger.Info("streaming: preview disabled (preview.enabled = false)")
		return
	}
	src := d.previewStreamSource()
	// The previous recording's final decode may still be running; it holds the
	// same recognizer this is about to restart. Capture asks for a preview only
	// when no earlier cycle is pending, so this is normally already closed —
	// but awaiting it here is the backstop against feeding a recognizer that is
	// still finishing the last stream.
	d.awaitStreamDrain(ctx)

	d.streamMu.Lock()
	if d.streamCancel != nil {
		d.streamCancel()
	}
	ctxStream, cancel := context.WithCancel(ctx)
	d.streamCancel = cancel
	d.streamHistory = ""
	d.liveID = c.id
	c.hadSpeech = false
	d.streamSource = src
	d.streamFeedDone = make(chan struct{})
	c.usesMain = d.previewMode != speech.PreviewCompanion
	d.streamMu.Unlock()

	if d.previewMode == speech.PreviewCompanion && d.companion != nil {
		d.streamMu.Lock()
		d.streamSource = nil
		c.usesMain = false
		d.streamMu.Unlock()
		d.startBackup(ctx, c)
		d.runBackupPump(ctxStream)
		return
	}
	if src != nil {
		d.runStreamPreview(ctxStream, c, src)
		return
	}
	d.runPhrasePreview(ctxStream, c)
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

// PreviewTick is how often the daemon hands the preview recognizer another
// slice of captured audio while the user is speaking.
//
// It is exported and named because it is not a private implementation detail:
// it is the cadence a streaming model is actually driven at, so it is the
// cadence a streaming model has to be measured at. cmd/mavor-bench fed 100 ms
// chunks for a while and described them in the report as "the way the daemon
// does" — a model whose per-call overhead only shows below 100 ms scored well
// there and would have felt slow here. A test now pins the two together.
const PreviewTick = 30 * time.Millisecond

// runStreamPreview feeds captured audio to a recognizer that decodes
// incrementally — the main model when it can do that, otherwise the companion
// loaded alongside it — and paints what it emits.
func (d *Daemon) runStreamPreview(ctx context.Context, c *dictationCycle, src speech.StreamTranscriber) {
	d.logger.Info("streaming: initializing transducer stream session")
	if err := src.StartStream(ctx); err != nil {
		close(d.streamFeedDone)
		d.logger.Warn("streaming: start stream failed", "err", err)
		return
	}

	cr, ok := d.recorder.(audio.ChunkReader)
	if !ok {
		// The recorder cannot hand out audio as it arrives, so a streaming
		// preview has nothing to read. Said once, loudly, rather than
		// discovered as a preview that never appears.
		d.logger.Warn("streaming: recorder cannot supply live audio — no preview will appear",
			"recorder", fmt.Sprintf("%T", d.recorder))
		close(d.streamFeedDone)
		return
	}

	done := d.streamFeedDone
	go func() {
		defer close(done)
		ticker := time.NewTicker(PreviewTick)
		defer ticker.Stop()

		// A preview that silently never appears is the failure this whole
		// path is prone to: every way of getting nothing — no audio, an
		// offline recognizer, a model that needs more speech — looks
		// identical from the outside. So count what happens and say so once.
		var fed, gotText int
		var warned bool
		began := time.Now()
		for {
			select {
			case <-ctx.Done():
				if fed > 0 && gotText == 0 {
					d.logger.Warn("streaming: fed audio to the preview model but it returned no text",
						"chunks", fed, "preview_mode", d.previewMode)
				}
				return
			case <-ticker.C:
				chunk, err := cr.ReadChunk()
				if err != nil {
					d.logger.Debug("streaming: read chunk failed", "err", err)
					continue
				}
				if len(chunk) == 0 {
					// Two seconds of ticks with no bytes means the
					// recording is not growing, which is a broken preview
					// however good the model is. Checked in this goroutine
					// rather than a timer, so `fed` needs no lock.
					if !warned && fed == 0 && time.Since(began) > 2*time.Second {
						warned = true
						d.logger.Warn("streaming: no audio has reached the preview in 2s — the recording is not growing")
					}
					continue
				}
				fed++
				feedStart := time.Now()
				partial, err := src.FeedChunk(ctx, chunk)
				if err != nil {
					d.logger.Warn("streaming: feed chunk failed", "err", err)
					continue
				}
				// Per tick, so debug only. bytes says whether audio is
				// keeping up; chars says how far the preview has grown,
				// which is what drives the overlay's width; feed_ms says
				// whether the recognizer is the thing falling behind.
				d.logger.Debug("streaming: chunk fed",
					"bytes", len(chunk), "chars", len(partial),
					"feed_ms", time.Since(feedStart).Milliseconds(), "chunk_n", fed)
				partial = soften(partial)
				if partial != "" {
					if gotText == 0 {
						d.logger.Info("streaming: preview is producing text", "after_chunks", fed)
					}
					gotText++
					d.setPreview(ctx, c, partial)
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
func (d *Daemon) runPhrasePreview(ctx context.Context, c *dictationCycle) {
	d.logger.Info("streaming: initializing phrase-mode preview",
		"silence_threshold", d.silenceThreshold,
		"min_phrase", d.minPhraseDuration)

	done := d.streamFeedDone
	go func() {
		var phrases sync.WaitGroup
		defer func() { phrases.Wait(); close(done) }()
		ticker := time.NewTicker(PreviewTick)
		defer ticker.Stop()

		phrase := newPhraseBuffer()
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

				rms := audio.CalculateRMS(phrase.add(chunk))
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

				speechFrames = 0
				silenceFrames = 0
				pcm := phrase.take()
				phrases.Add(1)
				go func() { defer phrases.Done(); d.transcribePhrase(ctx, c, pcm) }()
			}
		}
	}()
}

// transcribePhrase decodes one phrase with the main model for the preview.
// A failure drops that phrase and nothing else: it must never abort the
// recording or the final transcript (§10.2).
func (d *Daemon) transcribePhrase(ctx context.Context, c *dictationCycle, phrase []int16) {
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
	d.appendPhrase(ctx, c, txt)
}

// appendPhrase adds one phrase-mode result to the preview. On stop all preview
// work is cancelled and its results discarded (§10.3), and a transcription
// still in flight is precisely the result that can outlive its recording: it
// is dropped here, not appended.
func (d *Daemon) appendPhrase(ctx context.Context, c *dictationCycle, text string) {
	// Phrase mode re-transcribes with the main model, so it produces the same
	// non-speech annotations the final transcript does — a pause mid-sentence
	// paints "[BLANK_AUDIO]" across the HUD. Cut them here, before the empty
	// check, so a phrase that was nothing but markers adds nothing.
	text = strings.TrimSpace(speech.StripNonSpeech(text))
	if text == "" {
		return
	}

	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	if !d.previewLive(ctx, c) {
		d.logger.Debug("streaming: phrase result arrived after stop — dropped", "text", text)
		return
	}
	c.hadSpeech = true
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
func (d *Daemon) setPreview(ctx context.Context, c *dictationCycle, text string) {
	// Partials read off the main model carry its annotations too.
	text = speech.StripNonSpeech(text)
	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	if !d.previewLive(ctx, c) {
		d.logger.Debug("streaming: partial arrived after stop — dropped", "text", text)
		return
	}
	if strings.TrimSpace(text) != "" {
		c.hadSpeech = true
	}
	if d.overlay != nil {
		_ = d.overlay.SetText(text)
	}
}

// previewLive reports whether the recording that produced a preview result is
// still the current one. Callers hold streamMu.
func (d *Daemon) previewLive(ctx context.Context, c *dictationCycle) bool {
	return c != nil && c.id == d.liveID && ctx.Err() == nil
}

// stopStreamingMonitoring cancels the live preview. c is the cycle that
// stopped, whose own drain and recognition evidence the final decode reads; it
// may be nil on shutdown, when there is no cycle to record them on.
func (d *Daemon) stopStreamingMonitoring(c *dictationCycle) {
	d.provenanceMu.Lock()
	keepDiagnostic := d.machine.State() == state.Failed || time.Now().Before(d.warningUntil)
	d.provenanceMu.Unlock()
	d.streamMu.Lock()
	if d.streamCancel != nil {
		d.streamCancel()
		d.streamCancel = nil
	}
	src := d.streamSource
	done := d.streamFeedDone
	d.streamFeedDone = nil
	d.streamSource = nil
	// Moving the live id on under the same lock the writers take is what
	// discards the work still in flight: a phrase transcription or a partial
	// that lands after this finds its recording no longer live and paints
	// nothing.
	d.liveID = 0
	d.streamHistory = ""
	if d.overlay != nil && !keepDiagnostic {
		_ = d.overlay.SetText("")
	}
	drain := make(chan struct{})
	if src != nil || done != nil {
		d.streamDrain = drain
		if c != nil {
			c.drain = drain
		}
	}
	d.streamMu.Unlock()

	if src == nil && done == nil {
		return
	}
	if backup := d.currentBackup(); backup != nil && src == nil {
		if done != nil {
			<-done
		}
		close(drain)
		return
	}

	// StopStream makes the recognizer decode whatever audio it still holds.
	// Its text is never emitted, but recognition can prevent the energy
	// filter from discarding a quiet recording. It runs here rather than
	// synchronously on the control path so a slow final decode never delays the
	// `mavor stop` reply or the capture hand-off.
	go func() {
		defer close(drain)
		if done != nil {
			<-done
		}
		if src == nil {
			return
		}
		started := time.Now()
		finalText, err := src.StopStream(context.Background())
		if err == nil && finalText != "" {
			if strings.TrimSpace(speech.StripNonSpeech(finalText)) != "" {
				d.streamMu.Lock()
				if c != nil {
					c.hadSpeech = true
				}
				d.streamMu.Unlock()
			}
			d.logger.Info("streaming: final preview text discarded",
				"text", finalText, "drain_took", time.Since(started))
		}
	}()
}

// awaitStreamDrain blocks until the most recently stopped recording's
// StopStream has returned, so a recognizer is never fed a new stream while it
// is still finishing the last one. It is normally already closed.
func (d *Daemon) awaitStreamDrain(ctx context.Context) {
	d.streamMu.Lock()
	drain := d.streamDrain
	d.streamMu.Unlock()
	if drain == nil {
		return
	}
	select {
	case <-drain:
	case <-ctx.Done():
		return
	}
}

// awaitCycleDrain blocks until this cycle's preview decode has returned.
func (d *Daemon) awaitCycleDrain(ctx context.Context, c *dictationCycle) {
	d.streamMu.Lock()
	var drain chan struct{}
	if c != nil {
		drain = c.drain
	}
	d.streamMu.Unlock()
	if drain == nil {
		return
	}
	select {
	case <-drain:
	case <-ctx.Done():
		return
	}
}

// previewRecognizedSpeech checks recognition evidence before rejecting a quiet
// recording. Once any words were recognized, later empty partials cannot erase
// that evidence. Only a recording the energy filter would otherwise discard
// needs to wait for the tail decode, and only if it has no recognized words yet.
func (d *Daemon) previewRecognizedSpeech(ctx context.Context, c *dictationCycle) bool {
	d.streamMu.Lock()
	var hadSpeech bool
	var drain chan struct{}
	if c != nil {
		hadSpeech, drain = c.hadSpeech, c.drain
	}
	d.streamMu.Unlock()
	if hadSpeech {
		return true
	}
	if drain != nil {
		select {
		case <-drain:
		case <-ctx.Done():
			return false
		}
	}
	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	if c == nil {
		return false
	}
	return c.hadSpeech
}

// decodeCycle recognizes one stopped recording and hands the result to the
// dispatcher. It runs on the single decode worker, so cycles are recognized in
// spoken order; the dispatcher, not this function, owns output.
func (d *Daemon) decodeCycle(ctx context.Context, c *dictationCycle) {
	cycleStart := time.Now()
	wav := c.wav
	out := &dispatchOutcome{source: "main", model: d.mainModel}
	if wav == "" {
		d.completeCycle(c, out, false)
		return
	}
	defer removeCapture(wav)

	// The companion backup finalizes this recording. There is one at a time: a
	// recording whose earlier cycle had a preview drain open skipped its
	// preview, so d.backup belongs to this cycle alone.
	type completedBackup struct {
		result BackupResult
		err    error
	}
	var backupDone chan completedBackup
	if b := d.takeBackup(); b != nil {
		backupDone = make(chan completedBackup, 1)
		// The companion's finalization is this cycle's drain: the energy filter
		// waits on it for recognition evidence, and a later recording would wait
		// on it before reusing the companion. Re-pointing the cycle's drain here
		// is what makes the finalized tail count.
		drain := make(chan struct{})
		d.streamMu.Lock()
		c.drain = drain
		d.streamMu.Unlock()
		go func() {
			defer close(drain)
			r, e := b.Finish(ctx, wav)
			if e == nil && strings.TrimSpace(speech.StripNonSpeech(r.Text)) != "" {
				d.streamMu.Lock()
				c.hadSpeech = true
				d.streamMu.Unlock()
			}
			backupDone <- completedBackup{r, e}
		}()
		defer func() { <-backupDone }()
	}

	// The energy filter measures loudness, not intelligibility: quiet or short
	// speech can fail it even after the preview recognized words. Recognition
	// wins that disagreement; only the main model's final transcript is emitted.
	if d.silenceFilter && wav != "" && !d.incrementalFinal() {
		if hasSpeech, vadErr := audio.DetectSpeech(wav, 150*time.Millisecond); vadErr == nil && !hasSpeech {
			if !d.previewRecognizedSpeech(ctx, c) {
				d.logger.Info("pipeline: silence detected by VAD pre-filter — skipping transcription", "cycle", c.id)
				d.completeCycle(c, out, false)
				return
			}
			d.logger.Info("pipeline: preview recognized speech below energy threshold — running final transcription", "cycle", c.id)
		}
	}

	transcribeStart := time.Now()
	var text string
	var err error
	if d.incrementalFinal() {
		final := d.finalCycle
		if final == nil {
			err = errors.New("missing final session")
		} else {
			var result speech.FinalResult
			result, err = final.session.Finish(ctx, wav)
			text = result.Text
			d.logger.Info("pipeline: final session", "requested", result.RequestedMode, "actual", result.ActualMode, "replay_reason", result.ReplayReason, "stats", result.Stats)
			// Enabled energy rejection still requires recognition evidence, including
			// authoritative last-tail text; disabled never gates quiet audio.
			if err == nil && d.silenceFilter && (result.ActualMode == models.FinalAfterStop || strings.TrimSpace(speech.StripNonSpeech(text)) == "") {
				if hasSpeech, e := audio.DetectSpeech(wav, 150*time.Millisecond); e == nil && !hasSpeech && !d.previewRecognizedSpeech(ctx, c) {
					text = ""
				}
			}
		}
	} else {
		if c.usesMain {
			d.awaitCycleDrain(ctx, c)
		}
		text, err = d.transcriber.Transcribe(ctx, wav)
	}
	if err != nil {
		if ctx.Err() != nil {
			d.completeCycle(c, out, false)
			return
		}
		mainErr := err
		if speech.IsGPURequestFailure(err) {
			d.showCycleDiagnostic(overlay.Error, c.id, mainErr.Error())
			d.syncHUD(ctx)
			timer := time.NewTimer(d.errorDuration)
			select {
			case <-ctx.Done():
				timer.Stop()
				d.completeCycle(c, out, false)
				return
			case <-timer.C:
			}
			if backupDone != nil {
				var completed completedBackup
				select {
				case <-ctx.Done():
					d.completeCycle(c, out, false)
					return
				case completed = <-backupDone:
				}
				backupDone <- completed
				r := completed.result
				if completed.err == nil && usableBackup(r, c.id) {
					text = r.Text
					err = nil
					out.source = "companion-backup"
					out.model = r.Model
					out.warning = fmt.Sprintf("GPU-enabled server request failed: %v; finalized companion backup: %s", mainErr, r.Model)
					d.logger.Warn("pipeline: using finalized companion backup", "warning", out.warning, "cycle", r.CycleID)
					d.showCycleDiagnostic(overlay.Degraded, c.id, out.warning)
					d.provenanceMu.Lock()
					d.warning = out.warning
					d.warningUntil = time.Now().Add(3 * time.Second)
					d.provenanceMu.Unlock()
				} else {
					d.logger.Warn("pipeline: finalized backup unavailable", "err", completed.err, "complete", r.Complete, "cycle", r.CycleID)
				}
			}
		}
		if err != nil {
			d.reportCycleError(ctx, c, "pipeline: transcribe failed — aborting", mainErr)
			d.completeCycle(c, out, true)
			return
		}
	}
	transcribeMS := time.Since(transcribeStart).Milliseconds()
	d.logger.Info("pipeline: transcript received", "cycle", c.id, "text_len", len(text), "transcribe_ms", transcribeMS)

	// Whisper decodes near-speech to an annotation rather than to nothing, so
	// "[BLANK_AUDIO]" reaches here as a perfectly ordinary non-empty string and
	// would be typed. Strip the annotations before the empty check, so a
	// transcript that was nothing but markers takes the existing no-op path.
	if stripped := speech.StripNonSpeech(text); stripped != text {
		d.logger.Info("pipeline: stripped non-speech markers from transcript",
			"before", text, "after", stripped)
		text = stripped
	}

	// Flatten before the empty check, and before recording, so the log holds
	// exactly the string that gets typed. whisper-cli writes one line per ~30
	// second window, so any dictation longer than that is multi-line; Emit
	// already flattens it, and a history entry that still carried the breaks
	// pasted line endings the user never dictated when it was recovered.
	text = output.CleanText(text)

	if text == "" {
		d.logger.Warn("pipeline: empty transcript — skipping emit (whisper found no speech?)", "cycle", c.id)
		d.completeCycle(c, out, false)
		return
	}
	if ctx.Err() != nil {
		d.completeCycle(c, out, false)
		return
	}
	out.text = text
	// The dispatcher emits in spoken order; this only records the completion.
	d.completeCycle(c, out, false)

	// The overlay stays up for the whole emit span, so "it lingered" is a
	// question about which stage was slow rather than about the overlay.
	d.logger.Info("pipeline: cycle complete",
		"cycle", c.id,
		"chars", len(text),
		"transcribe_ms", transcribeMS,
		"total_ms", time.Since(cycleStart).Milliseconds())
}

// completeCycle queues a finished cycle with the dispatcher. The dispatcher,
// not this call, retires the cycle from the machine — after its output is out,
// so return to Idle means everything has been typed. keepError leaves the error
// pill up for errorDuration once the cycle is retired.
func (d *Daemon) completeCycle(c *dictationCycle, out *dispatchOutcome, keepError bool) {
	out.keepError = keepError
	d.dispatcher.complete(c.id, out)
}

// scheduleRerender repaints the derived visual after a delay, so an error or
// warning has its moment before the stack replaces it.
func (d *Daemon) scheduleRerender(ctx context.Context, after time.Duration) {
	if after <= 0 {
		d.showDerived()
		return
	}
	d.warningWG.Add(1)
	go func() {
		defer d.warningWG.Done()
		timer := time.NewTimer(after)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		d.showDerived()
	}()
}

// removeCapture deletes a stopped recording and the sidecar whisper-cli may
// write beside it.
func removeCapture(wav string) {
	if wav == "" {
		return
	}
	_ = os.Remove(wav)
	_ = os.Remove(wav + ".txt")
}

// TranscriptRecorder persists completed transcripts for later recovery. It is
// an interface so the daemon can be tested without touching the filesystem.
type TranscriptRecorder interface {
	Append(history.Entry) error
}

// ErrNotRunning is returned by `mavor toggle` when the daemon socket isn't
// reachable. cmd/mavor translates this to an actionable user message.
var ErrNotRunning = errors.New("daemon: not running (socket unreachable)")

// soften lowercases a preview that is entirely upper case.
//
// The streaming recognisers that drive the preview emit their tokens in caps —
// the 20M zipformer says "THE GRASS IS TALL AND WIDE" — and a HUD that shouts
// at you while you speak reads as an alarm rather than as feedback. Only the
// preview is touched: the transcript that gets typed comes from the main
// model, which cases its own output, and this never sees it.
//
// The test is "has letters and none of them are lower case", so a model that
// already cases its output is left exactly as it is, and so is a genuine
// acronym sitting inside otherwise normal text.
func soften(s string) string {
	hasUpper := false
	for _, r := range s {
		if unicode.IsLower(r) {
			return s
		}
		if unicode.IsUpper(r) {
			hasUpper = true
		}
	}
	if !hasUpper {
		return s
	}
	return strings.ToLower(s)
}

func closeInitialized(v Initialized) {
	if c, ok := v.Transcriber.(io.Closer); ok {
		_ = c.Close()
	}
	if c, ok := v.PreviewCompanion.(io.Closer); ok {
		_ = c.Close()
	}
}

// syncHUD confirms a production frame submission where the backend supports it;
// it is not compositor screenshot proof. Never delay controls for this wait.
func (d *Daemon) syncHUD(ctx context.Context) {
	if observer, ok := d.overlay.(overlay.FrameObserver); ok {
		frameCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if _, err := observer.SyncFrame(frameCtx); err != nil {
			d.logger.Warn("overlay: frame submission unavailable", "err", err)
		}
	}
}
