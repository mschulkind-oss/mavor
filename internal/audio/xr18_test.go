package audio

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeMixer is a UDP server that speaks just enough of the X Air OSC dialect
// to stand in for a mixer: it answers `/ch/NN/mix/on` queries with the value
// it holds and records the sets it is told to make.
//
// A real socket rather than an interface: the thing worth testing here is the
// wire format, and a fake that took Go structs would agree with the encoder by
// construction whether or not either matched what a mixer expects.
type fakeMixer struct {
	conn *net.UDPConn

	mu sync.Mutex
	// on is the current value per channel.
	on map[int]int32
	// sets is every set that arrived, in order.
	sets []mixerSet
	// silent makes the mixer accept messages and answer nothing, which is
	// what an unreachable or wedged mixer looks like from the client side.
	silent bool
}

type mixerSet struct {
	addr string
	val  int32
}

func startFakeMixer(t *testing.T, initial map[int]int32) *fakeMixer {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	m := &fakeMixer{conn: c, on: map[int]int32{}}
	for k, v := range initial {
		m.on[k] = v
	}
	go m.serve()
	t.Cleanup(func() { _ = c.Close() })
	return m
}

func (m *fakeMixer) serve() {
	buf := make([]byte, 1024)
	for {
		n, from, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		addr, rest, ok := readOSCString(pkt)
		if !ok {
			continue
		}
		m.mu.Lock()
		silent := m.silent
		if len(rest) == 0 {
			// A query: answer with the value held for that channel.
			ch, ok := channelOf(addr)
			val := m.on[ch]
			m.mu.Unlock()
			if ok && !silent {
				_, _ = m.conn.WriteToUDP(encodeInt(addr, val), from)
			}
			continue
		}
		// A set.
		if a, v, ok := decodeIntReply(pkt); ok {
			m.sets = append(m.sets, mixerSet{addr: a, val: v})
			if ch, ok := channelOf(a); ok {
				m.on[ch] = v
			}
		}
		m.mu.Unlock()
	}
}

func (m *fakeMixer) port() int { return m.conn.LocalAddr().(*net.UDPAddr).Port }

func (m *fakeMixer) goSilent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.silent = true
}

func (m *fakeMixer) setsSoFar() []mixerSet {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]mixerSet, len(m.sets))
	copy(out, m.sets)
	return out
}

func (m *fakeMixer) value(ch int) int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.on[ch]
}

// waitForSets blocks until the mixer has seen n sets, so a test never races
// the UDP delivery of a message the ducker has already written.
func (m *fakeMixer) waitForSets(t *testing.T, n int) []mixerSet {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := m.setsSoFar(); len(s) >= n {
			return s
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("mixer saw %d sets, want %d", len(m.setsSoFar()), n)
	return nil
}

// channelOf pulls the channel number out of "/ch/NN/mix/on".
func channelOf(addr string) (int, bool) {
	var ch int
	if _, err := fmt.Sscanf(addr, "/ch/%d/mix/on", &ch); err != nil {
		return 0, false
	}
	return ch, true
}

func newTestDucker(t *testing.T, m *fakeMixer, channels []int, timeout time.Duration) *XR18Ducker {
	t.Helper()
	d, err := NewXR18Ducker("127.0.0.1", m.port(), channels, timeout)
	if err != nil {
		t.Fatalf("NewXR18Ducker: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestXR18DuckMutesEveryConfiguredChannel(t *testing.T) {
	m := startFakeMixer(t, map[int]int32{15: 1, 16: 1})
	d := newTestDucker(t, m, []int{15, 16}, 500*time.Millisecond)

	if err := d.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	sets := m.waitForSets(t, 2)

	want := []mixerSet{{"/ch/15/mix/on", 0}, {"/ch/16/mix/on", 0}}
	for i, w := range want {
		if sets[i] != w {
			t.Errorf("set %d = %+v, want %+v", i, sets[i], w)
		}
	}
	if !d.IsDucked() {
		t.Error("IsDucked() = false after Duck")
	}
}

// The point of querying before muting: a channel the user had already muted
// must not be switched on by a dictation ending.
func TestXR18RestorePutsBackWhatWasThereNotJustOn(t *testing.T) {
	m := startFakeMixer(t, map[int]int32{15: 1, 16: 0})
	d := newTestDucker(t, m, []int{15, 16}, 500*time.Millisecond)

	if err := d.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	m.waitForSets(t, 2)
	if err := d.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	m.waitForSets(t, 4)

	if got := m.value(15); got != 1 {
		t.Errorf("channel 15 = %d after restore, want 1 (it was on before)", got)
	}
	if got := m.value(16); got != 0 {
		t.Errorf("channel 16 = %d after restore, want 0 — it was muted before mavor touched it", got)
	}
	if d.IsDucked() {
		t.Error("IsDucked() = true after Restore")
	}
}

// A mixer that is powered off must cost one timeout, not one per channel, and
// must not stop the dictation.
func TestXR18SilentMixerStillMutesAndCostsOneTimeout(t *testing.T) {
	m := startFakeMixer(t, map[int]int32{15: 1, 16: 1})
	m.goSilent()
	const timeout = 150 * time.Millisecond
	d := newTestDucker(t, m, []int{15, 16}, timeout)

	start := time.Now()
	if err := d.Duck(); err != nil {
		t.Fatalf("Duck on a silent mixer should still succeed: %v", err)
	}
	elapsed := time.Since(start)

	// One timeout, not len(channels) of them. The margin is generous; the
	// assertion that matters is that it did not scale with the channel count.
	if elapsed > timeout*2 {
		t.Errorf("Duck took %s against a silent mixer with a %s timeout — "+
			"the queries are not being sent before the replies are read", elapsed, timeout)
	}
	sets := m.waitForSets(t, 2)
	for _, s := range sets {
		if s.val != 0 {
			t.Errorf("set %+v: want the channel muted even though the mixer never answered", s)
		}
	}

	// An unanswered channel is assumed to have been on, so it comes back on.
	if err := d.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	m.waitForSets(t, 4)
	if got := m.value(15); got != 1 {
		t.Errorf("channel 15 = %d after restore, want 1", got)
	}
}

func TestXR18DuckIsIdempotentAndRestoreWithoutDuckIsANoop(t *testing.T) {
	m := startFakeMixer(t, map[int]int32{15: 1})
	d := newTestDucker(t, m, []int{15}, 500*time.Millisecond)

	if err := d.Restore(); err != nil {
		t.Fatalf("Restore before Duck: %v", err)
	}
	if got := len(m.setsSoFar()); got != 0 {
		t.Fatalf("Restore before Duck sent %d messages, want 0", got)
	}

	if err := d.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	m.waitForSets(t, 1)
	if err := d.Duck(); err != nil {
		t.Fatalf("second Duck: %v", err)
	}
	// A second Duck must not re-query and overwrite the saved value with the
	// muted one it just wrote, which would leave the channel muted forever.
	time.Sleep(50 * time.Millisecond)
	if got := len(m.setsSoFar()); got != 1 {
		t.Errorf("two Ducks sent %d sets, want 1", got)
	}
	if err := d.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	m.waitForSets(t, 2)
	if got := m.value(15); got != 1 {
		t.Errorf("channel 15 = %d after restore, want 1", got)
	}
}

func TestNewXR18DuckerRejectsNonsense(t *testing.T) {
	for _, tc := range []struct {
		name     string
		host     string
		port     int
		channels []int
	}{
		{"no address", "", 10024, []int{15}},
		{"no channels", "192.168.1.6", 10024, nil},
		{"channel zero", "192.168.1.6", 10024, []int{0}},
		{"channel past an X32", "192.168.1.6", 10024, []int{33}},
		{"impossible port", "192.168.1.6", 99999, []int{15}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewXR18Ducker(tc.host, tc.port, tc.channels, 0); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}

func TestNewXR18DuckerDefaultsAndDeduplicates(t *testing.T) {
	d, err := NewXR18Ducker("192.168.1.6", 0, []int{16, 15, 16}, 0)
	if err != nil {
		t.Fatalf("NewXR18Ducker: %v", err)
	}
	if want := "192.168.1.6:10024"; d.Addr() != want {
		t.Errorf("Addr() = %q, want %q", d.Addr(), want)
	}
	got := d.Channels()
	if len(got) != 2 || got[0] != 15 || got[1] != 16 {
		t.Errorf("Channels() = %v, want [15 16]", got)
	}
}

// The wire format is the part a fake cannot check for us, so it is pinned
// against bytes counted by hand from the OSC spec.
func TestOSCEncoding(t *testing.T) {
	t.Run("a string already a multiple of four still gets a whole pad word", func(t *testing.T) {
		// "/ch" is 3 bytes; with its terminator that is 4, so no extra pad.
		if got, want := len(padOSCString("/ch")), 4; got != want {
			t.Errorf("len = %d, want %d", got, want)
		}
		// "/ch/" is 4 bytes, so the terminator forces a second word.
		if got, want := len(padOSCString("/ch/")), 8; got != want {
			t.Errorf("len = %d, want %d", got, want)
		}
	})

	t.Run("set message", func(t *testing.T) {
		got := encodeInt("/ch/15/mix/on", 0)
		want := []byte{
			'/', 'c', 'h', '/', '1', '5', '/', 'm', 'i', 'x', '/', 'o', 'n', 0, 0, 0,
			',', 'i', 0, 0,
			0, 0, 0, 0,
		}
		if string(got) != string(want) {
			t.Errorf("encodeInt = % x\nwant        % x", got, want)
		}
	})

	t.Run("query message carries no type tag", func(t *testing.T) {
		got := encodeQuery("/ch/16/mix/on")
		if len(got) != 16 {
			t.Fatalf("len = %d, want 16", len(got))
		}
		if _, rest, ok := readOSCString(got); !ok || len(rest) != 0 {
			t.Errorf("query should be an address and nothing else, got %d trailing bytes", len(rest))
		}
	})

	t.Run("round trip", func(t *testing.T) {
		addr, val, ok := decodeIntReply(encodeInt("/ch/07/mix/on", 1))
		if !ok || addr != "/ch/07/mix/on" || val != 1 {
			t.Errorf("decodeIntReply = %q, %d, %v", addr, val, ok)
		}
	})

	t.Run("junk is rejected rather than guessed at", func(t *testing.T) {
		for _, b := range [][]byte{
			nil,
			[]byte("no-nul-terminator"),
			encodeQuery("/ch/15/mix/on"), // no type tag
			append(padOSCString("/x"), padOSCString(",f")...), // a float, and truncated
		} {
			if _, _, ok := decodeIntReply(b); ok {
				t.Errorf("decodeIntReply(% x) = ok, want not ok", b)
			}
		}
	})
}

func TestDuckersRunEveryMemberAndJoinFailures(t *testing.T) {
	boom := errors.New("boom")
	a := &MockDucker{}
	bad := &MockDucker{DuckErr: boom, RestoreErr: boom}
	c := &MockDucker{}
	ds := Duckers{a, bad, nil, c}

	err := ds.Duck()
	if !errors.Is(err, boom) {
		t.Errorf("Duck error = %v, want it to wrap boom", err)
	}
	// The member after the failing one must still have run: a Duck that
	// stopped early would leave the rest un-ducked with no record of it.
	if !a.IsDucked() || !c.IsDucked() {
		t.Error("a failing ducker stopped the others from ducking")
	}

	if err := ds.Restore(); !errors.Is(err, boom) {
		t.Errorf("Restore error = %v, want it to wrap boom", err)
	}
	if a.IsDucked() || c.IsDucked() {
		t.Error("a failing ducker stopped the others from restoring")
	}
}
