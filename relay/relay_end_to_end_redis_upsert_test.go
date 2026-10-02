//go:build redis_unit_tests
// +build redis_unit_tests

package relay

// These tests send flag updates through Relay into Redis. They cover both upsert modes of the Redis
// data store. A Redis server must be running on localhost for these tests.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	redigo "github.com/gomodule/redigo/redis"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-server-sdk-evaluation/v3/ldbuilders"
	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservices"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservicesv2"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"
	c "github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/logging/logtest"
	st "github.com/launchdarkly/ld-relay/v9/internal/sharedtest"
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

// runRedisUpsertTest starts several Relay instances that share one Redis prefix. Each instance
// receives the same change sets, so the instances write the same items at the same time. Every
// final version must reach Redis.
func runRedisUpsertTest(t *testing.T, atomicUpsert bool) {
	prefix := fmt.Sprintf("relay-upsert-test-%d", time.Now().UnixNano())
	conn := dialTestRedis(t)
	defer deleteTestRedisPrefix(t, conn, prefix)

	var flagKeys []string
	for i := 0; i < upsertTestFlagCount; i++ {
		flagKeys = append(flagKeys, upsertTestFlagKey(i))
	}

	testEnv := st.EnvWithAllCredentials
	testEnv.Config.Prefix = prefix
	redisConfig := c.RedisConfig{Host: "localhost", LocalTTL: configtypes.NewOptDuration(time.Minute),
		AtomicUpsert: atomicUpsert}

	eventsServer := httptest.NewServer(httphelpers.HandlerWithStatus(202))
	defer eventsServer.Close()

	// The mock stream gives the initial payload only to the first client, so each instance gets
	// its own stream. Every stream receives every change set.
	var streams []httphelpers.SSEStreamControl
	var logs []*logtest.MockHandler
	for i := 0; i < upsertTestRelayCount; i++ {
		streamHandler, stream := makeFlagStream(flagKeys...)
		defer stream.Close()
		streamServer := httptest.NewServer(streamHandler)
		defer streamServer.Close()
		streams = append(streams, stream)

		config := c.Config{Environment: st.MakeEnvConfigs(testEnv), Redis: redisConfig}
		config.Main.StreamURI, _ = configtypes.NewOptURLAbsoluteFromString(streamServer.URL)
		config.Main.BaseURI, _ = configtypes.NewOptURLAbsoluteFromString(streamServer.URL)
		config.Events.EventsURI, _ = configtypes.NewOptURLAbsoluteFromString(eventsServer.URL)

		logger, mockHandler := logtest.NewMockLogger()
		relay, err := newRelayInternal(config, relayInternalOptions{logger: logger})
		require.NoError(t, err)
		defer relay.Close()
		require.NoError(t, relay.waitForAllClients(5*time.Second))
		logs = append(logs, mockHandler)
	}

	// Send each round to every instance at once.
	for round := 2; round <= upsertTestRounds+1; round++ {
		var wg sync.WaitGroup
		for _, stream := range streams {
			wg.Add(1)
			go func(stream httphelpers.SSEStreamControl) {
				defer wg.Done()
				sendFlagChanges(stream, round, flagKeys...)
			}(stream)
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

	// The mock poll endpoint does not exist, so the SDK also logs a polling initializer failure.
	// Only data store messages are relevant here.
	var problems []string
	for _, mockHandler := range logs {
		for _, msg := range append(mockHandler.Messages(slog.LevelWarn), mockHandler.Messages(slog.LevelError)...) {
			if strings.Contains(msg, "store") {
				problems = append(problems, msg)
			}
		}
	}
	t.Logf("atomicUpsert=%t: %d data store warnings or errors across %d instances", atomicUpsert,
		len(problems), upsertTestRelayCount)
	for _, p := range problems {
		t.Log(p)
	}
	if atomicUpsert {
		assert.Empty(t, problems)
	}
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
			streamHandler, stream := makeFlagStream(flagKey)
			defer stream.Close()

			testEnv := st.EnvWithAllCredentials
			testEnv.Config.Prefix = prefix
			config := c.Config{Environment: st.MakeEnvConfigs(testEnv), Redis: c.RedisConfig{
				Host: "localhost", Username: user, Password: password,
				LocalTTL: configtypes.NewOptDuration(time.Minute), AtomicUpsert: atomicUpsert,
			}}
			relayEndToEndTest(t, config, relayTestBehavior{}, streamHandler, func(p relayEndToEndTestParams) {
				p.waitForSuccessfulInit()
				sendFlagChanges(stream, 2, flagKey)

				if !atomicUpsert {
					require.Eventually(t, func() bool {
						return getStoredFlagVersion(t, conn, prefix, flagKey) == 2
					}, 5*time.Second, 50*time.Millisecond)
					return
				}
				p.waitForLogMessage(slog.LevelWarn, "Detected persistent store unavailability", "store outage")
				assert.Equal(t, 1, getStoredFlagVersion(t, conn, prefix, flagKey))
			})
		})
	}
}

func upsertTestFlagKey(i int) string {
	return fmt.Sprintf("flag-%d", i)
}

// makeFlagStream returns a mock FDv2 stream whose initial payload has each flag at version 1.
func makeFlagStream(keys ...string) (http.Handler, httphelpers.SSEStreamControl) {
	data := ldservicesv2.NewServerSDKData()
	for _, key := range keys {
		data.Flags(ldbuilders.NewFlagBuilder(key).Version(1).Build())
	}
	protocol := ldservicesv2.NewStreamingProtocol().
		WithIntent(subsystems.ServerIntent{Payload: subsystems.Payload{
			ID: "fake-id", Code: subsystems.IntentTransferFull, Reason: "payload-missing",
		}}).
		WithPutObjects(data.ToPutObjects()).
		WithTransferred("state", 1)
	return ldservices.ServerSideStreamingV2ServiceProtocolHandler(protocol)
}

// sendFlagChanges sends one change set that puts each flag at the given version.
func sendFlagChanges(stream httphelpers.SSEStreamControl, version int, keys ...string) {
	protocol := ldservicesv2.NewStreamingProtocol()
	for _, key := range keys {
		flag, err := json.Marshal(ldbuilders.NewFlagBuilder(key).Version(version).Build())
		if err != nil {
			panic(err)
		}
		protocol.WithPutObject(subsystems.PutObject{
			Version: version, Kind: subsystems.FlagKind, Key: key, Object: flag,
		})
	}
	protocol.WithTransferred("state", version)
	protocol.Enqueue(stream)
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
