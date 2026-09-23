package application

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/require"
)

func h2Frame(typ, flags byte, stream uint32, payload []byte) []byte {
	b := make([]byte, 9+len(payload))
	b[0], b[1], b[2] = byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload))
	b[3], b[4] = typ, flags
	binary.BigEndian.PutUint32(b[5:], stream)
	copy(b[9:], payload)
	return b
}

// stalledHTTP2Conn opens a TLS HTTP/2 connection, grants the server a huge flow-control
// window, sends one GET, and then never reads the socket. The server's frame writer blocks
// on TCP, so a per-stream write deadline cannot send its stream reset.
func stalledHTTP2Conn(t *testing.T, port int) net.Conn {
	t.Helper()
	var raw net.Conn
	require.Eventually(t, func() bool {
		var err error
		raw, err = net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, raw.(*net.TCPConn).SetReadBuffer(4096))
	conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}}) //nolint:gosec
	require.NoError(t, conn.Handshake())
	require.Equal(t, "h2", conn.ConnectionState().NegotiatedProtocol)

	settings := make([]byte, 6)
	binary.BigEndian.PutUint16(settings[0:], 0x4) // INITIAL_WINDOW_SIZE
	binary.BigEndian.PutUint32(settings[2:], 0x7fffffff)
	windowUpdate := make([]byte, 4)
	binary.BigEndian.PutUint32(windowUpdate, 0x7fffffff-65535)
	authority := fmt.Sprintf("127.0.0.1:%d", port)
	headers := append([]byte{0x82, 0x87, 0x84, 0x41, byte(len(authority))}, authority...) // GET https /
	for _, b := range [][]byte{
		[]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"),
		h2Frame(0x4, 0, 0, settings),
		h2Frame(0x8, 0, 0, windowUpdate),
		h2Frame(0x1, 0x4|0x1, 1, headers), // HEADERS with END_HEADERS|END_STREAM
	} {
		_, err := conn.Write(b)
		require.NoError(t, err)
	}
	return raw
}

func TestStartHTTPServerMaxClientWriteTimeFreesHandlerOnStalledHTTP2Connection(t *testing.T) {
	port := st.GetAvailablePort(t)
	handlerDone := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + strings.Repeat("x", 8<<20) + "\n\n"))
		_ = http.NewResponseController(w).Flush()
	})

	withSelfSignedCert(t, func(certFilePath, keyFilePath string, _ *x509.CertPool) {
		server, _ := StartHTTPServer(port, handler, true, certFilePath, keyFilePath, 0, time.Second,
			500*time.Millisecond, ldlog.NewDisabledLoggers())
		defer server.Close()
		conn := stalledHTTP2Conn(t, port)
		defer conn.Close()

		// Without the connection write timeout the handler stays parked until the kernel gives
		// up on the peer, which takes seconds on macOS and minutes on Linux.
		select {
		case <-handlerDone:
		case <-time.After(3 * time.Second):
			t.Fatal("handler still blocked writing to a stalled HTTP/2 connection")
		}
	})
}

func TestStartHTTPServerSetsHTTP2WriteByteTimeoutOnlyWhenConfigured(t *testing.T) {
	for _, d := range []time.Duration{0, 45 * time.Second} {
		port := st.GetAvailablePort(t)
		server, _ := StartHTTPServer(port, http.NotFoundHandler(), false, "", "", 0, time.Second, d, ldlog.NewDisabledLoggers())
		if d == 0 {
			require.Nil(t, server.HTTP2)
		} else {
			require.NotNil(t, server.HTTP2)
			require.Equal(t, d, server.HTTP2.WriteByteTimeout)
		}
		_ = server.Close()
	}
}
