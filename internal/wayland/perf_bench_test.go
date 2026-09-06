package wayland

import (
	"encoding/binary"
	"net"
	"syscall"
	"testing"
	"time"
)

// newConnPair returns two Conns wired together over a real AF_UNIX
// SOCK_STREAM socket pair, so DispatchPending exercises the actual
// SetReadDeadline + ReadMsgUnix path production runs, not a fake.
func newConnPair(t testing.TB) (client, server *Conn) {
	t.Helper()
	a, b := socketpair(t)
	client = &Conn{sock: a, nextID: firstClientID, handlers: map[ObjectID]handler{}, readBuf: make([]byte, 8192)}
	server = &Conn{sock: b, nextID: firstClientID, handlers: map[ObjectID]handler{}, readBuf: make([]byte, 8192)}
	return client, server
}

func socketpair(t testing.TB) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	l, err := net.Listen("unix", t.TempDir()+"/sock")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	var serverConn net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := l.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		serverConn = c
	}()

	clientConn, err := net.Dial("unix", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	<-done

	return clientConn.(*net.UnixConn), serverConn.(*net.UnixConn)
}

// deleteIDMessage builds a minimal, well-formed wl_display.delete_id(id)
// event: 12 bytes total, object 1 (wl_display), opcode 1.
func deleteIDMessage(id uint32) []byte {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(displayID))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(12)<<16|uint32(1))
	binary.LittleEndian.PutUint32(buf[8:12], id)
	return buf
}

// BenchmarkDispatchPendingIdle measures DispatchPending when nothing has
// arrived on the socket — the common steady-state case in the render loop's
// ~26.7 Hz tick (pulsePeriod/24) once every buffer has already been released
// and no configure/output event is in flight. This is the case the
// pendingReadWindow comment describes: a positive deadline is needed so a
// release is not silently missed, but that same deadline means every idle
// tick pays for a blocking recvmsg that the kernel cannot answer until either
// data shows up or the 1ms window elapses.
func BenchmarkDispatchPendingIdle(b *testing.B) {
	client, server := newConnPair(b)
	client.watchDisplay()
	defer client.Close()
	defer server.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.DispatchPending(); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	perCall := b.Elapsed() / time.Duration(b.N)
	b.ReportMetric(float64(perCall.Microseconds()), "us/call")
}

// BenchmarkDispatchPendingReady is the other steady-state case: a
// wl_buffer.release (or any other event) is already sitting in the kernel
// buffer when DispatchPending is called, e.g. because the compositor released
// a buffer between ticks. This should return in well under the 1ms window
// since ReadMsgUnix has data immediately available.
func BenchmarkDispatchPendingReady(b *testing.B) {
	client, server := newConnPair(b)
	client.watchDisplay()
	defer client.Close()
	defer server.Close()

	msg := deleteIDMessage(12345)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if _, err := server.sock.Write(msg); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := client.DispatchPending(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDispatchOOBAlloc isolates the ancillary-data buffer allocation in
// Dispatch (conn.go: `oob := make([]byte, syscall.CmsgSpace(4*8))`), which
// runs on every single call — at least once per DispatchPending, which the
// render loop calls on every tick, forever, whether or not the overlay is
// showing anything.
func BenchmarkDispatchOOBAlloc(b *testing.B) {
	b.ReportAllocs()
	var sink []byte
	for i := 0; i < b.N; i++ {
		sink = make([]byte, syscall.CmsgSpace(4*8))
	}
	_ = sink
}

// BenchmarkDispatchSteadyState is Dispatch() in the shape the render loop
// actually exercises it: one small event (e.g. a wl_buffer.release) arriving
// per read, over and over. It exists to show that Dispatch's own allocations
// do not amortize away with repetition — see the finding at conn.go:129,
// :153 and wire.go:158,174.
func BenchmarkDispatchSteadyState(b *testing.B) {
	client, server := newConnPair(b)
	client.watchDisplay()
	defer client.Close()
	defer server.Close()

	msg := deleteIDMessage(1)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if _, err := server.sock.Write(msg); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := client.Dispatch(); err != nil {
			b.Fatal(err)
		}
	}
}
