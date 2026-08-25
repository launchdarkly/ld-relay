package streams

import (
	"bufio"
	"context"
	"fmt"
	"io"
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

// These tests use real sockets. They drive the actual chain, HandlerV2 -> withInitDeadline
// -> eventsource -> initwrite, over a TCP connection, so SetWriteDeadline reaches a real
// net.Conn. An httptest.ResponseRecorder cannot examine the deadline at all.

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
func serveStream(t *testing.T, limiter *concurrency.Limiter, maxHold time.Duration, flags []ldmodel.FeatureFlag, fdv1 bool, wrapListener func(net.Listener) net.Listener) (*httptest.Server, EnvStreamProvider) {
	t.Helper()
	sp := NewStreamProvider(basictypes.ServerSideStream, 0, 0, WithInitLimiter(limiter, maxHold)).(*serverSideStreamProvider)
	cred := sdkauth.New(testSDKKey)
	store := makeMockStore(flags, nil)
	var esp EnvStreamProvider
	var h http.HandlerFunc
	if fdv1 {
		esp = sp.RegisterV1(cred, store, slog.Default())
		h = sp.HandlerV1(cred)
	} else {
		esp = sp.RegisterV2(cred, store, slog.Default())
		h = sp.HandlerV2(cred)
	}
	require.NotNil(t, esp)

	srv := httptest.NewUnstartedServer(h)
	if wrapListener != nil {
		srv.Listener = wrapListener(srv.Listener)
	}
	srv.Start()
	t.Cleanup(func() { srv.Close(); esp.Close(); sp.Close() })
	return srv, esp
}

// TestSocketHalfClosedClientDrainEndsWithTheSlot models the hostile shape: a client takes a
// slot, half-closes (FIN), and never reads. The producer releases the slot fast in any case;
// the question is the handler goroutine, which is blocked writing the payload to the dead
// socket. Without the disconnect watcher it drains until the per-chunk write deadline
// (seconds); the watcher must cut it as soon as the request context ends, so the payload's
// memory and egress end with the slot. The observable is the handler's return: the test never
// reads after the half-close, because reading would unblock the write it is measuring.
func TestSocketHalfClosedClientDrainEndsWithTheSlot(t *testing.T) {
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 10})
	sp := NewStreamProvider(basictypes.ServerSideStream, 0, 0,
		// A generous cap: within this test's window, only the watcher's cut (not the
		// per-chunk deadline, ~5s minimum) can end the drain quickly.
		WithInitLimiter(limiter, 30*time.Second)).(*serverSideStreamProvider)
	cred := sdkauth.New(testSDKKey)
	store := makeMockStore(manyFlags(400), nil)
	esp := sp.RegisterV2(cred, store, slog.Default())
	require.NotNil(t, esp)

	handlerReturned := make(chan struct{})
	inner := sp.HandlerV2(cred)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerReturned)
		inner(w, r)
	}))
	srv.Listener = smallSndbufListener{srv.Listener}
	srv.Start()
	t.Cleanup(func() { srv.Close(); esp.Close(); sp.Close() })

	dialer := &net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 2048)
		})
	}}
	conn, err := dialer.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nAuthorization: %s\r\nAccept: text/event-stream\r\n\r\n", testSDKKey)
	require.NoError(t, err)

	// Read the headers only, so the server's basis write blocks on our tiny receive buffer.
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		if line == "\r\n" {
			break
		}
	}
	// Let the delivery get into its blocked write before the half-close.
	time.Sleep(200 * time.Millisecond)

	// Half-close: FIN the write side and never read another byte.
	require.NoError(t, conn.(*net.TCPConn).CloseWrite())
	closedAt := time.Now()

	// The slot comes back quickly regardless of the drain.
	for limiter.Stats().Held != 0 {
		if time.Since(closedAt) > 2*time.Second {
			require.Fail(t, "slot was not released after the half-close")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The drain must end with the slot: the cut fails the blocked write, so the handler
	// returns promptly instead of draining to the per-chunk deadline.
	select {
	case <-handlerReturned:
		assert.Less(t, time.Since(closedAt), 2500*time.Millisecond,
			"the handler outlived the slot by too long; the disconnect cut did not fire")
	case <-time.After(4 * time.Second):
		require.Fail(t, "the handler kept draining to the dead client after the slot was released")
	}
}

// TestSocketStalledClientIsCutAtDeadline makes sure the write deadline fires: the relay
// closes the connection of a client that stops reading at approximately maxHold, and the
// client cannot keep a slot.
func TestSocketStalledClientIsCutAtDeadline(t *testing.T) {
	const maxHold = 800 * time.Millisecond
	srv, _ := serveStream(t, concurrency.New("t", concurrency.Params{MaxConcurrent: 4, MaxQueued: 10}), maxHold, manyFlags(400), false, func(l net.Listener) net.Listener {
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

// TestSocketBusyStreamSurvivesPastMaxHold guards the deadline scope: when the gated basis
// is delivered, the write deadline must be cleared, so the relay does NOT cut a healthy
// stream that continues to receive deltas after maxHold. If the deadline applied to the
// whole connection, the delta writes after maxHold would fail, and the client would stop
// receiving.
func TestSocketBusyStreamSurvivesPastMaxHold(t *testing.T) {
	const maxHold = 700 * time.Millisecond
	srv, esp := serveStream(t, concurrency.New("t", concurrency.Params{MaxConcurrent: 4, MaxQueued: 10}), maxHold, []ldmodel.FeatureFlag{ldbuilders.NewFlagBuilder("f").Version(1).Build()}, false, nil)

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

// TestSocketSlotHeldAcrossSend makes sure the budget slot stays held for the full send, not
// only until the channel handoff. With MaxConcurrent=1 and no queue, a client that stalls in
// the middle of its basis holds the only slot, so the relay sheds a second full-basis client:
// it closes that connection with no basis sent. If the slot were released at the handoff, the
// second client would be admitted, and the slot would limit nothing.
func TestSocketSlotHeldAcrossSend(t *testing.T) {
	const maxHold = 10 * time.Second // long, so A's deadline does not release the slot before B connects
	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 0})
	srv, _ := serveStream(t, limiter, maxHold, manyFlags(400), true, func(l net.Listener) net.Listener {
		return smallSndbufListener{l}
	})
	addr := strings.TrimPrefix(srv.URL, "http://")

	tinyRcvbuf := &net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 2048)
		})
	}}

	// Client A connects, reads only headers, then stalls -- so its basis write blocks and it
	// holds the only slot.
	connA, err := tinyRcvbuf.Dial("tcp", addr)
	require.NoError(t, err)
	defer connA.Close()
	fmt.Fprintf(connA, "GET / HTTP/1.1\r\nHost: x\r\nAuthorization: %s\r\nAccept: text/event-stream\r\n\r\n", testSDKKey)
	readHeaders(t, bufio.NewReader(connA))
	time.Sleep(400 * time.Millisecond) // let A's replay acquire the slot and block on the send

	// Client B asks for a full basis while A holds the slot. It must be shed: its stream ends
	// (the SDK will reconnect) having received no basis. Use an http client so we observe the
	// response body ending rather than the possibly-kept-alive TCP connection.
	ctxB, cancelB := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelB()
	reqB, _ := http.NewRequestWithContext(ctxB, "GET", srv.URL, nil)
	reqB.Header.Set("Authorization", string(testSDKKey))
	reqB.Header.Set("Accept", "text/event-stream")
	respB, err := http.DefaultClient.Do(reqB)
	require.NoError(t, err)
	defer respB.Body.Close()
	body, err := io.ReadAll(respB.Body) // returns when the shed ends the response body
	require.NoError(t, err, "shed client's stream did not end (it was admitted, so the slot was not held) ")
	// An FDv1 basis is an "event: put". A shed client must receive none.
	assert.NotContains(t, string(body), "event: put", "second client got a basis: the slot was NOT held across the first client's send (M1)")
	assert.NotContains(t, string(body), "\"flags\"", "second client got basis data: the slot was NOT held across the first client's send (M1)")
}

func readHeaders(t *testing.T, br *bufio.Reader) {
	t.Helper()
	for {
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		if line == "\r\n" {
			return
		}
	}
}

// TestSocketSlotReleasedAfterHealthyBasis guards against a slot-leak race: when a healthy
// client completes its small basis and stays connected, the writer's Done signal must
// release the slot quickly, and the slot must not stay held until the client disconnects.
// The stall test cannot find this fault, because there the context or the deadline fires in
// each case. The release path races the end-of-basis flush, so this test runs in a loop to
// make an intermittent fault fail reliably.
func TestSocketSlotReleasedAfterHealthyBasis(t *testing.T) {
	// Make the end-of-basis flush win the race with the release select. An old done-channel
	// capture -- a read of iw.Done() after closeOut instead of before -- then sees a nil
	// channel and leaks the slot; a capture before closeOut is safe. Without the seam, the
	// producer almost always wins the race, and the leak would not occur in CI.
	testHookSlowBasisClose.Store(true)
	defer testHookSlowBasisClose.Store(false)

	limiter := concurrency.New("t", concurrency.Params{MaxConcurrent: 1, MaxQueued: 10})
	// maxHold long, so a leaked slot is not masked by the write deadline reclaiming it (the
	// delivery completes, so no deadline fires anyway); release must come from Done.
	srv, _ := serveStream(t, limiter, 30*time.Second, []ldmodel.FeatureFlag{ldbuilders.NewFlagBuilder("f").Version(1).Build()}, true, nil)
	addr := strings.TrimPrefix(srv.URL, "http://")

	leakedAt := -1
	for i := 0; i < 40 && leakedAt < 0; i++ {
		conn, err := net.Dial("tcp", addr)
		require.NoError(t, err)
		fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nAuthorization: %s\r\nAccept: text/event-stream\r\n\r\n", testSDKKey)
		br := bufio.NewReader(conn)
		readHeaders(t, br)
		readPutEvent(t, br) // consume the whole basis, so the producer has acquired and delivered

		// The client stays connected. The slot must now return to 0, released via the writer's
		// Done. If the release raced and read a nil Done channel it would fall through to ctx
		// (this still-open connection) and Held would stay pinned at 1 -- a leaked slot. Because
		// we have read the full basis, the producer has definitely acquired, so an early Held==0
		// cannot mask the leak.
		released := false
		for j := 0; j < 200; j++ {
			if limiter.Stats().Held == 0 {
				released = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !released {
			leakedAt = i
		}
		conn.Close() // always close, even on leak, so the server's Close() at cleanup doesn't block
	}
	require.Less(t, leakedAt, 0, "slot not released after a healthy basis (leaked at iteration %d); the release raced the end-of-basis flush", leakedAt)
}

// readPutEvent consumes SSE lines through the end of the next "put" event (the blank line that
// terminates it), so the caller knows the full basis has been delivered.
func readPutEvent(t *testing.T, br *bufio.Reader) {
	t.Helper()
	sawPut := false
	for {
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		if strings.Contains(line, "put") {
			sawPut = true
		}
		if sawPut && (line == "\n" || line == "\r\n") {
			return
		}
	}
}
