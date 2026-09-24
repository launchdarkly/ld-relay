package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/launchdarkly/ld-relay/v9/internal/megastream/wire"
)

func TestWebsocketURL(t *testing.T) {
	for name, tc := range map[string]struct {
		configured string
		want       string
	}{
		// Production configuration. The default streaming URI is https.
		"https becomes wss":                   {"https://stream.launchdarkly.com", "wss://stream.launchdarkly.com/relay/v1/mega-stream"},
		"http becomes ws":                     {"http://127.0.0.1:8123", "ws://127.0.0.1:8123/relay/v1/mega-stream"},
		"wss stays wss":                       {"wss://stream.launchdarkly.com", "wss://stream.launchdarkly.com/relay/v1/mega-stream"},
		"ws stays ws":                         {"ws://127.0.0.1:8123", "ws://127.0.0.1:8123/relay/v1/mega-stream"},
		"a base path is kept":                 {"https://example.com/ld", "wss://example.com/ld/relay/v1/mega-stream"},
		"a trailing slash does not double up": {"https://example.com/ld/", "wss://example.com/ld/relay/v1/mega-stream"},
	} {
		t.Run(name, func(t *testing.T) {
			configured, err := url.Parse(tc.configured)
			require.NoError(t, err)
			got, err := websocketURL(configured)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.String())
		})
	}
}

func TestWebsocketURLRejectsUnusableSchemes(t *testing.T) {
	configured, err := url.Parse("ftp://example.com")
	require.NoError(t, err)
	_, err = websocketURL(configured)
	assert.Error(t, err)
}

func TestNewValidatesItsParameters(t *testing.T) {
	provider := func() (string, []wire.HelloScope) { return "sdk-env", nil }
	streamURI, err := url.Parse("http://127.0.0.1:8123")
	require.NoError(t, err)

	_, err = New(Params{Hello: provider})
	assert.Error(t, err, "a session needs somewhere to connect")

	_, err = New(Params{StreamURI: streamURI})
	assert.Error(t, err, "a session needs to know what to put in its hello")

	_, err = New(Params{StreamURI: streamURI, Hello: provider})
	assert.NoError(t, err)
}

// serverConn is one accepted connection, handed to a test's script.
//
// Nothing here fails the test. A script runs on the server's goroutine, which outlives the
// test body, so a failed assertion there would panic instead of reporting. Scripts move
// results onto channels and return early on error; the test body does the asserting.
type serverConn struct {
	socket *websocket.Conn
	hold   <-chan struct{}
}

// readHello reads the handshake the relay sent. It fails if the frame is not text, because
// the handshake travels as text.
func (c *serverConn) readHello() (wire.Hello, error) {
	kind, frame, err := c.socket.Read(context.Background())
	if err != nil {
		return wire.Hello{}, err
	}
	if kind != websocket.MessageText {
		return wire.Hello{}, fmt.Errorf("hello arrived as %v, not text", kind)
	}
	var hello wire.Hello
	if err := json.Unmarshal(frame, &hello); err != nil {
		return wire.Hello{}, err
	}
	return hello, nil
}

// readMessage reads one message the relay sent after the handshake.
func (c *serverConn) readMessage() (map[string]any, error) {
	kind, frame, err := c.socket.Read(context.Background())
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageBinary {
		return nil, fmt.Errorf("message arrived as %v, not binary", kind)
	}
	var decoded map[string]any
	if err := msgpack.Unmarshal(frame, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *serverConn) writeJSON(msg any) error {
	frame, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return c.socket.Write(context.Background(), websocket.MessageText, frame)
}

func (c *serverConn) writeMsgpack(msg any) error {
	frame, err := msgpack.Marshal(msg)
	if err != nil {
		return err
	}
	return c.socket.Write(context.Background(), websocket.MessageBinary, frame)
}

func (c *serverConn) writeRaw(kind websocket.MessageType, frame []byte) error {
	return c.socket.Write(context.Background(), kind, frame)
}

// welcome accepts the handshake. The heartbeat interval is zero, which disables the probes
// and with them the session's silence timeout, keeping these tests off the clock.
func (c *serverConn) welcome() error {
	return c.writeJSON(wire.Welcome{
		Type: wire.TypeWelcome, Version: 1,
		Capabilities:        []string{wire.CapabilityMsgpack, wire.CapabilityBatchRefs},
		HeartbeatIntervalMs: 0,
		Base:                "payload-1",
	})
}

// hold keeps the connection open until the test tears down.
func (c *serverConn) wait() {
	<-c.hold
}

// startServer serves one WebSocket endpoint. script runs per accepted connection on the
// server's goroutine, and the returned channel counts connections so a test can wait for a
// reconnect.
func startServer(t *testing.T, script func(*serverConn)) (*url.URL, <-chan int) {
	t.Helper()
	accepted := make(chan int, 16)
	paths := make(chan string, 16)
	hold := make(chan struct{})

	// Connections can overlap, so the counter is atomic.
	var count atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		accepted <- int(count.Add(1))
		script(&serverConn{socket: socket, hold: hold})
	}))

	// Cleanups run last registered first. Releasing the scripts before closing the server
	// lets its handlers finish, so no script is still running when the test completes.
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(hold) })
	t.Cleanup(func() {
		for {
			select {
			case path := <-paths:
				assert.Equal(t, StreamPath, path, "the relay dialed an unexpected path")
			default:
				return
			}
		}
	})

	streamURI, err := url.Parse(server.URL)
	require.NoError(t, err)
	return streamURI, accepted
}

func startSession(t *testing.T, streamURI *url.URL, params Params) (*Session, context.Context) {
	t.Helper()
	params.StreamURI = streamURI
	if params.Hello == nil {
		params.Hello = func() (string, []wire.HelloScope) {
			return "sdk-env", []wire.HelloScope{{Key: "sdk-env"}}
		}
	}
	if params.Logger == nil {
		params.Logger = slog.New(slog.DiscardHandler)
	}
	session, err := New(params)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go session.Run(ctx)
	return session, ctx
}

// expect reads the next value from the session, failing the test if none arrives.
func expect[T any](t *testing.T, session *Session) T {
	t.Helper()
	select {
	case msg, ok := <-session.Messages():
		require.True(t, ok, "the session closed its channel while a %T was expected", *new(T))
		require.IsType(t, *new(T), msg, "unexpected message %#v", msg)
		return msg.(T)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for a %T", *new(T))
		panic("unreachable")
	}
}

func TestHelloCarriesTheRootTheScopesAndTheirStates(t *testing.T) {
	hellos := make(chan wire.Hello, 1)
	streamURI, _ := startServer(t, func(c *serverConn) {
		hello, err := c.readHello()
		if err != nil {
			return
		}
		hellos <- hello
		if c.welcome() != nil {
			return
		}
		c.wait()
	})

	session, _ := startSession(t, streamURI, Params{
		Capabilities: []string{wire.CapabilityBatchRefs, wire.CapabilityZstd},
		RelayVersion: "9.0.0-rc.7",
		InstanceID:   "instance-abc",
		Hello: func() (string, []wire.HelloScope) {
			return "sdk-env", []wire.HelloScope{
				{Key: "sdk-env"},
				{Key: "sdk-viewA", State: "sel-1"},
				{Key: "mob-viewA"},
			}
		},
	})

	hello := <-hellos
	assert.Equal(t, wire.TypeHello, hello.Type)
	assert.Equal(t, wire.ProtocolVersion, hello.Version)
	assert.Equal(t, "sdk-env", hello.Root)
	assert.Equal(t, "9.0.0-rc.7", hello.RelayVersion)
	assert.Equal(t, "instance-abc", hello.InstanceID)
	assert.Equal(t, []string{wire.CapabilityBatchRefs, wire.CapabilityZstd}, hello.Capabilities)
	assert.Equal(t, []wire.HelloScope{
		{Key: "sdk-env"},
		{Key: "sdk-viewA", State: "sel-1"},
		{Key: "mob-viewA"},
	}, hello.Scopes)

	connected := expect[Connected](t, session)
	assert.Equal(t, "payload-1", connected.Welcome.Base)
}

// The provider is consulted per attempt, so a reconnect presents whatever the caller wants
// served now, with the selectors recorded since the last handshake.
func TestReconnectPresentsTheCurrentDesiredSet(t *testing.T) {
	hellos := make(chan wire.Hello, 4)
	streamURI, accepted := startServer(t, func(c *serverConn) {
		hello, err := c.readHello()
		if err != nil {
			return
		}
		hellos <- hello
		_ = c.welcome()
		// Return at once, closing the connection, so the session reconnects.
	})

	attempt := 0
	_, _ = startSession(t, streamURI, Params{
		Hello: func() (string, []wire.HelloScope) {
			attempt++
			if attempt == 1 {
				return "sdk-env", []wire.HelloScope{{Key: "sdk-env"}}
			}
			return "sdk-env", []wire.HelloScope{
				{Key: "sdk-env", State: "sel-9"},
				{Key: "sdk-viewB"},
			}
		},
	})

	require.Equal(t, 1, <-accepted)
	first := <-hellos
	assert.Equal(t, []wire.HelloScope{{Key: "sdk-env"}}, first.Scopes)

	require.Equal(t, 2, <-accepted)
	second := <-hellos
	assert.Equal(t, []wire.HelloScope{
		{Key: "sdk-env", State: "sel-9"},
		{Key: "sdk-viewB"},
	}, second.Scopes)
}

func TestHandshakeRefusesToSendAnUnusableHello(t *testing.T) {
	streamURI, _ := startServer(t, func(c *serverConn) { c.wait() })

	for name, provider := range map[string]HelloProvider{
		"no root": func() (string, []wire.HelloScope) {
			return "", []wire.HelloScope{{Key: "sdk-env"}}
		},
		// The root alone serves nothing, so a handshake with no scopes has no purpose.
		"no scopes": func() (string, []wire.HelloScope) { return "sdk-env", nil },
	} {
		t.Run(name, func(t *testing.T) {
			session, err := New(Params{
				StreamURI: streamURI, Hello: provider,
				Logger: slog.New(slog.DiscardHandler),
			})
			require.NoError(t, err)

			socket, err := session.dial(context.Background())
			require.NoError(t, err)
			defer socket.CloseNow()

			_, err = session.handshake(context.Background(), socket, &reconnectPolicy{})
			assert.Error(t, err)
		})
	}
}

func TestDataMessagesReachTheConsumerInOrder(t *testing.T) {
	streamURI, _ := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		if c.welcome() != nil {
			return
		}
		_ = c.writeMsgpack(map[string]any{
			"t": "server-intent", "scope": "sdk-env",
			"intentCode": "xfer-full", "unfiltered": true,
		})
		_ = c.writeMsgpack(map[string]any{
			"t": "put-object", "base": "payload-1", "kind": "flag",
			"key": "f1", "version": 13, "object": []byte(`{"key":"f1"}`),
		})
		_ = c.writeMsgpack(map[string]any{
			"t": "payload-transferred", "scope": "sdk-env",
			"state": "sel-1", "version": 13,
		})
		c.wait()
	})

	session, _ := startSession(t, streamURI, Params{})

	expect[Connected](t, session)

	intent := expect[wire.ServerIntent](t, session)
	assert.Equal(t, "sdk-env", intent.Scope)
	unfiltered, declared := intent.IsUnfiltered()
	assert.True(t, declared)
	assert.True(t, unfiltered)

	put := expect[wire.PutObject](t, session)
	assert.Equal(t, []byte(`{"key":"f1"}`), put.Object)

	cursor := expect[wire.PayloadTransferred](t, session)
	assert.Equal(t, "sel-1", cursor.State)
}

// The session answers heartbeats itself and does not pass them on: liveness is its business,
// not the caller's.
func TestHeartbeatsAreAcknowledgedAndNotForwarded(t *testing.T) {
	acks := make(chan map[string]any, 2)
	streamURI, _ := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		if c.welcome() != nil {
			return
		}
		for range 2 {
			if c.writeMsgpack(map[string]any{"t": "heartbeat"}) != nil {
				return
			}
			ack, err := c.readMessage()
			if err != nil {
				return
			}
			acks <- ack
		}
		_ = c.writeMsgpack(map[string]any{"t": "ref", "scope": "sdk-env", "kind": "flag", "key": "f1"})
		c.wait()
	})

	session, _ := startSession(t, streamURI, Params{})
	expect[Connected](t, session)

	assert.Equal(t, "heartbeat-ack", (<-acks)["t"])
	assert.Equal(t, "heartbeat-ack", (<-acks)["t"])

	// The ref arrives next, which shows nothing was emitted for either heartbeat.
	assert.Equal(t, "f1", expect[wire.Ref](t, session).Key)
}

// Two heartbeat intervals with no inbound traffic means the connection is gone, even though
// the socket still looks open.
func TestSilenceIsTreatedAsALostConnection(t *testing.T) {
	streamURI, accepted := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		_ = c.writeJSON(wire.Welcome{
			Type: wire.TypeWelcome, Version: 1,
			Capabilities:        []string{wire.CapabilityMsgpack},
			HeartbeatIntervalMs: 50,
			Base:                "payload-1",
		})
		// Then say nothing at all, holding the socket open.
		c.wait()
	})

	session, _ := startSession(t, streamURI, Params{})

	require.Equal(t, 1, <-accepted)
	expect[Connected](t, session)
	assert.Error(t, expect[Disconnected](t, session).Err)

	// The session reconnects rather than sitting on a dead socket.
	require.Equal(t, 2, drainTo(t, accepted, 2))
}

func TestUnrecognizedMessagesAreIgnoredWithoutDroppingTheConnection(t *testing.T) {
	streamURI, _ := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		if c.welcome() != nil {
			return
		}
		_ = c.writeMsgpack(map[string]any{"t": "invented-later", "payload": "whatever"})
		_ = c.writeMsgpack(map[string]any{"t": "ref", "scope": "sdk-env", "kind": "flag", "key": "f1"})
		c.wait()
	})

	session, _ := startSession(t, streamURI, Params{})
	expect[Connected](t, session)
	assert.Equal(t, "f1", expect[wire.Ref](t, session).Key)
}

// An undecodable frame in a single ordered stream is an unidentifiable message lost, so the
// session reports it, stops applying, and reconnects.
func TestAnUndecodableFrameIsReportedAndEndsTheConnection(t *testing.T) {
	reports := make(chan map[string]any, 2)
	streamURI, accepted := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		if c.welcome() != nil {
			return
		}
		if c.writeRaw(websocket.MessageBinary, []byte{0xc1, 0x00, 0x42}) != nil {
			return
		}
		if report, err := c.readMessage(); err == nil {
			reports <- report
		}
		c.wait()
	})

	session, _ := startSession(t, streamURI, Params{})
	expect[Connected](t, session)

	report := <-reports
	assert.Equal(t, "error", report["t"])
	assert.Equal(t, wire.ErrorBadMessage, report["code"])

	assert.Error(t, expect[Disconnected](t, session).Err)
	// The first connection was already counted; the second is the reconnect.
	require.Equal(t, 2, drainTo(t, accepted, 2))
}

// The two framings are part of the contract. A text frame where an envelope belongs means the
// peer is not speaking this protocol.
func TestATextFrameAfterTheHandshakeEndsTheConnection(t *testing.T) {
	streamURI, _ := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		if c.welcome() != nil {
			return
		}
		_ = c.writeRaw(websocket.MessageText, []byte(`{"t":"heartbeat"}`))
		c.wait()
	})

	session, _ := startSession(t, streamURI, Params{})
	expect[Connected](t, session)
	assert.Error(t, expect[Disconnected](t, session).Err)
}

// A rejected handshake produces no Connected, but it does report why. That is what lets a
// configuration error reach an operator instead of only a log file.
func TestARefusedHandshakeReportsWhyWithoutConnecting(t *testing.T) {
	streamURI, accepted := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		_ = c.writeJSON(wire.Goodbye{
			T:      wire.TypeGoodbye,
			Reason: "the root credential is view-scoped",
			Code:   wire.GoodbyeRootNotEnvironmentWide,
		})
		c.wait()
	})

	session, _ := startSession(t, streamURI, Params{})
	require.Equal(t, 1, <-accepted)

	failure := expect[Disconnected](t, session)
	require.Error(t, failure.Err)
	assert.Contains(t, failure.Err.Error(), wire.GoodbyeRootNotEnvironmentWide)
}

// A goodbye is forwarded, because a close can carry news the caller needs to act on, such as
// the root credential having been revoked.
func TestAGoodbyeReachesTheConsumer(t *testing.T) {
	streamURI, _ := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		if c.welcome() != nil {
			return
		}
		_ = c.writeMsgpack(map[string]any{
			"t": "goodbye", "reason": "the root credential was revoked",
			"code": "root-revoked",
		})
		c.wait()
	})

	session, _ := startSession(t, streamURI, Params{})
	expect[Connected](t, session)
	assert.Equal(t, wire.GoodbyeRootRevoked, expect[wire.Goodbye](t, session).Code)
	assert.Error(t, expect[Disconnected](t, session).Err)
}

func TestApplyGoodbyeRecordsAdviceAndParks(t *testing.T) {
	session, err := New(Params{
		StreamURI: mustParse(t, "http://127.0.0.1:1"),
		Hello:     func() (string, []wire.HelloScope) { return "sdk-env", nil },
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	advised := &reconnectPolicy{}
	session.applyGoodbye(wire.Goodbye{Code: wire.GoodbyeDraining, BackoffMs: 5000}, advised)
	withinJitter(t, 5*time.Second, advised.next())

	parked := &reconnectPolicy{}
	session.applyGoodbye(wire.Goodbye{Code: wire.GoodbyeMixedEnvironment}, parked)
	withinJitter(t, configErrorDelay, parked.next())
}

func TestRegisterAndDeregisterSendTheirFrames(t *testing.T) {
	sent := make(chan map[string]any, 2)
	streamURI, _ := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		if c.welcome() != nil {
			return
		}
		for range 2 {
			msg, err := c.readMessage()
			if err != nil {
				return
			}
			sent <- msg
		}
		c.wait()
	})

	session, ctx := startSession(t, streamURI, Params{})
	expect[Connected](t, session)

	require.NoError(t, session.Register(ctx, "sdk-viewB"))
	require.NoError(t, session.Deregister(ctx, "sdk-viewA"))

	add := <-sent
	assert.Equal(t, "scope-add", add["t"])
	assert.Equal(t, "sdk-viewB", add["key"])

	remove := <-sent
	assert.Equal(t, "scope-remove", remove["t"])
	assert.Equal(t, "sdk-viewA", remove["scope"])
}

// Registrations are level-triggered: the next handshake presents the whole desired set, so a
// caller may ignore this error.
func TestSendingWhileDisconnectedReportsItPlainly(t *testing.T) {
	session, err := New(Params{
		StreamURI: mustParse(t, "http://127.0.0.1:1"),
		Hello:     func() (string, []wire.HelloScope) { return "sdk-env", nil },
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	ctx := context.Background()
	assert.ErrorIs(t, session.Register(ctx, "sdk-viewB"), ErrNotConnected)
	assert.ErrorIs(t, session.Deregister(ctx, "sdk-viewA"), ErrNotConnected)
	assert.ErrorIs(t, session.ReportError(ctx, wire.ErrorMessage{}), ErrNotConnected)
}

func TestAMismatchedProtocolVersionIsNotAnEstablishedSession(t *testing.T) {
	streamURI, _ := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		_ = c.writeJSON(map[string]any{
			"type": "welcome", "version": 2,
			"capabilities": []string{"msgpack"}, "heartbeatIntervalMs": 0, "base": "payload-1",
		})
		c.wait()
	})

	session, _ := startSession(t, streamURI, Params{})
	failure := expect[Disconnected](t, session)
	assert.ErrorContains(t, failure.Err, "protocol version 2")
}

func TestRunClosesTheChannelWhenTheContextEnds(t *testing.T) {
	streamURI, _ := startServer(t, func(c *serverConn) {
		if _, err := c.readHello(); err != nil {
			return
		}
		if c.welcome() != nil {
			return
		}
		c.wait()
	})

	session, err := New(Params{
		StreamURI: streamURI,
		Hello: func() (string, []wire.HelloScope) {
			return "sdk-env", []wire.HelloScope{{Key: "sdk-env"}}
		},
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		session.Run(ctx)
		close(stopped)
	}()

	expect[Connected](t, session)
	cancel()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	// Drain whatever the shutdown buffered; the channel closing is the assertion.
	for range session.Messages() { //nolint:revive // draining to the close
	}
}

// drainTo waits for a connection count to reach want, ignoring the ones before it.
func drainTo(t *testing.T, accepted <-chan int, want int) int {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-accepted:
			if got >= want {
				return got
			}
		case <-deadline:
			t.Fatalf("timed out waiting for connection %d", want)
		}
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed
}
