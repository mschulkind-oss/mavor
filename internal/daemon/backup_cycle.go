package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

const backupPendingLimit = 24 * 16000 * 2

type BackupResult struct {
	CycleID     uint64
	Text, Model string
	Complete    bool
	Samples     int64
}
type BackupCycleOptions struct {
	CycleID         uint64
	Model           string
	FinalizeTimeout time.Duration
	OnPartial       func(string)
}

// BackupCycle owns one companion stream. Only its worker accesses the recognizer;
// cancellation joins that worker before the shared recognizer can be reused.
// Callbacks must not call Finish/Cancel synchronously (they run on the worker).
type BackupCycle struct {
	mu          sync.Mutex
	cond        *sync.Cond
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	src         speech.StreamTranscriber
	abort       speech.StreamAborter
	opts        BackupCycleOptions
	queue       [][]byte
	pending     int
	prefix      int64
	digest      hash.Hash
	finishing   bool
	final       *os.File
	tail, total int64
	err         error
	result      BackupResult
}

func NewBackupCycle(ctx context.Context, src speech.StreamTranscriber, opts BackupCycleOptions) (*BackupCycle, error) {
	if src == nil {
		return nil, errors.New("backup: no companion")
	}
	abort, ok := src.(speech.StreamAborter)
	if !ok {
		return nil, errors.New("backup: companion cannot abort")
	}
	if opts.FinalizeTimeout <= 0 {
		opts.FinalizeTimeout = 5 * time.Second
	}
	child, cancel := context.WithCancel(ctx)
	if err := src.StartStream(child); err != nil {
		cancel()
		_ = abort.AbortStream(context.Background())
		return nil, fmt.Errorf("backup start: %w", err)
	}
	c := &BackupCycle{ctx: child, cancel: cancel, done: make(chan struct{}), src: src, abort: abort, opts: opts, digest: sha256.New()}
	c.cond = sync.NewCond(&c.mu)
	go c.run()
	return c, nil
}
func (c *BackupCycle) failLocked(err error) {
	if c.err == nil {
		c.err = err
	}
	c.cancel()
	c.cond.Broadcast()
}

// Feed never waits for recognition; exceeding the bounded pending queue makes
// this entire recording ineligible rather than silently dropping audio.
func (c *BackupCycle) Feed(pcm []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	if c.finishing {
		return errors.New("backup: feed after finish")
	}
	if err := c.ctx.Err(); err != nil {
		c.failLocked(err)
		return err
	}
	if len(pcm)%2 != 0 {
		c.failLocked(errors.New("backup: odd PCM length"))
		return c.err
	}
	if len(pcm) > backupPendingLimit-c.pending {
		c.failLocked(errors.New("backup: audio queue overflow"))
		return c.err
	}
	if len(pcm) == 0 {
		return nil
	}
	c.queue = append(c.queue, append([]byte(nil), pcm...))
	c.pending += len(pcm)
	c.prefix += int64(len(pcm))
	_, _ = c.digest.Write(pcm)
	c.cond.Signal()
	return nil
}

func (c *BackupCycle) run() {
	success := false
	wake := context.AfterFunc(c.ctx, func() { c.mu.Lock(); c.cond.Broadcast(); c.mu.Unlock() })
	defer func() {
		wake()
		if !success {
			_ = c.abort.AbortStream(context.Background())
		}
		c.mu.Lock()
		if c.final != nil {
			_ = c.final.Close()
		}
		c.queue = nil
		c.mu.Unlock()
		close(c.done)
	}()
	for {
		c.mu.Lock()
		for len(c.queue) == 0 && c.final == nil && c.err == nil && c.ctx.Err() == nil {
			c.cond.Wait()
		}
		if c.ctx.Err() != nil && c.err == nil {
			c.err = c.ctx.Err()
		}
		if c.err != nil {
			c.mu.Unlock()
			return
		}
		if len(c.queue) > 0 {
			p := c.queue[0]
			c.queue[0] = nil
			c.queue = c.queue[1:]
			c.mu.Unlock()
			text, err := c.src.FeedChunk(c.ctx, p)
			c.mu.Lock()
			c.pending -= len(p)
			if err != nil {
				c.failLocked(fmt.Errorf("backup feed: %w", err))
			}
			valid := c.err == nil && c.ctx.Err() == nil
			c.mu.Unlock()
			if valid && c.opts.OnPartial != nil {
				c.opts.OnPartial(text)
			}
			continue
		}
		f, tail, total := c.final, c.tail, c.total
		c.mu.Unlock()
		// Final WAV supplies all bytes missed by the last live read, including quiet
		// tail. This is the same stream, not a second reader of the recording source.
		buf := make([]byte, 32000)
		for tail < total {
			if err := c.ctx.Err(); err != nil {
				c.mu.Lock()
				c.failLocked(err)
				c.mu.Unlock()
				return
			}
			n := int64(len(buf))
			if n > total-tail {
				n = total - tail
			}
			_, err := io.ReadFull(f, buf[:n])
			if err == nil {
				_, err = c.src.FeedChunk(c.ctx, buf[:n])
			}
			if err != nil {
				c.mu.Lock()
				c.failLocked(fmt.Errorf("backup tail: %w", err))
				c.mu.Unlock()
				return
			}
			tail += n
		}
		if err := c.ctx.Err(); err != nil {
			c.mu.Lock()
			c.failLocked(err)
			c.mu.Unlock()
			return
		}
		text, err := c.src.StopStream(c.ctx)
		c.mu.Lock()
		if err != nil {
			c.failLocked(fmt.Errorf("backup finalize: %w", err))
		}
		if c.ctx.Err() != nil && c.err == nil {
			c.err = c.ctx.Err()
		}
		if c.err == nil {
			c.result = BackupResult{CycleID: c.opts.CycleID, Model: c.opts.Model, Text: output.CleanText(speech.StripNonSpeech(text)), Complete: true, Samples: total / 2}
			success = true
		}
		c.mu.Unlock()
		return
	}
}

func (c *BackupCycle) Finish(ctx context.Context, path string) (BackupResult, error) {
	c.mu.Lock()
	if c.finishing {
		c.mu.Unlock()
		return BackupResult{}, errors.New("backup: finish already called")
	}
	c.finishing = true
	prefix := c.prefix
	digest := c.digest.Sum(nil)
	prior := c.err
	c.mu.Unlock()
	deadline, stop := context.WithTimeout(ctx, c.opts.FinalizeTimeout)
	defer stop()
	interrupt := context.AfterFunc(deadline, func() { c.mu.Lock(); c.failLocked(deadline.Err()); c.mu.Unlock() })
	defer interrupt()
	if prior == nil {
		f, total, err := backupPCM(path, prefix, digest, deadline)
		c.mu.Lock()
		if err != nil {
			c.failLocked(err)
		} else if c.ctx.Err() != nil || c.err != nil {
			_ = f.Close()
			if c.err == nil {
				c.failLocked(c.ctx.Err())
			}
		} else {
			c.final = f
			c.total = total
			c.tail = prefix
			c.cond.Signal()
		}
		c.mu.Unlock()
	}
	<-c.done // Native calls cannot be freed/preempted on a deadline; always join.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return BackupResult{}, c.err
	}
	return c.result, nil
}

// Cancel is idempotent. ctx is a caller diagnostic, not permission to abandon a
// live native call and reuse/free its recognizer before the owned worker joins.
func (c *BackupCycle) Cancel(ctx context.Context) error {
	c.mu.Lock()
	c.failLocked(context.Canceled)
	c.mu.Unlock()
	<-c.done
	return ctx.Err()
}

// backupPCM validates exact PCM format and live-prefix identity, leaving the
// same open file at the first unaccepted byte. RIFF metadata/padding is allowed.
func backupPCM(path string, prefix int64, digest []byte, ctx context.Context) (f *os.File, total int64, err error) {
	f, err = os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("backup WAV: %w", err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			f = nil
		}
	}()
	var head [12]byte
	if _, err = io.ReadFull(f, head[:]); err != nil {
		return f, 0, err
	}
	if string(head[:4]) != "RIFF" || string(head[8:]) != "WAVE" {
		return f, 0, errors.New("backup: invalid WAV")
	}
	end := int64(binary.LittleEndian.Uint32(head[4:8])) + 8
	stat, e := f.Stat()
	if e != nil {
		return f, 0, e
	}
	if end > stat.Size() || end < 12 {
		return f, 0, errors.New("backup: truncated WAV")
	}
	valid := false
	for pos := int64(12); pos+8 <= end; {
		if e := ctx.Err(); e != nil {
			return f, 0, e
		}
		var h [8]byte
		if _, err = io.ReadFull(f, h[:]); err != nil {
			return f, 0, err
		}
		size := int64(binary.LittleEndian.Uint32(h[4:]))
		pos += 8
		if size > end-pos {
			return f, 0, errors.New("backup: truncated WAV chunk")
		}
		switch string(h[:4]) {
		case "fmt ":
			if size < 16 {
				return f, 0, errors.New("backup: short WAV format")
			}
			var format [16]byte
			if _, err = io.ReadFull(f, format[:]); err != nil {
				return f, 0, err
			}
			valid = binary.LittleEndian.Uint16(format[0:]) == 1 && binary.LittleEndian.Uint16(format[2:]) == 1 && binary.LittleEndian.Uint32(format[4:]) == 16000 && binary.LittleEndian.Uint32(format[8:]) == 32000 && binary.LittleEndian.Uint16(format[12:]) == 2 && binary.LittleEndian.Uint16(format[14:]) == 16
		case "data":
			if !valid || size%2 != 0 || prefix > size {
				return f, 0, errors.New("backup: invalid PCM format or coverage")
			}
			hash := sha256.New()
			buf := make([]byte, 32000)
			for remaining := prefix; remaining > 0; {
				if e := ctx.Err(); e != nil {
					return f, 0, e
				}
				n := int64(len(buf))
				if n > remaining {
					n = remaining
				}
				if _, err = io.ReadFull(f, buf[:n]); err != nil {
					return f, 0, err
				}
				_, _ = hash.Write(buf[:n])
				remaining -= n
			}
			if string(hash.Sum(nil)) != string(digest) {
				return f, 0, errors.New("backup: live prefix differs from final WAV")
			}
			return f, size, nil
		}
		pos += size + (size % 2)
		if _, err = f.Seek(pos, io.SeekStart); err != nil {
			return f, 0, err
		}
	}
	return f, 0, errors.New("backup: WAV has no PCM data")
}
