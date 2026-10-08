package middleware

import (
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"
)

func proofH2Frame(typ, flags byte, stream uint32, payload []byte) []byte {
	b := make([]byte, 9+len(payload))
	b[0], b[1], b[2] = byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload))
	b[3], b[4] = typ, flags
	binary.BigEndian.PutUint32(b[5:], stream)
	copy(b[9:], payload)
	return b
}

// proofZeroWindowClient opens an h2 connection that advertises INITIAL_WINDOW_SIZE=0 and never
// sends WINDOW_UPDATE, but keeps reading the socket, so TCP and WriteByteTimeout never stall.
func proofZeroWindowClient(t *testing.T, addr string) net.Conn {
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}}) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	settings := make([]byte, 6)
	binary.BigEndian.PutUint16(settings[0:], 0x4) // INITIAL_WINDOW_SIZE
	binary.BigEndian.PutUint32(settings[2:], 0)
	headers := append([]byte{0x82, 0x87, 0x84, 0x41, byte(len(addr))}, addr...)
	for _, b := range [][]byte{
		[]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"),
		proofH2Frame(0x4, 0, 0, settings),
		proofH2Frame(0x1, 0x4|0x1, 1, headers),
	} {
		if _, err := conn.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	go func() { _, _ = io.Copy(io.Discard, conn) }()
	return conn
}

func proofStuckInHandlerDone() bool {
	buf := make([]byte, 1<<20)
	return strings.Contains(string(buf[:runtime.Stack(buf, true)]), "http2responseWriter).handlerDone")
}

func proofRun(t *testing.T, h http.Handler) bool {
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	c := proofZeroWindowClient(t, srv.Listener.Addr().String())
	defer c.Close()
	time.Sleep(3 * time.Second) // Slack is 500ms
	return proofStuckInHandlerDone()
}

var proofLimits = &initwrite.Limits{MinBytesPerSecond: 64 * 1024, Slack: 500 * time.Millisecond}

func proofBody(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(strings.Repeat("x", 3000))) // below the 4 KiB h2 handler buffer
}

// With the shipped middleware, the deadline is cleared before the h2 handlerDone flush, which
// then blocks on flow control forever.
func TestProofH2PollExitFlushUnbounded(t *testing.T) {
	if !proofRun(t, WriteDeadline(proofLimits)(http.HandlerFunc(proofBody))) {
		t.Fatal("expected handler goroutine parked in handlerDone")
	}
}

// Control: the same writer without the deferred clear bounds the exit flush.
func TestProofH2PollExitFlushBoundedWithoutClear(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proofBody(initwrite.WrapResponse(w, *proofLimits), r)
	})
	if proofRun(t, h) {
		t.Fatal("handler goroutine still parked in handlerDone")
	}
}
