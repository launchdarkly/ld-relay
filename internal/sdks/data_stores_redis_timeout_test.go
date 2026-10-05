package sdks

import (
	"net"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-configtypes"

	redigo "github.com/gomodule/redigo/redis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startSilentServer accepts connections and never answers, like a Redis server that has stalled.
func startSilentServer(t *testing.T) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	return listener.Addr().String()
}

func TestRedisDialOptionsApplyReadTimeout(t *testing.T) {
	addr := startSilentServer(t)
	redisConfig := config.RedisConfig{ReadTimeout: configtypes.NewOptDuration(50 * time.Millisecond)}

	conn, err := redigo.DialURL("redis://"+addr, redisDialOptions(redisConfig)...)
	require.NoError(t, err)
	defer conn.Close()

	start := time.Now()
	_, err = conn.Do("PING")
	require.Error(t, err)
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout())
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestRedisDialOptionsOmitTimeoutsThatAreNotSet(t *testing.T) {
	assert.Len(t, redisDialOptions(config.RedisConfig{}), 0)

	redisConfig := config.RedisConfig{
		ConnectTimeout: configtypes.NewOptDuration(time.Second),
		ReadTimeout:    configtypes.NewOptDuration(time.Second),
	}
	assert.Len(t, redisDialOptions(redisConfig), 2)
}
