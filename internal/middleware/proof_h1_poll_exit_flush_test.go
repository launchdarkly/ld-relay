package middleware

import (
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func proofStuckInFinishRequest() bool {
	buf := make([]byte, 1<<20)
	return strings.Contains(string(buf[:runtime.Stack(buf, true)]), "response).finishRequest")
}

// A keep-alive client pipelines small polls and never reads. Each response fits in net/http's
// buffers, so the handler's own writes never reach the socket; the socket write happens in
// finishRequest, after WriteDeadline's deferred clear, and blocks with no deadline.
func TestProofH1PipelinedPollExitFlushUnbounded(t *testing.T) {
	srv := httptest.NewServer(WriteDeadline(proofLimits)(http.HandlerFunc(proofBody)))
	defer srv.Close()
	d := net.Dialer{Control: nil}
	c, err := d.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.(*net.TCPConn).SetReadBuffer(4096)
	go func() {
		req := []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")
		for i := 0; i < 20000; i++ {
			if _, err := c.Write(req); err != nil {
				return
			}
		}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if proofStuckInFinishRequest() {
			time.Sleep(3 * time.Second) // well past the 500ms slack
			if proofStuckInFinishRequest() {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("expected a handler goroutine parked in finishRequest")
}
