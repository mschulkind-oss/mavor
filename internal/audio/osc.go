package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// DefaultOSCPort is the UDP port used when none is configured. 10024 is what
// the Behringer X Air family (XR12, XR16, XR18) listens on, which is the
// hardware this was written for; an X32 or M32 uses 10023, and anything else
// uses whatever it uses. It is a convenience, not an assumption — the port is
// configurable and the address always has to be given anyway.
const DefaultOSCPort = 10024

// DefaultOSCTimeoutMS is how long Duck waits for the device to report the
// parameters' current values. What it does not report, it does not get muted
// — see Duck.
//
// It is on the dictation hot path — the daemon calls Duck on the transition
// into Recording, before the recorder starts — so it is deliberately short.
// The queries are sent together and answered in one round trip, measured at
// under 300µs against an XR18 on the same subnet; the timeout only matters
// when the device is off, and then the cost of being wrong is a fifth of a
// second before recording starts.
const DefaultOSCTimeoutMS = 200

// OSCDucker mutes parameters on a network audio device while mavor is
// recording, and puts them back afterwards.
//
// It exists because a mixer is not a sound server. A Behringer XR18 is a box
// on the network with its own inputs and its own monitor mix, so audio
// playing through it never passes through PipeWire and CommandDucker cannot
// reach it. Muting the channel on the device is the only way to stop it
// bleeding into the microphone.
//
// Control is OSC — Open Sound Control, a UDP message format of an address
// string and typed arguments. Nothing here is mixer-specific: a ducker is
// given a list of OSC addresses and an integer to write to them, so any
// device with an integer parameter that means "off" can be driven by it. On
// an X Air that parameter is `/ch/NN/mix/on`, where NN is the channel
// zero-padded to two digits and the value is 1 for on and 0 for muted, but
// the code knows none of that — it is entirely a matter of what is in the
// config file.
//
// The one convention it does rely on is that sending an address with no
// arguments asks for the current value, which is how OSC query works and what
// X Air firmware does. A device that will not answer that is one this ducker
// declines to touch at all, because it could not undo what it did.
//
// Whatever the parameters were before Duck is what Restore puts back, so a
// channel the user had already muted stays muted afterwards rather than being
// switched on by a dictation ending.
//
// Best-effort by design. UDP acknowledges nothing, and a mixer that is
// powered off is a normal Tuesday — Duck reports what went wrong and the
// daemon logs it and dictates anyway, which is the same contract
// CommandDucker has.
type OSCDucker struct {
	mu      sync.Mutex
	addr    string
	paths   []string
	muted   int32
	timeout time.Duration
	log     *slog.Logger

	conn *net.UDPConn
	// saved is each path's value as it was before Duck. Nil when not ducked.
	saved  map[string]int32
	ducked bool
}

// NewOSCDucker builds a ducker for the device at host (an IP or hostname)
// that mutes by writing mutedValue to each of paths. A port or timeout of
// zero takes the default.
func NewOSCDucker(host string, port int, paths []string, mutedValue int32, timeout time.Duration) (*OSCDucker, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("osc: no address — set ducking.osc.address to the device's IP")
	}
	if port == 0 {
		port = DefaultOSCPort
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("osc: port %d is not a port", port)
	}
	if timeout <= 0 {
		timeout = DefaultOSCTimeoutMS * time.Millisecond
	}
	if len(paths) == 0 {
		return nil, errors.New(`osc: no paths — set ducking.osc.paths to the parameters to mute, e.g. ["/ch/15/mix/on", "/ch/16/mix/on"]`)
	}
	// Deduplicated, keeping the order they were written in so logs and
	// diagnostics read the way the config file does.
	seen := map[string]bool{}
	var clean []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		// An OSC address is a path and must start with a slash. Sending
		// anything else is a message the device silently ignores, which
		// would look exactly like a mixer that is switched off.
		if !strings.HasPrefix(p, "/") {
			return nil, fmt.Errorf("osc: %q is not an OSC address — it must start with %q, e.g. \"/ch/15/mix/on\"", p, "/")
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		clean = append(clean, p)
	}

	return &OSCDucker{
		addr:    net.JoinHostPort(host, fmt.Sprint(port)),
		paths:   clean,
		muted:   mutedValue,
		timeout: timeout,
		log:     slog.Default(),
	}, nil
}

// SetLogger overrides the logger.
func (x *OSCDucker) SetLogger(l *slog.Logger) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if l != nil {
		x.log = l
	}
}

// Addr is the device's UDP address, for logs and diagnostics.
func (x *OSCDucker) Addr() string { return x.addr }

// Paths are the OSC addresses this ducker mutes, in config order.
func (x *OSCDucker) Paths() []string {
	out := make([]string, len(x.paths))
	copy(out, x.paths)
	return out
}

// MutedValue is what Duck writes to each path.
func (x *OSCDucker) MutedValue() int32 { return x.muted }

// IsDucked reports whether the parameters are currently muted by mavor.
func (x *OSCDucker) IsDucked() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.ducked
}

// Duck records what each parameter is set to and mutes it.
func (x *OSCDucker) Duck() error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if x.ducked {
		return nil // already muted; keep the values saved the first time
	}
	if err := x.dial(); err != nil {
		return err
	}

	// Ask about every path before muting any, and mute only what answered.
	//
	// Not muting an unreadable parameter is the whole safety story. There is
	// no generic "unmuted" value to fall back on — OSC says nothing about
	// what a number means, and this ducker is told only which value mutes —
	// so a parameter whose previous value was never read is one Restore could
	// only guess at. Muting it anyway would risk leaving it muted after the
	// dictation with nothing in the device's interface to explain why, which
	// is a far worse failure than not ducking. If a device accepts sets but
	// does not answer queries, it is not usable here, and `mavor doctor` says
	// so in words.
	saved, missing := x.query()
	if len(missing) > 0 {
		x.log.Warn("osc: device did not report these parameters, so they were left alone",
			"paths", missing, "addr", x.addr, "timeout", x.timeout)
	}

	var errs []error
	for _, p := range x.paths {
		if _, ok := saved[p]; !ok {
			continue
		}
		if err := x.send(p, x.muted); err != nil {
			errs = append(errs, err)
		}
	}
	// Ducked even on a partial failure: some parameters are muted now, and
	// Restore is the only thing that will put them back.
	x.saved = saved
	x.ducked = true
	return errors.Join(errs...)
}

// Restore sets each parameter back to the value it had before Duck.
func (x *OSCDucker) Restore() error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if !x.ducked {
		return nil
	}
	x.ducked = false
	saved := x.saved
	x.saved = nil

	if err := x.dial(); err != nil {
		return err
	}
	var errs []error
	for _, p := range x.paths {
		old, ok := saved[p]
		if !ok {
			continue // never read, therefore never muted — see Duck
		}
		if err := x.send(p, old); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Probe asks the device for the current value of each configured parameter,
// changing nothing. It is what `mavor doctor` reports: OSC is UDP, so a device
// at the wrong address, on another VLAN or simply switched off is
// indistinguishable from a working one until something asks it a question and
// waits.
//
// Returns each path that answered with its value, and the paths that did not.
func (x *OSCDucker) Probe() (values map[string]int32, missing []string, err error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := x.dial(); err != nil {
		return nil, nil, err
	}
	got, missed := x.query()
	return got, missed, nil
}

// Close releases the socket. The ducker is usable again afterwards: the next
// Duck, Restore or Probe dials a new one.
func (x *OSCDucker) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.closeConn()
}

func (x *OSCDucker) closeConn() error {
	if x.conn == nil {
		return nil
	}
	err := x.conn.Close()
	x.conn = nil
	return err
}

// dial makes sure there is a socket. Held open between dictations: a
// connected UDP socket costs nothing to keep and saves resolving the address
// on the hot path.
func (x *OSCDucker) dial() error {
	if x.conn != nil {
		return nil
	}
	ua, err := net.ResolveUDPAddr("udp", x.addr)
	if err != nil {
		return fmt.Errorf("osc: resolve %s: %w", x.addr, err)
	}
	c, err := net.DialUDP("udp", nil, ua)
	if err != nil {
		return fmt.Errorf("osc: dial %s: %w", x.addr, err)
	}
	x.conn = c
	return nil
}

// query asks for every path's current value and returns what came back, plus
// the paths that did not answer in time.
//
// Every question goes out before the first answer is read. Waiting for each
// reply in turn would cost one round trip per path, and against an
// unreachable device one whole timeout per path — with two channels that is
// nearly half a second added to the start of every dictation.
func (x *OSCDucker) query() (map[string]int32, []string) {
	want := map[string]bool{}
	for _, p := range x.paths {
		want[p] = true
	}
	for _, p := range x.paths {
		if err := x.sendRaw(encodeQuery(p)); err != nil {
			x.log.Debug("osc: query failed", "path", p, "err", err)
		}
	}

	got := map[string]int32{}
	deadline := time.Now().Add(x.timeout)
	_ = x.conn.SetReadDeadline(deadline)
	buf := make([]byte, 4096)
	for len(got) < len(want) && time.Now().Before(deadline) {
		n, err := x.conn.Read(buf)
		if err != nil {
			break // deadline, or the socket went away; the caller reports it
		}
		addr, val, ok := decodeIntReply(buf[:n])
		if !ok {
			continue
		}
		if want[addr] {
			got[addr] = val
		}
	}
	_ = x.conn.SetReadDeadline(time.Time{})

	var missing []string
	for _, p := range x.paths {
		if _, ok := got[p]; !ok {
			missing = append(missing, p)
		}
	}
	return got, missing
}

func (x *OSCDucker) send(addr string, val int32) error {
	return x.sendRaw(encodeInt(addr, val))
}

func (x *OSCDucker) sendRaw(msg []byte) error {
	if x.conn == nil {
		return errors.New("osc: not connected")
	}
	if _, err := x.conn.Write(msg); err != nil {
		// A write error means this socket is finished. Drop it so the next
		// call dials again rather than failing forever against a dead one.
		_ = x.closeConn()
		return fmt.Errorf("osc: send to %s: %w", x.addr, err)
	}
	return nil
}

// padOSCString writes an OSC string: the bytes, then at least one NUL, then
// NULs until the length is a multiple of four. A string whose length is
// already a multiple of four gets four NULs, not zero — the terminator is
// required before the padding.
func padOSCString(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// encodeQuery is an OSC message with an address and no type tag, which is how
// a parameter's current value is asked for.
func encodeQuery(addr string) []byte { return padOSCString(addr) }

// encodeInt is an OSC message setting addr to a single 32-bit integer.
func encodeInt(addr string, val int32) []byte {
	msg := padOSCString(addr)
	msg = append(msg, padOSCString(",i")...)
	msg = binary.BigEndian.AppendUint32(msg, uint32(val))
	return msg
}

// decodeIntReply reads an OSC message carrying one integer, which is what a
// device answers a query with. Anything else — a different type, a bundle, a
// truncated packet — is reported as not ok rather than guessed at.
func decodeIntReply(b []byte) (addr string, val int32, ok bool) {
	addr, rest, ok := readOSCString(b)
	if !ok {
		return "", 0, false
	}
	tags, rest, ok := readOSCString(rest)
	if !ok || tags != ",i" || len(rest) < 4 {
		return "", 0, false
	}
	return addr, int32(binary.BigEndian.Uint32(rest[:4])), true
}

func readOSCString(b []byte) (s string, rest []byte, ok bool) {
	end := -1
	for i, c := range b {
		if c == 0 {
			end = i
			break
		}
	}
	if end < 0 {
		return "", nil, false
	}
	// Consume the terminator and the padding that follows it.
	next := end + 1
	for next%4 != 0 {
		next++
	}
	if next > len(b) {
		return "", nil, false
	}
	return string(b[:end]), b[next:], true
}
