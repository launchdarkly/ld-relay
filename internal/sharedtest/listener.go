package sharedtest

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// WithListenerForAnyPort creates a listener for an available port, calls the function with the listener
// and the port number, and then closes the listener.
func WithListenerForAnyPort(t *testing.T, fn func(net.Listener, int)) {
	l, port := startListenerForAnyAvailablePort(t)
	defer l.Close() //nolint:errcheck
	fn(l, port)
}

// GetAvailablePort finds an available port (by creating and then immediately closing a listener) and
// returns the port number.
func GetAvailablePort(t *testing.T) int {
	l, port := startListenerForAnyAvailablePort(t)
	l.Close() //nolint:errcheck,gosec
	return port
}

func startListenerForAnyAvailablePort(t *testing.T) (net.Listener, int) {
	l, err := net.Listen("tcp", ":0") //nolint:gosec
	require.NoError(t, err)
	addr := l.Addr().String()
	port, err := strconv.Atoi(addr[strings.LastIndex(addr, ":")+1:])
	require.NoError(t, err)
	return l, port
}
