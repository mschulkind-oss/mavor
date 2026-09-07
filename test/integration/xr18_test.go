//go:build integration

package integration

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/ipc"
)

// TestXR18ChannelsMuteWhileRecording drives the real daemon binary against a
// stand-in mixer and watches the OSC traffic.
//
// The parts either side of this are covered elsewhere: internal/audio tests
// XR18Ducker against a fake mixer, and internal/daemon tests that the FSM
// ducks on the way into Recording and restores on the way out. What only this
// covers is the wiring in cmd/mavor — that an [xr18] table in config.toml
// reaches the daemon as a ducker at all, and that adding it does not displace
// the PipeWire ducking that was already there.
func TestXR18ChannelsMuteWhileRecording(t *testing.T) {
	mixer := startStubMixer(t, map[int]int32{15: 1, 16: 1})

	h := Start(t, Options{Width: testWidth, Height: testHeight})
	h.ExtraConfig = fmt.Sprintf("[xr18]\nenabled = true\naddress = \"127.0.0.1\"\nport = %d\nchannels = [15, 16]\n",
		mixer.port())
	socket, _ := h.RunDaemon(t.Context(), MavorBinary, "whisper-tiny.en")

	if _, err := ipc.Send(socket, ipc.Request{Action: "start"}, 2*time.Second); err != nil {
		t.Fatalf("start: %v", err)
	}
	mixer.waitFor(t, map[int]int32{15: 0, 16: 0}, "both channels muted once recording starts")

	if _, err := ipc.Send(socket, ipc.Request{Action: "stop"}, 5*time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	mixer.waitFor(t, map[int]int32{15: 1, 16: 1}, "both channels back on once recording ends")

	// The mixer must have been asked before it was told: that query is the
	// whole reason a channel the user had already muted survives a dictation.
	if q := mixer.queries(); len(q) < 2 {
		t.Errorf("mixer saw %d queries, want at least 2 — the daemon muted without reading the current state", len(q))
	}
}

// stubMixer answers `/ch/NN/mix/on` the way X Air firmware does. It duplicates
// a little of internal/audio's test fake on purpose: this one has to work
// against the encoder inside a separately compiled binary, so it shares no
// code with it beyond the wire format itself.
type stubMixer struct {
	conn *net.UDPConn

	mu    sync.Mutex
	on    map[int]int32
	asked []string
}

func startStubMixer(t *testing.T, initial map[int]int32) *stubMixer {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	m := &stubMixer{conn: c, on: map[int]int32{}}
	for k, v := range initial {
		m.on[k] = v
	}
	t.Cleanup(func() { _ = c.Close() })
	go m.serve()
	return m
}

func (m *stubMixer) port() int { return m.conn.LocalAddr().(*net.UDPAddr).Port }

func (m *stubMixer) serve() {
	buf := make([]byte, 1024)
	for {
		n, from, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		addr, rest, ok := oscString(buf[:n])
		if !ok {
			continue
		}
		var ch int
		if _, err := fmt.Sscanf(addr, "/ch/%d/mix/on", &ch); err != nil {
			continue
		}

		m.mu.Lock()
		if len(rest) == 0 { // a query
			m.asked = append(m.asked, addr)
			val := m.on[ch]
			m.mu.Unlock()
			_, _ = m.conn.WriteToUDP(oscInt(addr, val), from)
			continue
		}
		if tags, args, ok := oscString(rest); ok && tags == ",i" && len(args) >= 4 {
			m.on[ch] = int32(binary.BigEndian.Uint32(args[:4]))
		}
		m.mu.Unlock()
	}
}

func (m *stubMixer) queries() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.asked...)
}

func (m *stubMixer) snapshot() map[int]int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int]int32{}
	for k, v := range m.on {
		out[k] = v
	}
	return out
}

func (m *stubMixer) waitFor(t *testing.T, want map[int]int32, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got map[int]int32
	for time.Now().Before(deadline) {
		got = m.snapshot()
		match := true
		for ch, v := range want {
			if got[ch] != v {
				match = false
				break
			}
		}
		if match {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: mixer state %v, want %v", what, got, want)
}

func oscString(b []byte) (s string, rest []byte, ok bool) {
	for i, c := range b {
		if c != 0 {
			continue
		}
		next := i + 1
		for next%4 != 0 {
			next++
		}
		if next > len(b) {
			return "", nil, false
		}
		return string(b[:i]), b[next:], true
	}
	return "", nil, false
}

func oscInt(addr string, val int32) []byte {
	pad := func(s string) []byte {
		b := make([]byte, len(s)+1)
		copy(b, s)
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
		return b
	}
	msg := pad(addr)
	msg = append(msg, pad(",i")...)
	return binary.BigEndian.AppendUint32(msg, uint32(val))
}
