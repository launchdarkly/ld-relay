package initwrite

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"
)

// proofBurstRun mimics eventsource's live path on a WrapStream writer: the header Flush
// (eventsource server.go:369), then `events` live events, each written and flushed as its own
// unit (server.go:500 -> writeEventOrCommentAndFlush, server.go:301-308), back to back as the
// SSE server does when a burst is published (for example FDv2 SetBasis publishing one event per
// flag, stream_provider_server_side.go SetBasis). The client reads at a steady 2x the floor.
// A small client receive buffer models a bottleneck link, so the backlog queues in the
// server's send buffer.
func proofBurstRun(t *testing.T, events, size int) (gotLast bool, firstErr error, at time.Duration) {
	unit := Limits{MinBytesPerSecond: 64 * 1024, Slack: 5 * time.Second}
	t0 := time.Now()
	errc := make(chan error, 1)
	var maxUnit time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w := WrapStream(rw, unit, Limits{})
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		w.Flush()
		payload := strings.Repeat("x", size)
		for i := 0; i < events; i++ {
			marker := ""
			if i == events-1 {
				marker = "LAST"
			}
			ws := time.Now()
			if _, err := w.WriteString("data: " + payload + marker + "\n\n"); err != nil {
				t.Logf("server write of event %d failed at %v after %v: %v", i, time.Since(t0), time.Since(ws), err)
				errc <- err
				return
			}
			w.Flush()
			if d := time.Since(ws); d > maxUnit {
				maxUnit = d
			}
		}
		t.Logf("max unit %v, all written at %v", maxUnit, time.Since(t0))
		errc <- nil
		<-r.Context().Done()
	}))
	defer srv.Close()

	dialer := &net.Dialer{Control: func(_, _ string, rc syscall.RawConn) error {
		return rc.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 64*1024)
		})
	}}
	c, err := dialer.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	// 128 KiB/s: 4 KiB every 31.25ms.
	buf := make([]byte, 4096)
	var tail []byte
	tick := time.NewTicker(31250 * time.Microsecond)
	defer tick.Stop()
	for time.Since(t0) < 120*time.Second {
		<-tick.C
		n, rerr := c.Read(buf)
		tail = append(tail, buf[:n]...)
		if len(tail) > 64 {
			tail = tail[len(tail)-64:]
		}
		if bytes.Contains(tail, []byte("LAST")) {
			return true, nil, time.Since(t0)
		}
		if rerr != nil {
			select {
			case firstErr = <-errc:
			case <-time.After(time.Second):
			}
			return false, firstErr, time.Since(t0)
		}
	}
	t.Fatal("timed out")
	return
}

// 2200 live events of 2 KiB (4.3 MiB, within eventsource's 128-event subscriber buffer of
// what a 4 MiB send buffer holds) to a client that reads at twice the floor the whole time.
// Each event is its own unit with a fresh 5s budget, but its write has to wait for the
// backlog of the earlier units to drain from the kernel send buffer, which takes longer than
// the budget. The relay cuts the healthy client.
func TestProofLiveBurstCutsClientAtTwiceTheFloor(t *testing.T) {
	got, err, at := proofBurstRun(t, 2200, 2048)
	t.Logf("gotLast=%v serverWriteErr=%v at=%v", got, err, at)
	if got {
		t.Fatal("client received every event; no cut")
	}
}
