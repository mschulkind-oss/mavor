package audio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultXR18Port is the UDP port an X Air mixer listens for OSC on. The
// XR12, XR16 and XR18 all use it; an X32 or M32 uses 10023 instead, which is
// the only reason the port is configurable.
const DefaultXR18Port = 10024

// DefaultXR18TimeoutMS is how long Duck waits for the mixer to report the
// channels' current state before muting them anyway.
//
// It is on the dictation hot path — the daemon calls Duck on the transition
// into Recording, before the recorder starts — so it is deliberately short.
// The queries are sent together and answered in one round trip, which is a
// millisecond or two on a LAN; the timeout only matters when the mixer is
// off, and then the cost of being wrong is a fifth of a second before
// recording starts.
const DefaultXR18TimeoutMS = 200

// XR18Ducker mutes channels on a Behringer X Air digital mixer while mavor is
// recording, and unmutes them afterwards.
//
// It exists because a mixer is not a sound server: the XR18 is a box on the
// network with its own inputs and its own monitor mix, so audio playing
// through it never passes through PipeWire and CommandDucker cannot reach it.
// Muting a channel there is the only way to stop it bleeding into the
// microphone.
//
// Control is OSC — Open Sound Control, a small UDP message format of an
// address string and typed arguments — which is what X Air firmware speaks
// and what X-Air-Edit itself uses. The parameter is `/ch/NN/mix/on`, where NN
// is the channel zero-padded to two digits and the value is 1 for a channel
// that is on and 0 for one that is muted. Sending the address with no
// argument asks the mixer for the current value; sending it with an int sets
// it.
//
// Whatever the channels were before Duck is what Restore puts back, so a
// channel the user had already muted stays muted afterwards rather than being
// switched on by a dictation.
//
// Best-effort by design. UDP acknowledges nothing, and a mixer that is
// powered off is a normal Tuesday — Duck reports what went wrong and the
// daemon logs it and dictates anyway, which is the same contract
// CommandDucker has.
type XR18Ducker struct {
	mu       sync.Mutex
	addr     string
	channels []int
	timeout  time.Duration
	log      *slog.Logger

	conn *net.UDPConn
	// saved is each channel's `mix/on` value as it was before Duck, keyed by
	// channel number. Nil when not ducked.
	saved  map[int]int32
	ducked bool
}

// NewXR18Ducker builds a ducker for the mixer at host (an IP or hostname) and
// the given channels. A port or timeout of zero takes the default.
func NewXR18Ducker(host string, port int, channels []int, timeout time.Duration) (*XR18Ducker, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("xr18: no address — set xr18.address to the mixer's IP")
	}
	if port == 0 {
		port = DefaultXR18Port
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("xr18: port %d is not a port", port)
	}
	if timeout <= 0 {
		timeout = DefaultXR18TimeoutMS * time.Millisecond
	}
	if len(channels) == 0 {
		return nil, errors.New("xr18: no channels — set xr18.channels to the channels to mute, e.g. [15, 16]")
	}
	// Deduplicated and sorted so the requests go out in a stable order and a
	// channel listed twice is not saved twice.
	seen := map[int]bool{}
	var chans []int
	for _, ch := range channels {
		// The XR18 has 16 mic channels; an X32 has 32. Anything outside that
		// is a typo rather than a channel, and silently addressing
		// `/ch/00/mix/on` would be a request the mixer ignores.
		if ch < 1 || ch > 32 {
			return nil, fmt.Errorf("xr18: channel %d is out of range (1-16 on an XR18, 1-32 on an X32)", ch)
		}
		if seen[ch] {
			continue
		}
		seen[ch] = true
		chans = append(chans, ch)
	}
	sort.Ints(chans)

	return &XR18Ducker{
		addr:     net.JoinHostPort(host, fmt.Sprint(port)),
		channels: chans,
		timeout:  timeout,
		log:      slog.Default(),
	}, nil
}

// SetLogger overrides the logger.
func (x *XR18Ducker) SetLogger(l *slog.Logger) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if l != nil {
		x.log = l
	}
}

// Addr is the mixer's UDP address, for logs and diagnostics.
func (x *XR18Ducker) Addr() string { return x.addr }

// Channels are the channels this ducker mutes, sorted.
func (x *XR18Ducker) Channels() []int {
	out := make([]int, len(x.channels))
	copy(out, x.channels)
	return out
}

// IsDucked reports whether the channels are currently muted by mavor.
func (x *XR18Ducker) IsDucked() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.ducked
}

// Duck records what each channel is set to and mutes it.
func (x *XR18Ducker) Duck() error {
	x.mu.Lock()
	defer x.mu.Unlock()

	if x.ducked {
		return nil // already muted; keep the values saved the first time
	}
	if err := x.dial(); err != nil {
		return err
	}

	// Ask about every channel before muting any. A channel the mixer does not
	// answer for is assumed to have been on, which is both the normal state
	// and the safe guess: the alternative leaves a channel muted after
	// dictation with nothing in the interface to explain it.
	saved, missing := x.query()
	if len(missing) > 0 {
		x.log.Warn("xr18: mixer did not report these channels; assuming they were unmuted",
			"channels", missing, "addr", x.addr, "timeout", x.timeout)
	}

	var errs []error
	for _, ch := range x.channels {
		if err := x.send(mixOnAddr(ch), 0); err != nil {
			errs = append(errs, err)
		}
	}
	// Ducked even on a partial failure: some channels are muted now, and
	// Restore is the only thing that will put them back.
	x.saved = saved
	x.ducked = true
	return errors.Join(errs...)
}

// Restore sets each channel back to the value it had before Duck.
func (x *XR18Ducker) Restore() error {
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
	for _, ch := range x.channels {
		on, ok := saved[ch]
		if !ok {
			on = 1
		}
		if err := x.send(mixOnAddr(ch), on); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close releases the socket. The ducker is usable again afterwards: the next
// Duck or Restore dials a new one.
func (x *XR18Ducker) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.closeConn()
}

func (x *XR18Ducker) closeConn() error {
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
func (x *XR18Ducker) dial() error {
	if x.conn != nil {
		return nil
	}
	ua, err := net.ResolveUDPAddr("udp", x.addr)
	if err != nil {
		return fmt.Errorf("xr18: resolve %s: %w", x.addr, err)
	}
	c, err := net.DialUDP("udp", nil, ua)
	if err != nil {
		return fmt.Errorf("xr18: dial %s: %w", x.addr, err)
	}
	x.conn = c
	return nil
}

// query asks for every channel's current `mix/on` and returns what came back,
// plus the channels that did not answer in time.
//
// Every question goes out before the first answer is read. Waiting for each
// reply in turn would cost one round trip per channel, and on an unreachable
// mixer one whole timeout per channel — with two channels that is nearly half
// a second added to the start of every dictation.
func (x *XR18Ducker) query() (map[int]int32, []int) {
	want := map[string]int{}
	for _, ch := range x.channels {
		want[mixOnAddr(ch)] = ch
	}
	for _, ch := range x.channels {
		if err := x.sendRaw(encodeQuery(mixOnAddr(ch))); err != nil {
			x.log.Debug("xr18: query failed", "channel", ch, "err", err)
		}
	}

	got := map[int]int32{}
	deadline := time.Now().Add(x.timeout)
	_ = x.conn.SetReadDeadline(deadline)
	buf := make([]byte, 512)
	for len(got) < len(want) && time.Now().Before(deadline) {
		n, err := x.conn.Read(buf)
		if err != nil {
			break // deadline, or the socket went away; the caller reports it
		}
		addr, val, ok := decodeIntReply(buf[:n])
		if !ok {
			continue
		}
		if ch, wanted := want[addr]; wanted {
			got[ch] = val
		}
	}
	_ = x.conn.SetReadDeadline(time.Time{})

	var missing []int
	for _, ch := range x.channels {
		if _, ok := got[ch]; !ok {
			missing = append(missing, ch)
		}
	}
	return got, missing
}

// Probe asks the mixer what the configured channels are set to, changing
// nothing. It is what `mavor doctor` reports: OSC is UDP, so a mixer at the
// wrong address, on another VLAN or simply switched off is indistinguishable
// from a working one until something asks it a question and waits.
//
// Returns each channel that answered with its `mix/on` value, and the
// channels that did not.
func (x *XR18Ducker) Probe() (on map[int]int32, missing []int, err error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := x.dial(); err != nil {
		return nil, nil, err
	}
	got, missed := x.query()
	return got, missed, nil
}

func (x *XR18Ducker) send(addr string, val int32) error {
	return x.sendRaw(encodeInt(addr, val))
}

func (x *XR18Ducker) sendRaw(msg []byte) error {
	if x.conn == nil {
		return errors.New("xr18: not connected")
	}
	if _, err := x.conn.Write(msg); err != nil {
		// A write error means this socket is finished. Drop it so the next
		// call dials again rather than failing forever against a dead one.
		_ = x.closeConn()
		return fmt.Errorf("xr18: send to %s: %w", x.addr, err)
	}
	return nil
}

// mixOnAddr is the OSC address of a channel's mute switch. The channel is
// zero-padded to two digits: `/ch/07/mix/on`, never `/ch/7/mix/on`.
func mixOnAddr(ch int) string { return fmt.Sprintf("/ch/%02d/mix/on", ch) }

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
// X Air firmware is asked for a parameter's current value.
func encodeQuery(addr string) []byte { return padOSCString(addr) }

// encodeInt is an OSC message setting addr to a single 32-bit integer.
func encodeInt(addr string, val int32) []byte {
	msg := padOSCString(addr)
	msg = append(msg, padOSCString(",i")...)
	msg = binary.BigEndian.AppendUint32(msg, uint32(val))
	return msg
}

// decodeIntReply reads an OSC message carrying one integer, which is what the
// mixer answers a `/ch/NN/mix/on` query with. Anything else — a different
// type, a bundle, a truncated packet — is reported as not ok rather than
// guessed at.
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
