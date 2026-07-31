package streams

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/sdkauth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are real-socket assembled tests: they drive the actual HandlerV2 -> withInitDeadline
// -> eventsource -> initwrite chain over a TCP connection, so SetWriteDeadline reaches a real
// net.Conn. httptest.ResponseRecorder cannot exercise the deadline at all.

// smallSndbufListener sets a tiny SO_SNDBUF on each accepted connection so a write to a
// non-reading client blocks after only a few KB, making the write deadline observable without
// a multi-megabyte payload.
type smallSndbufListener struct{ net.Listener }

func (l smallSndbufListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return c, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		if raw, err := tc.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 2048)
			})
		}
	}
	return c, nil
}

func manyFlags(n int) []ldmodel.FeatureFlag {
	flags := make([]ldmodel.FeatureFlag, n)
	for i := range flags {
		flags[i] = ldbuilders.NewFlagBuilder(fmt.Sprintf("flag-%04d", i)).Version(1).Build()
	}
	return flags
}

// serveStream builds a server-side stream provider with the init limiter enabled and serves
// HandlerV2 over the given listener wrapper (nil for a normal listener). It returns the server
// and the env stream provider (for publishing deltas).
func serveStream(t *testing.T, maxHold time.Duration, flags []ldmodel.FeatureFlag, wrapListener func(net.Listener) net.Listener) (*httptest.Server, EnvStreamProvider) {
	t.Helper()
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 4, MaxQueued: 10})
	sp := NewStreamProvider(basictypes.ServerSideStream, 0, 0, WithInitLimiter(limiter, maxHold)).(*serverSideStreamProvider)
	cred := sdkauth.New(testSDKKey)
	esp := sp.RegisterV2(cred, makeMockStore(flags, nil), slog.Default())
	require.NotNil(t, esp)

	srv := httptest.NewUnstartedServer(sp.HandlerV2(cred))
	if wrapListener != nil {
		srv.Listener = wrapListener(srv.Listener)
	}
	srv.Start()
	t.Cleanup(func() { srv.Close(); esp.Close(); sp.Close() })
	return srv, esp
}

// TestSocketStalledClientIsCutAtDeadline confirms the write deadline still fires: a client
// that stops reading has its connection torn down around maxHold rather than parking a slot.
func TestSocketStalledClientIsCutAtDeadline(t *testing.T) {
	const maxHold = 800 * time.Millisecond
	srv, _ := serveStream(t, maxHold, manyFlags(400), func(l net.Listener) net.Listener {
		return smallSndbufListener{l}
	})

	// A tiny receive buffer on the client means the server's basis write blocks almost
	// immediately once the client stops reading (the kernel can't absorb the payload).
	dialer := &net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 2048)
		})
	}}
	conn, err := dialer.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "GET /?filter= HTTP/1.1\r\nHost: x\r\nAuthorization: %s\r\nAccept: text/event-stream\r\n\r\n", testSDKKey)
	require.NoError(t, err)

	// Read only the status line + headers, then stop reading so the server's basis write blocks.
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		if line == "\r\n" {
			break // end of headers
		}
	}

	// Stall past maxHold, then drain: the server must have closed the connection (EOF) rather
	// than still holding it open mid-basis.
	time.Sleep(maxHold + 700*time.Millisecond)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 32*1024)
	closed := false
	for {
		_, err := conn.Read(buf)
		if err != nil {
			// EOF / reset => server closed the connection at the deadline. A timeout would mean
			// the connection is still open (deadline never fired).
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				closed = false
			} else {
				closed = true
			}
			break
		}
	}
	assert.True(t, closed, "stalled client was not cut by the write deadline (connection still open)")
}

// TestSocketBusyStreamSurvivesPastMaxHold is the C2 regression guard: once the gated basis is
// delivered, the write deadline must be cleared, so a healthy stream that keeps receiving
// deltas past maxHold is NOT cut. If the deadline were scoped to the whole connection (the
// regression), delta writes after maxHold would fail and the client would stop receiving.
func TestSocketBusyStreamSurvivesPastMaxHold(t *testing.T) {
	const maxHold = 700 * time.Millisecond
	srv, esp := serveStream(t, maxHold, []ldmodel.FeatureFlag{ldbuilders.NewFlagBuilder("f").Version(1).Build()}, nil)

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", string(testSDKKey))
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Drain the stream in the background, recording the arrival time of every "put-object" the
	// deltas below produce.
	patchTimes := make(chan time.Time, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "event:") && strings.Contains(sc.Text(), "put-object") {
				select {
				case patchTimes <- time.Now():
				default:
				}
			}
		}
	}()

	start := time.Now()
	// Publish a delta every 150ms for well past maxHold. Each delivers a put-object to the client.
	deadline := time.After(3 * maxHold)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	version := 2
	for stop := false; !stop; {
		select {
		case <-deadline:
			stop = true
		case <-ticker.C:
			cs, err := subsystems.NewChangeSetBuilder().
				Start(subsystems.ServerIntent{Payload: subsystems.Payload{ID: "s", Target: version, Code: subsystems.IntentTransferChanges, Reason: "stale"}}).
				AddPut(subsystems.FlagKind, "f", version, mustFlagJSON(version)).
				Finish(subsystems.NewSelector("s", version))
			require.NoError(t, err)
			esp.Apply(*cs)
			version++
		}
	}

	// A put-object must have arrived AFTER maxHold elapsed -- proof the connection was not cut.
	var afterMaxHold int
	timeout := time.After(time.Second)
	for {
		select {
		case ts := <-patchTimes:
			if ts.Sub(start) > maxHold+200*time.Millisecond {
				afterMaxHold++
			}
		case <-timeout:
			assert.Greater(t, afterMaxHold, 0, "no deltas arrived after maxHold: the busy stream was wrongly cut (C2)")
			return
		default:
			if afterMaxHold > 0 {
				return // success
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func mustFlagJSON(version int) []byte {
	return []byte(fmt.Sprintf(`{"key":"f","version":%d,"on":false,"variations":[false,true],"offVariation":0,"fallthrough":{"variation":0},"salt":"s"}`, version))
}
