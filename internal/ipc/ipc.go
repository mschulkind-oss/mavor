// Package ipc is the JSON-over-Unix-socket protocol the daemon and the
// CLI speak (toggle, start, stop, status). One connection = one request + one response. The
// protocol is intentionally trivial — we are not building a general RPC, just
// a way for a hotkey-launched short-lived process to nudge the daemon.
//
// Architecture and invariants: docs/reference/how-mavor-works.md
package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

type Request struct {
	Action string `json:"action"`
}

type Response struct {
	Source  string `json:"source,omitempty"`
	Warning string `json:"warning,omitempty"`
	State   string `json:"state,omitempty"`
	Error   string `json:"error,omitempty"`
	// Residency names where the daemon's main model currently lives: "vram"
	// or "gtt". Empty means there is nothing to report — the model has no
	// GPU-resident weights to track, or the probe could not read the child.
	// See speech.Residency for the states themselves.
	Residency string `json:"residency,omitempty"`
	// ResidencyVRAM, ResidencyGTT and ResidencyEvicted are the model's own
	// footprint in bytes, as the reading behind Residency saw it: what the
	// child holds in device memory, what it holds in system RAM, and how much
	// of the first the driver says it pushed out. They are zero whenever
	// Residency is empty, and on a kernel that does not print the evicted
	// counter. A model still "vram" by the residency rule can be carrying most
	// of its working set in GTT, which is what these numbers answer and the one
	// word cannot.
	ResidencyVRAM    uint64 `json:"residency_vram_bytes,omitempty"`
	ResidencyGTT     uint64 `json:"residency_gtt_bytes,omitempty"`
	ResidencyEvicted uint64 `json:"residency_evicted_bytes,omitempty"`
	// Recording is true while a capture is active. State already derives to
	// "recording" in that case; this makes the stack explicit for a caller that
	// wants to render each row rather than the coarse word.
	Recording bool `json:"recording,omitempty"`
	// Transcribing is the number of stopped dictations awaiting output. State
	// stays "transcribing" for all of them; the count is what says how deep the
	// stack is.
	Transcribing int `json:"transcribing,omitempty"`
}

type Handler func(Request) Response

type Server struct {
	socket  string
	handler Handler
}

func NewServer(socket string, h Handler) *Server {
	return &Server{socket: socket, handler: h}
}

// Serve listens on the configured socket until ctx is cancelled. A stale
// socket file from a crashed previous daemon is silently replaced; a *live*
// socket (a process still listening) returns ErrAddrInUse.
func (s *Server) Serve(ctx context.Context) error { return s.ServeReady(ctx, nil) }

// ServeReady calls ready after binding, before accepting requests.
func (s *Server) ServeReady(ctx context.Context, ready func()) error {
	if err := prepareSocket(s.socket); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.socket, err)
	}

	if ready != nil {
		ready()
	}

	// Close the listener when ctx is done; this unblocks Accept and lets us
	// remove the socket file before returning.
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	defer os.Remove(s.socket)

	var wg sync.WaitGroup
	for {
		conn, err := listener.Accept()
		if err != nil {
			wg.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		wg.Go(func() { s.handle(conn) })
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(Response{Error: fmt.Sprintf("decode: %v", err)})
		return
	}
	resp := s.handler(req)
	_ = json.NewEncoder(conn).Encode(resp)
}

// prepareSocket removes a stale socket file at path if no process is
// listening on it. If a process *is* listening, it returns an error so the
// new daemon refuses to start (avoiding a dueling-daemons situation).
func prepareSocket(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if conn, err := net.DialTimeout("unix", path, 100*time.Millisecond); err == nil {
		conn.Close()
		return fmt.Errorf("ipc: socket %s is already in use by another daemon", path)
	}
	return os.Remove(path)
}

// Send connects to the socket, writes the request, reads one response, and
// closes. timeout bounds the entire round-trip.
func Send(socket string, req Request, timeout time.Duration) (Response, error) {
	deadline := time.Now().Add(timeout)
	conn, err := net.DialTimeout("unix", socket, timeout)
	if err != nil {
		return Response{}, fmt.Errorf("dial %s: %w", socket, err)
	}
	defer conn.Close()
	conn.SetDeadline(deadline)

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("write request: %w", err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("read response: %w", err)
	}
	return resp, nil
}

// ErrAddrInUse is returned by Serve when the socket file is bound by a live
// process.
var ErrAddrInUse = errors.New("socket already in use")
