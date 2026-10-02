package speech

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/models"
)

// FinalSession owns main-model work for one recording. Partials are provisional;
// only Finish's result may be emitted, after capture has stopped.
type FinalSession interface {
	Feed([]byte) error
	FailLive(string)
	Snapshot() FinalStats
	Finish(context.Context, string) (FinalResult, error)
	Cancel(context.Context) error
}
type FinalSessionOptions struct {
	Mode      models.FinalMode
	Logger    *slog.Logger
	OnPartial func(string)
}
type FinalStats struct {
	CapturedSamples, LiveSamples, TailSamples, PendingSamplesAtRelease, PeakPendingSamples int64
	Segments                                                                               int
	DrainDuration, FinalizeDuration, ReplayDuration                                        time.Duration
	Overloaded                                                                             bool
}
type FinalResult struct {
	Text                      string
	RequestedMode, ActualMode models.FinalMode
	ReplayReason              string
	Stats                     FinalStats
}

const finalBudgetSamples = 24 * 16000

type finalSession struct {
	opMu                      sync.Mutex
	mu                        sync.Mutex
	t                         Transcriber
	stream                    StreamTranscriber
	opts                      FinalSessionOptions
	lifetime                  context.Context
	cancelLifetime            context.CancelFunc
	ctx                       context.Context
	cancel                    context.CancelFunc
	queue                     chan []byte
	done                      chan struct{}
	stats                     FinalStats
	pending                   int64
	capturedBytes             int64
	digest                    hash.Hash
	reason                    string
	finished, closed, started bool
	abandoned                 bool
	segments                  []string // worker-owned until joined
	segment                   []int16
	quiet                     int
}

func NewFinalSession(ctx context.Context, t Transcriber, opts FinalSessionOptions) (FinalSession, error) {
	mode, err := models.ParseFinalMode(string(opts.Mode))
	if err != nil {
		return nil, err
	}
	opts.Mode = mode
	s := &finalSession{t: t, opts: opts, queue: make(chan []byte, 1024), done: make(chan struct{}), digest: sha256.New()}
	s.lifetime, s.cancelLifetime = context.WithCancel(ctx)
	s.ctx, s.cancel = context.WithCancel(s.lifetime)
	if mode == models.FinalStreaming {
		st, ok := t.(StreamTranscriber)
		if !ok {
			s.cancel()
			s.cancelLifetime()
			return nil, errors.New("speech: streaming final requires a streaming implementation")
		}
		if sh, ok := Unwrap(t).(*SherpaTranscriber); ok && !sh.Streaming {
			s.cancel()
			s.cancelLifetime()
			return nil, errors.New("speech: streaming final loaded an offline model")
		}
		if _, ok := Unwrap(t).(StreamAborter); !ok {
			s.cancel()
			s.cancelLifetime()
			return nil, errors.New("speech: streaming final requires abort support")
		}
		s.stream = st
	}
	if mode == models.FinalSegments {
		if sh, ok := Unwrap(t).(*SherpaTranscriber); ok && sh.Streaming {
			s.cancel()
			s.cancelLifetime()
			return nil, errors.New("speech: segment final loaded an online model")
		}
	}
	go s.work()
	return s, nil
}
func (s *finalSession) FailLive(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failLocked(reason)
}
func (s *finalSession) failLocked(reason string) {
	if reason == "" {
		reason = "live recognition failed"
	}
	if s.reason == "" {
		s.reason = reason
	}
	s.cancel()
}
func (s *finalSession) Feed(pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return errors.New("speech: final session already finished")
	}
	s.capturedBytes += int64(len(pcm))
	_, _ = s.digest.Write(pcm)
	if s.opts.Mode == models.FinalAfterStop {
		return nil
	}
	return s.enqueueLocked(pcm)
}
func (s *finalSession) enqueueLocked(pcm []byte) error {
	if s.reason != "" {
		return errors.New(s.reason)
	}
	if len(pcm)%2 != 0 {
		s.failLocked("unaligned live PCM")
		return errors.New(s.reason)
	}
	if len(pcm) == 0 {
		return nil
	}
	n := int64(len(pcm) / 2)
	if s.pending+n > finalBudgetSamples {
		s.stats.Overloaded = true
		s.failLocked("live audio backlog exceeded 24 seconds")
		return errors.New(s.reason)
	}
	select {
	case s.queue <- append([]byte(nil), pcm...):
		s.pending += n
		if s.pending > s.stats.PeakPendingSamples {
			s.stats.PeakPendingSamples = s.pending
		}
		return nil
	default:
		s.stats.Overloaded = true
		s.failLocked("live chunk queue full")
		return errors.New(s.reason)
	}
}
func (s *finalSession) Snapshot() FinalStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.stats
	v.CapturedSamples = s.capturedBytes / 2
	v.PendingSamplesAtRelease = s.pending
	return v
}
func (s *finalSession) processed(n int) {
	s.mu.Lock()
	s.pending -= int64(n)
	s.stats.LiveSamples += int64(n)
	s.mu.Unlock()
}
func (s *finalSession) work() {
	defer close(s.done)
	if s.stream != nil {
		if err := s.stream.StartStream(s.ctx); err != nil {
			s.FailLive("start stream: " + err.Error())
			return
		}
		s.started = true
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case pcm, ok := <-s.queue:
			if !ok {
				if s.opts.Mode == models.FinalSegments && len(s.segment) > 0 {
					if err := s.decodeSegment(); err != nil {
						s.FailLive(err.Error())
					}
				}
				return
			}
			if s.opts.Mode == models.FinalAfterStop {
				continue
			}
			if s.stream != nil {
				partial, err := s.stream.FeedChunk(s.ctx, pcm)
				if err != nil {
					s.FailLive("feed stream: " + err.Error())
					return
				}
				s.processed(len(pcm) / 2)
				if s.opts.OnPartial != nil && s.ctx.Err() == nil {
					s.opts.OnPartial(partial)
				}
			} else {
				for i := 0; i < len(pcm); i += 2 {
					s.segment = append(s.segment, int16(binary.LittleEndian.Uint16(pcm[i:])))
					if len(s.segment)%480 == 0 {
						frame := s.segment[len(s.segment)-480:]
						if audio.CalculateRMS(frame) < audio.SpeechRMSThreshold {
							s.quiet += 480
						} else {
							s.quiet = 0
						}
						if len(s.segment) >= 32000 && s.quiet >= 7200 {
							if err := s.decodeSegment(); err != nil {
								s.FailLive(err.Error())
								return
							}
						}
						if len(s.segment) >= 12*16000 {
							s.FailLive("no safe segment boundary within 12 seconds")
							return
						}
					}
				}
			}
		}
	}
}
func (s *finalSession) decodeSegment() error {
	f, err := os.CreateTemp("", "mavor-final-*.wav")
	if err != nil {
		return err
	}
	path := f.Name()
	_ = f.Close()
	defer os.Remove(path)
	defer os.Remove(path + ".txt")
	if err := audio.WriteWAV(path, s.segment, 16000); err != nil {
		return err
	}
	text, err := s.t.Transcribe(s.ctx, path)
	if err != nil {
		return err
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.segments = append(s.segments, strings.TrimSpace(text))
	s.processed(len(s.segment))
	s.segment = nil
	s.quiet = 0
	s.mu.Lock()
	s.stats.Segments++
	s.mu.Unlock()
	if s.opts.OnPartial != nil {
		s.opts.OnPartial(strings.Join(s.segments, " "))
	}
	return nil
}
func (s *finalSession) join() {
	s.mu.Lock()
	if !s.closed {
		close(s.queue)
		s.closed = true
	}
	s.mu.Unlock()
	<-s.done
}
func (s *finalSession) abort(ctx context.Context) error {
	if s.started && s.stream != nil {
		err := s.stream.(StreamAborter).AbortStream(ctx)
		if err == nil {
			s.started = false
		}
		return err
	}
	return nil
}
func (s *finalSession) Cancel(ctx context.Context) error {
	s.mu.Lock()
	s.abandoned = true
	s.mu.Unlock()
	s.cancelLifetime()
	s.cancel()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	s.finished = true
	s.mu.Unlock()
	s.join()
	return s.abort(ctx)
}
func (s *finalSession) Finish(ctx context.Context, path string) (FinalResult, error) {
	// Explicit cancellation must also reach finalization/replay; FailLive only
	// cancels the disposable live worker, leaving same-model replay available.
	ctx, cancelFinish := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(s.lifetime, cancelFinish)
	defer stopCancel()
	defer cancelFinish()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	began := time.Now()
	r := FinalResult{RequestedMode: s.opts.Mode, ActualMode: s.opts.Mode}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return r, errors.New("speech: final session already finished")
	}
	s.finished = true
	defer s.cancelLifetime()
	releasePending := s.pending
	s.mu.Unlock()
	pcm, wavErr := readFinalPCM(path)
	if wavErr == nil {
		s.mu.Lock()
		s.stats.CapturedSamples = int64(len(pcm) / 2)
		s.mu.Unlock()
	}
	if s.opts.Mode != models.FinalAfterStop {
		err := wavErr
		s.mu.Lock()
		if err != nil {
			s.failLocked("WAV reconciliation: " + err.Error())
		} else {
			s.stats.CapturedSamples = int64(len(pcm) / 2)
			if s.capturedBytes > int64(len(pcm)) || s.capturedBytes%2 != 0 {
				s.failLocked("live coverage exceeds final WAV")
			} else {
				prefix := sha256.Sum256(pcm[:int(s.capturedBytes)])
				if string(prefix[:]) != string(s.digest.Sum(nil)) {
					s.failLocked("live prefix does not match final WAV")
				} else {
					tail := pcm[int(s.capturedBytes):]
					s.stats.TailSamples = int64(len(tail) / 2)
					for len(tail) > 0 && s.reason == "" {
						n := min(len(tail), 960)
						_ = s.enqueueLocked(tail[:n])
						tail = tail[n:]
					}
				}
			}
		}
		s.mu.Unlock()
	}
	s.join()
	s.mu.Lock()
	r.ReplayReason = s.reason
	r.Stats = s.stats
	r.Stats.PendingSamplesAtRelease = s.pending
	s.mu.Unlock()
	// Release pending is measured before tail reconciliation, not after drain.
	r.Stats.PendingSamplesAtRelease = releasePending
	r.Stats.DrainDuration = time.Since(began)
	if s.isAbandoned() {
		_ = s.abort(context.Background())
		return r, context.Canceled
	}
	if ctx.Err() != nil || s.ctx.Err() != nil && r.ReplayReason == "" {
		_ = s.abort(context.Background())
		return r, ctxError(ctx, s.ctx)
	}
	finalize := time.Now()
	if s.opts.Mode == models.FinalStreaming && r.ReplayReason == "" {
		text, err := s.stream.StopStream(ctx)
		if err != nil {
			r.ReplayReason = "stop stream: " + err.Error()
		} else {
			r.Text = text
			s.started = false
		}
	} else if s.opts.Mode == models.FinalSegments && r.ReplayReason == "" {
		r.Text = strings.Join(s.segments, " ")
	}
	r.Stats.FinalizeDuration = time.Since(finalize)
	if r.ReplayReason != "" {
		if err := s.abort(context.Background()); err != nil {
			return r, fmt.Errorf("speech: abort before replay: %w", err)
		}
		r.ActualMode = models.FinalAfterStop
	}
	if r.ActualMode == models.FinalAfterStop {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		start := time.Now()
		text, err := s.t.Transcribe(ctx, path)
		r.Stats.ReplayDuration = time.Since(start)
		r.Text = text
		s.cancel()
		if s.isAbandoned() {
			return r, context.Canceled
		}
		return r, err
	}
	if s.isAbandoned() {
		s.cancel()
		return r, context.Canceled
	}
	s.cancel()
	return r, ctx.Err()
}
func (s *finalSession) isAbandoned() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.abandoned }
func ctxError(a, b context.Context) error {
	if a.Err() != nil {
		return a.Err()
	}
	return b.Err()
}

// readFinalPCM refuses format conversion or truncated chunks: the queued bytes
// must be a verifiable, contiguous prefix of precisely this retained recording.
func readFinalPCM(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("invalid WAV")
	}
	valid := false
	for i := 12; i+8 <= len(b); {
		n := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		id := string(b[i : i+4])
		i += 8
		if n > len(b)-i {
			return nil, errors.New("truncated WAV chunk")
		}
		p := b[i : i+n]
		if id == "fmt " {
			valid = n >= 16 && binary.LittleEndian.Uint16(p) == 1 && binary.LittleEndian.Uint16(p[2:]) == 1 && binary.LittleEndian.Uint32(p[4:]) == 16000 && binary.LittleEndian.Uint16(p[14:]) == 16
		}
		if id == "data" {
			if !valid || n%2 != 0 {
				return nil, errors.New("requires 16kHz mono s16le WAV")
			}
			return p, nil
		}
		i += n + n%2
	}
	return nil, errors.New("missing WAV data")
}
