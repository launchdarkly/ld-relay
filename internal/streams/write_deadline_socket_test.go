package streams

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems"

	"github.com/launchdarkly/ld-relay/v9/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v9/internal/concurrency"
	"github.com/launchdarkly/ld-relay/v9/internal/credential"
	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the write deadline that the Main.ClientWrite* options configure (the
// stream shape of initwrite). Like the tests in stream_provider_socket_test.go, they use real
// sockets, so SetWriteDeadline reaches a real net.Conn.

// streamUnitLimits is a 64 KiB/s floor with a short slack, so a stalled client is cut quickly.
var streamUnitLimits = initwrite.Limits{MinBytesPerSecond: 64 * 1024, Slack: 300 * time.Millisecond} //nolint:gochecknoglobals // test-only constant

type servedStream struct {
	srv             *httptest.Server
	esp             EnvStreamProvider
	handlerReturned chan struct{}
}

// serveServerSideStreamV2 serves HandlerV2 of a server-side stream provider with the given
// options. The handler is the actual chain, HandlerV2 -> withInitDeadline -> eventsource ->
// initwrite. If tiny is true, each accepted connection gets a tiny send buffer. If h2 is true,
// the server negotiates HTTP/2 over TLS.
func serveServerSideStreamV2(t *testing.T, tiny, h2 bool, opts ...Option) servedStream {
	t.Helper()
	sp := NewStreamProvider(basictypes.ServerSideStream, 0, 0, opts...).(*serverSideStreamProvider)
	esp := sp.RegisterV2(testSDKKey, makeMockStore(manyFlags(400), nil), slog.Default())
	require.NotNil(t, esp)

	s := servedStream{esp: esp, handlerReturned: make(chan struct{}, 8)}
	inner := sp.HandlerV2(testSDKKey)
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { s.handlerReturned <- struct{}{} }()
		inner(w, r)
	}))
	if tiny {
		s.srv.Listener = smallSndbufListener{s.srv.Listener}
	}
	if h2 {
		s.srv.EnableHTTP2 = true
		s.srv.StartTLS()
	} else {
		s.srv.Start()
	}
	t.Cleanup(func() { s.srv.Close(); esp.Close(); sp.Close() })
	return s
}

// dialTinyClient opens a connection with a tiny receive buffer, sends a stream request, and
// reads the response headers.
func dialTinyClient(t *testing.T, url string) (net.Conn, *bufio.Reader) {
	t.Helper()
	dialer := &net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 2048)
		})
	}}
	conn, err := dialer.Dial("tcp", strings.TrimPrefix(url, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nAuthorization: %s\r\nAccept: text/event-stream\r\n\r\n", testSDKKey)
	require.NoError(t, err)
	br := bufio.NewReader(conn)
	readHeaders(t, br)
	return conn, br
}

// readUntilBasisEnds reads the stream until the FDv2 basis is complete.
func readUntilBasisEnds(t *testing.T, br *bufio.Reader) {
	t.Helper()
	for {
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		if strings.Contains(line, "payload-transferred") {
			_, err = br.ReadString('\n') // the data line
			require.NoError(t, err)
			return
		}
	}
}

func bigFlagJSON(version int) []byte {
	return []byte(fmt.Sprintf(
		`{"key":"f","version":%d,"on":false,"variations":[%q,true],"offVariation":0,"fallthrough":{"variation":0},"salt":"s"}`,
		version, strings.Repeat("x", 64*1024)))
}

func applyFlag(t *testing.T, esp EnvStreamProvider, version int, flagJSON []byte) {
	t.Helper()
	cs, err := subsystems.NewChangeSetBuilder().
		Start(subsystems.ServerIntent{Payload: subsystems.Payload{ID: "s", Target: version, Code: subsystems.IntentTransferChanges, Reason: "stale"}}).
		AddPut(subsystems.FlagKind, "f", version, flagJSON).
		Finish(subsystems.NewSelector("s", version))
	require.NoError(t, err)
	esp.Apply(*cs)
}

func waitForHandlerReturn(t *testing.T, s servedStream, within time.Duration, msg string) {
	t.Helper()
	select {
	case <-s.handlerReturned:
	case <-time.After(within):
		require.Fail(t, msg)
	}
}

func TestWriteDeadlineCutsStalledInitialDeliveryWithoutLimiter(t *testing.T) {
	s := serveServerSideStreamV2(t, true, false, WithWriteDeadline(&streamUnitLimits))
	conn, _ := dialTinyClient(t, s.srv.URL)
	_ = conn

	// The client never reads past the headers. The basis is about 100 KB, so its deadline is
	// under 2.5s after the delivery starts.
	waitForHandlerReturn(t, s, 4*time.Second, "the stalled initial delivery was not cut")
}

func TestWriteDeadlineCutsStalledLiveStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"without limiter", nil},
		{"with limiter", []Option{WithInitLimiter(concurrency.New("t", concurrency.Params{MaxConcurrent: 4, MaxQueued: 4}), time.Minute)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := serveServerSideStreamV2(t, true, false, append(tc.opts, WithWriteDeadline(&streamUnitLimits))...)
			_, br := dialTinyClient(t, s.srv.URL)
			// The client reads the whole initial delivery, so it is healthy until it stops
			// reading. Then the live deltas, which have no deadline without the option, must
			// be cut.
			readUntilBasisEnds(t, br)
			for v := 2; v < 52; v++ {
				applyFlag(t, s.esp, v, bigFlagJSON(v))
			}
			waitForHandlerReturn(t, s, 4*time.Second, "the stalled live stream was not cut")
		})
	}
}

func TestWriteDeadlineCutsHalfClosedClientWithoutLimiter(t *testing.T) {
	// A long slack: within this test's window, only the cut on the request context (not the
	// deadline) can end the blocked write.
	s := serveServerSideStreamV2(t, true, false, WithWriteDeadline(&initwrite.Limits{MinBytesPerSecond: 64 * 1024, Slack: 30 * time.Second}))
	conn, _ := dialTinyClient(t, s.srv.URL)
	// Let the delivery get into its blocked write before the half-close.
	time.Sleep(200 * time.Millisecond)
	require.NoError(t, conn.(*net.TCPConn).CloseWrite())

	waitForHandlerReturn(t, s, 2500*time.Millisecond, "the handler kept writing to a half-closed client")
}

func TestWriteDeadlineKeepsIdleStreamOpen(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("http2=%t", h2), func(t *testing.T) {
			s := serveServerSideStreamV2(t, false, h2, WithWriteDeadline(&streamUnitLimits))
			req, err := http.NewRequest(http.MethodGet, s.srv.URL, nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", string(testSDKKey))
			req.Header.Set("Accept", "text/event-stream")
			resp, err := s.srv.Client().Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, map[bool]int{false: 1, true: 2}[h2], resp.ProtoMajor)

			puts := make(chan struct{}, 16)
			go func() {
				sc := bufio.NewScanner(resp.Body)
				sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
				for sc.Scan() {
					if strings.Contains(sc.Text(), "put-object") {
						puts <- struct{}{}
					}
				}
			}()
			waitForPut := func() {
				t.Helper()
				select {
				case <-puts:
				case <-time.After(3 * time.Second):
					require.Fail(t, "the stream stopped delivering")
				}
			}
			// The initial delivery has a put-object for each flag. Drain them.
			for i := 0; i < 400; i++ {
				waitForPut()
			}

			// Each gap is several times the slack. A deadline left set after a unit would end
			// the stream during the gap: on HTTP/2 it fires by itself, and on HTTP/1 the next
			// write fails.
			for v := 2; v < 5; v++ {
				time.Sleep(4 * streamUnitLimits.Slack)
				applyFlag(t, s.esp, v, mustFlagJSON(v))
				waitForPut()
			}
			select {
			case <-s.handlerReturned:
				assert.Fail(t, "the idle stream was cut")
			default:
			}
		})
	}
}

func TestWriteDeadlineAppliesToEveryStreamKind(t *testing.T) {
	for _, tc := range []struct {
		kind basictypes.StreamKind
		cred credential.SDKCredential
	}{
		{basictypes.ServerSideStream, testSDKKey},
		{basictypes.ServerSideFlagsOnlyStream, testSDKKey},
		{basictypes.MobilePingStream, testMobileKey},
		{basictypes.JSClientPingStream, testEnvID},
	} {
		for _, withOption := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/option=%t", tc.kind, withOption), func(t *testing.T) {
				var opts []Option
				if withOption {
					opts = append(opts, WithWriteDeadline(&streamUnitLimits))
				}
				sp := NewStreamProvider(tc.kind, 0, 0, opts...)
				defer sp.Close()
				for _, h := range []http.HandlerFunc{sp.HandlerV1(tc.cred), sp.HandlerV2(tc.cred)} {
					require.NotNil(t, h)
					rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
					ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
					h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))
					cancel()
					// The header flush is a unit, so the option always sets a deadline.
					if withOption {
						assert.NotZero(t, rec.count())
					} else {
						assert.Zero(t, rec.count())
					}
				}
			})
		}
	}
}
