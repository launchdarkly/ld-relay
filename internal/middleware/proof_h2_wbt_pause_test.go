package middleware

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"
)

// proofSmallBufListener shrinks the server socket send buffer so a client socket pause turns
// into a TCP stall quickly.
type proofSmallBufListener struct{ net.Listener }

func (l proofSmallBufListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		_ = c.(*net.TCPConn).SetWriteBuffer(16 * 1024)
	}
	return c, err
}

// proofPausableConn stops reading the socket while paused (a network blackout or a frozen
// client process, as seen from the server).
type proofPausableConn struct {
	net.Conn
	paused *atomic.Bool
}

func (c proofPausableConn) Read(p []byte) (int, error) {
	for c.paused.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	return c.Conn.Read(p)
}

// proofPauseRun serves a 3 MiB poll body under WriteDeadline (floor 64 KiB/s, slack 1s, so
// the unit budget is 48s+1s), with the HTTP/2 WriteByteTimeout set to the slack as ld-relay.go
// does. The client reads 1 MiB, stops reading the socket for 3s, then reads the rest. Its
// average rate is far above the floor and it is well inside the unit's cumulative deadline.
func proofPauseRun(t *testing.T, h2 bool) error {
	limits := &initwrite.Limits{MinBytesPerSecond: 64 * 1024, Slack: time.Second}
	body := make([]byte, 3<<20)
	srv := httptest.NewUnstartedServer(WriteDeadline(limits)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	})))
	srv.Listener = proofSmallBufListener{srv.Listener}
	srv.EnableHTTP2 = h2
	srv.Config.HTTP2 = &http.HTTP2Config{WriteByteTimeout: limits.Slack}
	srv.StartTLS()
	defer srv.Close()

	paused := &atomic.Bool{}
	client := srv.Client()
	tr := client.Transport.(*http.Transport)
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		_ = c.(*net.TCPConn).SetReadBuffer(16 * 1024)
		return proofPausableConn{Conn: c, paused: paused}, nil
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if (resp.ProtoMajor == 2) != h2 {
		t.Fatalf("unexpected protocol %s", resp.Proto)
	}
	if _, err := io.CopyN(io.Discard, resp.Body, 1<<20); err != nil {
		return err
	}
	paused.Store(true)
	time.Sleep(3 * time.Second)
	paused.Store(false)
	n, err := io.Copy(io.Discard, resp.Body)
	if err == nil && n != int64(len(body)-(1<<20)) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func TestProofH1SocketPauseWithinBudgetSurvives(t *testing.T) {
	if err := proofPauseRun(t, false); err != nil {
		t.Fatalf("HTTP/1 client cut: %v", err)
	}
}

// On HTTP/2 the same client, inside the same per-unit budget, loses the connection because
// WriteByteTimeout (= slack) closes it after 1s without TCP progress.
func TestProofH2SocketPauseWithinBudgetIsCut(t *testing.T) {
	err := proofPauseRun(t, true)
	if err == nil {
		t.Fatal("expected the HTTP/2 client to be cut")
	}
	t.Logf("HTTP/2 client cut: %v", err)
}
