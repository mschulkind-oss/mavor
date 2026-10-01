package output

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// X11Clipboard holds CLIPBOARD through a foreground xclip process. XWayland's
// selection bridge can supply native Wayland consumers without helper focus.
// Close must be called at daemon shutdown. No key injection or PRIMARY writes.
type X11Clipboard struct {
	gate   chan struct{}
	owner  *x11Owner // protected by gate
	closed bool
}

type x11Owner struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // read only after done closes
}

func NewX11Clipboard() *X11Clipboard {
	return &X11Clipboard{gate: make(chan struct{}, 1)}
}

// xclip's quiet (foreground) mode announces its request loop on stderr. This
// bounds startup, not successful selection transfer, which requires a consumer.
// Keep only a bounded diagnostic tail; never wait for stderr EOF from a holder.
type x11Startup struct {
	mu    sync.Mutex
	tail  string
	ready chan struct{}
	once  sync.Once
}

func (s *x11Startup) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tail += string(p)
	if strings.Contains(s.tail, "Waiting for selection requests, Control-C to quit") {
		s.once.Do(func() { close(s.ready) })
	}
	if len(s.tail) > 4096 {
		s.tail = s.tail[len(s.tail)-4096:]
	}
	return len(p), nil
}
func (s *x11Startup) diagnostic() string { s.mu.Lock(); defer s.mu.Unlock(); return s.tail }

func (o *x11Owner) stop() {
	select {
	case <-o.done:
		return
	default:
	}
	_ = o.cmd.Process.Kill()
	<-o.done
}

func (c *X11Clipboard) Emit(ctx context.Context, text string) error {
	text = CleanText(text)
	if text == "" {
		return nil
	}
	launchCtx, cancel := context.WithTimeout(ctx, ClipboardLaunchTimeout)
	defer cancel()
	select {
	case c.gate <- struct{}{}:
	case <-launchCtx.Done():
		return launchCtx.Err()
	}
	defer func() { <-c.gate }()
	if c.closed {
		return errors.New("output: x11 clipboard closed")
	}
	if err := launchCtx.Err(); err != nil {
		return err
	}
	startup := &x11Startup{ready: make(chan struct{})}
	// Deliberately not CommandContext: a successful owner must outlive Emit's
	// context. Only a failed/canceled launch is killed; successful owners live
	// until replacement, external selection loss, connection loss, or Close.
	cmd := exec.Command("xclip", "-selection", "clipboard", "-target", "UTF8_STRING", "-quiet", "-loops", "0")
	// Linux parent-death signaling also ends ownership on abrupt daemon death.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	cmd.Stdin = bytes.NewReader([]byte(text))
	cmd.Stderr = startup
	cmd.WaitDelay = 500 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("output: x11 clipboard launch: %w", err)
	}
	owner := &x11Owner{cmd: cmd, done: make(chan struct{})}
	go func() { owner.err = cmd.Wait(); close(owner.done) }()
	var err error
	select {
	case <-launchCtx.Done():
		err = launchCtx.Err()
	case <-owner.done:
		err = fmt.Errorf("xclip exited during startup: %v (%s)", owner.err, startup.diagnostic())
	case <-startup.ready:
		err = launchCtx.Err()
		select {
		case <-owner.done:
			err = fmt.Errorf("xclip exited during startup: %v", owner.err)
		default:
		}
	}
	if err != nil {
		owner.stop()
		return fmt.Errorf("output: x11 clipboard copy: %w", err)
	}
	if c.owner != nil {
		c.owner.stop()
	}
	c.owner = owner
	return nil
}

// Close terminates and reaps the last owner. Persistence after exit depends on
// the compositor's clipboard manager; it is not promised by this dispatcher.
func (c *X11Clipboard) Close() error {
	c.gate <- struct{}{}
	defer func() { <-c.gate }()
	c.closed = true
	if c.owner != nil {
		c.owner.stop()
		c.owner = nil
	}
	return nil
}
