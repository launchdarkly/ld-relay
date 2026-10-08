package middleware

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"

	"github.com/stretchr/testify/require"
)

type proofEvent struct {
	write    int
	deadline time.Time
	isDL     bool
}

type proofConn struct {
	net.Conn
	mu  *sync.Mutex
	evs *[]proofEvent
}

func (c proofConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	*c.evs = append(*c.evs, proofEvent{write: len(p)})
	c.mu.Unlock()
	return c.Conn.Write(p)
}

func (c proofConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	*c.evs = append(*c.evs, proofEvent{deadline: t, isDL: true})
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(t)
}

type proofListener struct {
	net.Listener
	mu  sync.Mutex
	evs []proofEvent
}

func (l *proofListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return proofConn{Conn: c, mu: &l.mu, evs: &l.evs}, nil
}

// TestProofPollFinalFlushHasNoDeadline shows that WriteDeadline clears the connection's write
// deadline before net/http's finishRequest pushes the buffered tail of the response to the
// socket, so that final socket write is unbounded. A small response is the extreme case:
// every body byte reaches the socket after the clear.
func TestProofPollFinalFlushHasNoDeadline(t *testing.T) {
	limits := initwrite.Limits{MinBytesPerSecond: 64 * 1024, Slack: time.Second}
	h := WriteDeadline(&limits)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 1500))
	}))
	srv := httptest.NewUnstartedServer(h)
	pl := &proofListener{Listener: srv.Listener}
	srv.Listener = pl
	srv.Start()
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	pl.mu.Lock()
	defer pl.mu.Unlock()
	var current time.Time
	sawArm := false
	for _, e := range pl.evs {
		if e.isDL {
			current = e.deadline
			if !e.deadline.IsZero() {
				sawArm = true
			}
			continue
		}
		t.Logf("socket write of %d bytes under deadline %v", e.write, current)
		require.True(t, sawArm, "expected the writer to arm a deadline before any socket write")
		require.True(t, current.IsZero(), "this socket write was bounded; the gap does not reproduce")
	}
	require.NotEmpty(t, pl.evs)
}
