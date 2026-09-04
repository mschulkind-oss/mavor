// Package ipc is the JSON-over-Unix-socket protocol the daemon and the
// CLI speak (toggle, start, stop, status). One connection = one request + one response. The
// protocol is intentionally trivial — we are not building a general RPC, just
// a way for a hotkey-launched short-lived process to nudge the daemon.
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
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
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
func (s *Server) Serve(ctx context.Context) error {
	if err := prepareSocket(s.socket); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.socket, err)
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
