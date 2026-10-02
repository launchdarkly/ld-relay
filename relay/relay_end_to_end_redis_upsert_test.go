//go:build redis_unit_tests
// +build redis_unit_tests

package relay

// These tests send flag updates through Relay into Redis. They cover both upsert modes of the Redis
// data store. A Redis server must be running on localhost for these tests.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	redigo "github.com/gomodule/redigo/redis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservices"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	c "github.com/launchdarkly/ld-relay/v8/config"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"
)

const (
	upsertTestRelayCount = 5
	upsertTestFlagCount  = 50
	upsertTestRounds     = 10
)

func TestRelayEndToEndRedisUpsertWatchMode(t *testing.T) {
	runRedisUpsertTest(t, false)
}

func TestRelayEndToEndRedisUpsertAtomicMode(t *testing.T) {
	runRedisUpsertTest(t, true)
}

// runRedisUpsertTest starts several Relay instances that share one Redis prefix and one stream.
// The stream sends many updates of different flags, so the instances write the same items at the
// same time. Every final version must reach Redis.
func runRedisUpsertTest(t *testing.T, atomicUpsert bool) {
	prefix := fmt.Sprintf("relay-upsert-test-%d", time.Now().UnixNano())
	conn := dialTestRedis(t)
	defer deleteTestRedisPrefix(t, conn, prefix)

	var flags []interface{}
	for i := 0; i < upsertTestFlagCount; i++ {
		flags = append(flags, ldbuilders.NewFlagBuilder(upsertTestFlagKey(i)).Version(1).Build())
	}
	putEvent := ldservices.NewServerSDKData().Flags(flags...).ToPutEvent()
	streamHandler, stream := ldservices.ServerSideStreamingServiceHandler(putEvent)
	defer stream.Close()

	testEnv := st.EnvWithAllCredentials
	testEnv.Config.Prefix = prefix
	redisConfig := c.RedisConfig{Host: "localhost", LocalTTL: configtypes.NewOptDuration(time.Minute),
		AtomicUpsert: atomicUpsert}

	eventsServer := httptest.NewServer(httphelpers.HandlerWithStatus(202))
	defer eventsServer.Close()

	httphelpers.WithServer(streamHandler, func(streamServer *httptest.Server) {
		config := c.Config{Environment: st.MakeEnvConfigs(testEnv), Redis: redisConfig}
		config.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(streamServer.URL)
		config.Main.BaseURI, _ = configtypes.NewOptURLAbsoluteFromString(streamServer.URL)
		config.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(eventsServer.URL)

		var logs []*ldlogtest.MockLog
		for i := 0; i < upsertTestRelayCount; i++ {
			mockLog := ldlogtest.NewMockLog()
			defer mockLog.DumpIfTestFailed(t)
			relay, err := newRelayInternal(config, relayInternalOptions{loggers: mockLog.Loggers})
			require.NoError(t, err)
			defer relay.Close()
			require.NoError(t, relay.waitForAllClients(5*time.Second))
			logs = append(logs, mockLog)
		}

		// Send every round of updates at once. Each instance receives each update.
		for round := 2; round <= upsertTestRounds+1; round++ {
			var wg sync.WaitGroup
			for i := 0; i < upsertTestFlagCount; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					stream.Send(makeFlagPatchEvent(t, upsertTestFlagKey(i), round))
				}(i)
			}
			wg.Wait()
		}

		finalVersion := upsertTestRounds + 1
		require.Eventually(t, func() bool {
			for i := 0; i < upsertTestFlagCount; i++ {
				if getStoredFlagVersion(t, conn, prefix, upsertTestFlagKey(i)) != finalVersion {
					return false
				}
			}
			return true
		}, 10*time.Second, 50*time.Millisecond, "not every flag reached its final version in Redis")

		var problems []string
		for _, mockLog := range logs {
			problems = append(problems, mockLog.GetOutput(ldlog.Warn)...)
			problems = append(problems, mockLog.GetOutput(ldlog.Error)...)
		}
		t.Logf("atomicUpsert=%t: %d warnings or errors across %d instances", atomicUpsert, len(problems),
			upsertTestRelayCount)
		for _, p := range problems {
			t.Log(p)
		}
		if atomicUpsert {
			assert.Empty(t, problems)
		}
	})
}

// TestRelayEndToEndRedisUpsertWithoutScriptPermission uses a Redis user that cannot run Lua scripts.
// The watch mode does not need scripts, so it must work. The atomic mode must report the store as
// unavailable.
func TestRelayEndToEndRedisUpsertWithoutScriptPermission(t *testing.T) {
	conn := dialTestRedis(t)
	const user, password = "relay-no-scripting", "test-password"
	_, err := conn.Do("ACL", "SETUSER", user, "on", ">"+password, "~*", "&*", "+@all", "-@scripting")
	require.NoError(t, err)
	defer conn.Do("ACL", "DELUSER", user)

	for _, atomicUpsert := range []bool{false, true} {
		t.Run(fmt.Sprintf("atomicUpsert=%t", atomicUpsert), func(t *testing.T) {
			prefix := fmt.Sprintf("relay-acl-test-%d", time.Now().UnixNano())
			defer deleteTestRedisPrefix(t, conn, prefix)

			flagKey := "acl-flag"
			putEvent := ldservices.NewServerSDKData().
				Flags(ldbuilders.NewFlagBuilder(flagKey).Version(1).Build()).ToPutEvent()
			streamHandler, stream := ldservices.ServerSideStreamingServiceHandler(putEvent)
			defer stream.Close()

			testEnv := st.EnvWithAllCredentials
			testEnv.Config.Prefix = prefix
			config := c.Config{Environment: st.MakeEnvConfigs(testEnv), Redis: c.RedisConfig{
				Host: "localhost", Username: user, Password: password,
				LocalTTL: configtypes.NewOptDuration(time.Minute), AtomicUpsert: atomicUpsert,
			}}
			relayEndToEndTest(t, config, relayTestBehavior{}, streamHandler, func(p relayEndToEndTestParams) {
				p.waitForSuccessfulInit()
				stream.Send(makeFlagPatchEvent(t, flagKey, 2))

				if !atomicUpsert {
					require.Eventually(t, func() bool {
						return getStoredFlagVersion(t, conn, prefix, flagKey) == 2
					}, 5*time.Second, 50*time.Millisecond)
					return
				}
				p.waitForLogMessage(ldlog.Warn, "Detected persistent store unavailability", "store outage")
				assert.Equal(t, 1, getStoredFlagVersion(t, conn, prefix, flagKey))
			})
		})
	}
}

func upsertTestFlagKey(i int) string {
	return fmt.Sprintf("flag-%d", i)
}

func makeFlagPatchEvent(t *testing.T, key string, version int) httphelpers.SSEEvent {
	flag := ldbuilders.NewFlagBuilder(key).Version(version).Build()
	data, err := json.Marshal(map[string]interface{}{"path": "/flags/" + key, "data": flag})
	require.NoError(t, err)
	return httphelpers.SSEEvent{Event: "patch", Data: string(data)}
}

func dialTestRedis(t *testing.T) redigo.Conn {
	conn, err := redigo.Dial("tcp", "localhost:6379")
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}

func getStoredFlagVersion(t *testing.T, conn redigo.Conn, prefix, key string) int {
	data, err := redigo.Bytes(conn.Do("HGET", prefix+":features", key))
	if err == redigo.ErrNil {
		return 0
	}
	require.NoError(t, err)
	var item struct {
		Version int `json:"version"`
	}
	require.NoError(t, json.Unmarshal(data, &item))
	return item.Version
}

func deleteTestRedisPrefix(t *testing.T, conn redigo.Conn, prefix string) {
	keys, err := redigo.Strings(conn.Do("KEYS", prefix+":*"))
	require.NoError(t, err)
	for _, k := range keys {
		_, err := conn.Do("DEL", k)
		require.NoError(t, err)
	}
}
