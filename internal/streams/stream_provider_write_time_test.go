package streams

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http/httptest"
	neturl "net/url"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v8/internal/logging"
	"github.com/launchdarkly/ld-relay/v8/internal/sdkauth"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldmodel"

	"github.com/stretchr/testify/require"
)

// unreadStreamConn sends a stream request, consumes the response headers, then never reads
// again, like a client behind a proxy that half-closed the connection.
func unreadStreamConn(t *testing.T, url string) *net.TCPConn {
	t.Helper()
	u, err := neturl.Parse(url)
	require.NoError(t, err)
	c, err := net.Dial("tcp", u.Host)
	require.NoError(t, err)
	conn := c.(*net.TCPConn)
	require.NoError(t, conn.SetReadBuffer(4096))
	_, err = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nAccept: text/event-stream\r\n\r\n", u.Host)
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	var head []byte
	buf := make([]byte, 1)
	for !bytes.HasSuffix(head, []byte("\r\n\r\n")) {
		n, err := conn.Read(buf)
		require.NoError(t, err)
		head = append(head, buf[:n]...)
	}
	require.NoError(t, conn.SetReadDeadline(time.Time{}))
	return conn
}

// The initial put for a large environment, written to a client that never reads, must end in
// a logged write timeout even behind the debug request logger that wraps every response.
func TestStreamProviderMaxWriteTimeDisconnectsClientThatStopsReading(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	mockLog.Loggers.SetMinLevel(ldlog.Debug)
	defer mockLog.DumpIfTestFailed(t)

	raw := make([]byte, 8<<20)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	bigFlag := ldbuilders.NewFlagBuilder("big").Version(1).
		Variations(ldvalue.String(base64.StdEncoding.EncodeToString(raw))).Build()

	sp := NewStreamProvider(basictypes.ServerSideStream, StreamProviderSettings{
		MaxWriteTime: 200 * time.Millisecond,
		Loggers:      mockLog.Loggers,
	})
	defer sp.Close()
	credential := sdkauth.New(testSDKKey)
	esp := sp.Register(credential, makeMockStore([]ldmodel.FeatureFlag{bigFlag}, nil), mockLog.Loggers)
	defer esp.Close()

	httpServer := httptest.NewServer(logging.RequestLoggerMiddleware(mockLog.Loggers)(sp.Handler(credential)))
	defer httpServer.Close()
	conn := unreadStreamConn(t, httpServer.URL)
	defer func() {
		_ = conn.SetLinger(0)
		_ = conn.Close()
	}()

	require.Eventually(t, func() bool {
		return mockLog.HasMessageMatch(ldlog.Warn, "took longer than maxClientWriteTime")
	}, 3*time.Second, 10*time.Millisecond, "handler never timed out its write")
}
