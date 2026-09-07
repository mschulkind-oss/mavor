package audio

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeMixer is a UDP server that speaks OSC the way X Air firmware does: it
// answers a bare address with the integer it holds for it, and records the
// sets it is told to make. It is keyed by OSC address rather than by channel,
// because the ducker under test knows nothing about channels.
//
// A real socket rather than an interface: the thing worth testing here is the
// wire format, and a fake that took Go structs would agree with the encoder by
// construction whether or not either matched what a device expects.
type fakeMixer struct {
	conn *net.UDPConn

	mu sync.Mutex
	// values is the current integer at each OSC address.
	values map[string]int32
	// sets is every set that arrived, in order.
	sets []mixerSet
	// silent makes the mixer accept messages and answer nothing, which is
	// what an unreachable or wedged device looks like from the client side.
	silent bool
	// refused are addresses it ignores queries for, the way a device does for
	// a parameter it does not have.
	refused map[string]bool
}

type mixerSet struct {
	addr string
	val  int32
}

// startFakeMixer starts a mixer holding the given channel values, expressed as
// channel numbers for readability.
func startFakeMixer(t *testing.T, channels map[int]int32) *fakeMixer {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	m := &fakeMixer{conn: c, values: map[string]int32{}}
	for ch, v := range channels {
		m.values[fmt.Sprintf("/ch/%02d/mix/on", ch)] = v
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
		if len(rest) == 0 { // a query
			silent, val := m.silent || m.refused[addr], m.values[addr]
			m.mu.Unlock()
			if !silent {
				_, _ = m.conn.WriteToUDP(encodeInt(addr, val), from)
			}
			continue
		}
		if a, v, ok := decodeIntReply(pkt); ok {
			m.sets = append(m.sets, mixerSet{addr: a, val: v})
			m.values[a] = v
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

// refuse makes the mixer ignore queries for one address while still answering
// the rest, which is what a path the device does not implement looks like.
func (m *fakeMixer) refuse(addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refused == nil {
		m.refused = map[string]bool{}
	}
	m.refused[addr] = true
}

func (m *fakeMixer) setRaw(addr string, v int32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[addr] = v
}

func (m *fakeMixer) rawValue(addr string) int32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.values[addr]
}

// value reads a channel's mute switch, for tests written in mixer terms.
func (m *fakeMixer) value(ch int) int32 {
	return m.rawValue(fmt.Sprintf("/ch/%02d/mix/on", ch))
}

func (m *fakeMixer) setsSoFar() []mixerSet {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]mixerSet, len(m.sets))
	copy(out, m.sets)
	return out
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

// chanPaths turns channel numbers into the X Air addresses the fake mixer
// answers on, so the tests read in the terms the hardware uses while the
// ducker itself only ever sees OSC paths.
func chanPaths(chans ...int) []string {
	var out []string
	for _, c := range chans {
		out = append(out, fmt.Sprintf("/ch/%02d/mix/on", c))
	}
	return out
}

func newTestDucker(t *testing.T, m *fakeMixer, channels []int, timeout time.Duration) *OSCDucker {
	t.Helper()
	d, err := NewOSCDucker("127.0.0.1", m.port(), chanPaths(channels...), 0, timeout)
	if err != nil {
		t.Fatalf("NewOSCDucker: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOSCDuckMutesEveryConfiguredChannel(t *testing.T) {
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
func TestOSCRestorePutsBackWhatWasThereNotJustOn(t *testing.T) {
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

// A device that is powered off must cost one timeout rather than one per
// path, must not stop the dictation, and — the part that matters — must not
// be muted at all. A parameter whose old value was never read is one Restore
// could not put back, so Duck leaves it alone.
func TestOSCSilentDeviceIsLeftAloneAndCostsOneTimeout(t *testing.T) {
	m := startFakeMixer(t, map[int]int32{15: 1, 16: 1})
	m.goSilent()
	const timeout = 150 * time.Millisecond
	d := newTestDucker(t, m, []int{15, 16}, timeout)

	start := time.Now()
	if err := d.Duck(); err != nil {
		t.Fatalf("Duck against a silent device should not fail: %v", err)
	}
	elapsed := time.Since(start)

	// One timeout, not len(paths) of them. The margin is generous; the
	// assertion that matters is that it did not scale with the path count.
	if elapsed > timeout*2 {
		t.Errorf("Duck took %s against a silent device with a %s timeout — "+
			"the queries are not being sent before the replies are read", elapsed, timeout)
	}

	// Nothing was written, because nothing could have been put back.
	time.Sleep(50 * time.Millisecond)
	if got := m.setsSoFar(); len(got) != 0 {
		t.Errorf("Duck sent %v to a device that never answered; want nothing muted "+
			"— an unreadable parameter is one Restore cannot undo", got)
	}
	if got := m.value(15); got != 1 {
		t.Errorf("channel 15 = %d, want it untouched at 1", got)
	}

	if err := d.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := m.setsSoFar(); len(got) != 0 {
		t.Errorf("Restore sent %v, want nothing — it muted nothing to begin with", got)
	}
}

// A device that answers for some paths and not others mutes exactly the ones
// it answered for.
func TestOSCPartialAnswerMutesOnlyWhatItRead(t *testing.T) {
	m := startFakeMixer(t, map[int]int32{15: 1})
	// Channel 16 is not in the mixer's map, but it still answers for it with
	// the zero value, so give this test a path the fake will never know.
	d, err := NewOSCDucker("127.0.0.1", m.port(),
		[]string{"/ch/15/mix/on", "/nope/does/not/exist"}, 0, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("NewOSCDucker: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// Make the fake refuse just that one address by answering nothing for it.
	m.refuse("/nope/does/not/exist")

	if err := d.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	sets := m.waitForSets(t, 1)
	time.Sleep(50 * time.Millisecond)
	if got := m.setsSoFar(); len(got) != 1 {
		t.Fatalf("Duck sent %v, want only the path that answered", got)
	}
	if sets[0].addr != "/ch/15/mix/on" || sets[0].val != 0 {
		t.Errorf("set = %+v, want /ch/15/mix/on = 0", sets[0])
	}
}

func TestOSCDuckIsIdempotentAndRestoreWithoutDuckIsANoop(t *testing.T) {
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

func TestNewOSCDuckerRejectsNonsense(t *testing.T) {
	for _, tc := range []struct {
		name  string
		host  string
		port  int
		paths []string
	}{
		{"no address", "", 10024, []string{"/ch/15/mix/on"}},
		{"no paths", "192.168.1.6", 10024, nil},
		{"a path that is not an OSC address", "192.168.1.6", 10024, []string{"ch/15/mix/on"}},
		{"an empty path", "192.168.1.6", 10024, []string{""}},
		{"impossible port", "192.168.1.6", 99999, []string{"/ch/15/mix/on"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewOSCDucker(tc.host, tc.port, tc.paths, 0, 0); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}

func TestNewOSCDuckerDefaultsAndDeduplicates(t *testing.T) {
	d, err := NewOSCDucker("192.168.1.6", 0,
		[]string{"/ch/16/mix/on", "/ch/15/mix/on", "/ch/16/mix/on"}, 0, 0)
	if err != nil {
		t.Fatalf("NewOSCDucker: %v", err)
	}
	if want := "192.168.1.6:10024"; d.Addr() != want {
		t.Errorf("Addr() = %q, want %q", d.Addr(), want)
	}
	// Config order, not sorted: the log should read the way the file does.
	got := d.Paths()
	want := []string{"/ch/16/mix/on", "/ch/15/mix/on"}
	if len(got) != len(want) {
		t.Fatalf("Paths() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Paths() = %v, want %v", got, want)
			break
		}
	}
}

// Nothing in the ducker is mixer-specific: a device whose "off" is 1 and
// whose addresses look nothing like a mixer's works the same way.
func TestOSCDuckerIsNotMixerSpecific(t *testing.T) {
	m := startFakeMixer(t, nil)
	m.setRaw("/amp/standby", 0)
	d, err := NewOSCDucker("127.0.0.1", m.port(), []string{"/amp/standby"}, 1, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("NewOSCDucker: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := d.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	m.waitForSets(t, 1)
	if got := m.rawValue("/amp/standby"); got != 1 {
		t.Errorf("/amp/standby = %d after Duck, want 1 (the configured muted_value)", got)
	}
	if err := d.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	m.waitForSets(t, 2)
	if got := m.rawValue("/amp/standby"); got != 0 {
		t.Errorf("/amp/standby = %d after Restore, want 0", got)
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
