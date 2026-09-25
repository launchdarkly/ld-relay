package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/launchdarkly/ld-relay/v9/internal/megastream/wire"
)

// StreamPath is appended to the configured streaming URI to form the MegaStream URL.
const StreamPath = "/relay/v1/mega-stream"

const (
	// dialTimeout bounds one connection attempt, handshake excluded.
	dialTimeout = 10 * time.Second

	// handshakeTimeout bounds how long the relay waits for a welcome, a goodbye or an error
	// after sending its hello.
	handshakeTimeout = 30 * time.Second

	// readLimit bounds one inbound frame. The library defaults to 32 KiB, which a large
	// flag would exceed, so the session sets its own.
	readLimit = 16 << 20

	// messageBuffer smooths delivery bursts. The consumer must keep up: falling behind is
	// backpressure on the socket, never dropped messages, because the protocol's ordering
	// and completeness guarantees are what make a cursor trustworthy.
	messageBuffer = 256
)

// ErrNotConnected reports that the session had no established connection to send on.
//
// A caller may ignore it. Registrations are level-triggered: the next handshake presents the
// whole desired set, so a registration that could not be sent now is made at reconnect.
var ErrNotConnected = errors.New("megastream: not connected")

// HelloProvider supplies the contents of a handshake. The session calls it immediately
// before every connection attempt, so a reconnect presents the caller's current desired set
// with the most recently recorded selectors.
//
// Root is the environment-wide server-side credential that authorizes the session. It
// contributes no content unless the same value also appears among the scopes.
type HelloProvider func() (root string, scopes []wire.HelloScope)

// Params configures a Session.
type Params struct {
	// StreamURI is the configured LaunchDarkly streaming base URI.
	StreamURI *url.URL

	// Hello supplies the root credential and the scopes to register.
	Hello HelloProvider

	// Capabilities the relay offers. MessagePack is implicitly required and may be omitted.
	Capabilities []string

	// RelayVersion is reported for diagnostics.
	RelayVersion string

	// InstanceID identifies this relay instance and must be stable across reconnects. It
	// belongs to the process rather than to one session, so the caller supplies it. Left
	// empty, the field is omitted and the server can never refuse this relay for
	// reconnecting before its advised delay.
	InstanceID string

	// HTTPClient carries the relay's proxy and TLS configuration into the dial.
	HTTPClient *http.Client

	Logger *slog.Logger
}

// Connected reports a completed handshake and carries the negotiated session parameters.
type Connected struct {
	Welcome wire.Welcome
}

// Disconnected reports that a connection attempt ended, and Err says why.
//
// It arrives for every attempt, including one that never reached a handshake. A consumer
// therefore always learns why a session is not up, which is what lets a configuration error
// reach an operator rather than only a log file. It is not paired with Connected: an attempt
// that failed to dial or was refused produces a Disconnected and no Connected.
type Disconnected struct {
	Err error
}

// Session owns one MegaStream connection and reconnects it as needed.
type Session struct {
	params  Params
	logger  *slog.Logger
	url     *url.URL
	out     chan any
	current atomic.Pointer[connection]
}

// connection is one established connection: its socket and the framing negotiated for it.
type connection struct {
	socket *websocket.Conn
	codec  *wire.Codec
	// write serializes sends, because the socket permits one writer at a time.
	write sync.Mutex
}

func (c *connection) send(ctx context.Context, frame []byte) error {
	c.write.Lock()
	defer c.write.Unlock()
	return c.socket.Write(ctx, websocket.MessageBinary, frame)
}

// New builds a Session. It does not connect; call Run.
func New(params Params) (*Session, error) {
	if params.StreamURI == nil {
		return nil, errors.New("megastream: no streaming URI configured")
	}
	if params.Hello == nil {
		return nil, errors.New("megastream: no hello provider configured")
	}
	streamURL, err := websocketURL(params.StreamURI)
	if err != nil {
		return nil, err
	}
	logger := params.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Session{
		params: params,
		logger: logger.With("component", "MegaStream"),
		url:    streamURL,
		out:    make(chan any, messageBuffer),
	}, nil
}

// Messages is the session's ordered output. Each value is a Connected, a Disconnected, or
// one of the message types in the wire package. Every connection attempt ends with a
// Disconnected, whether or not it got as far as Connected.
//
// Heartbeats do not appear: the session answers them itself. Everything else the server
// sends does, including a goodbye, because a close can carry news the caller needs, such as
// the root credential having been revoked.
//
// The channel closes when Run returns.
func (s *Session) Messages() <-chan any {
	return s.out
}

// URL is the address this session dials.
func (s *Session) URL() string {
	return s.url.String()
}

// Run connects and keeps the session connected until ctx is cancelled. It closes the message
// channel before returning.
func (s *Session) Run(ctx context.Context) {
	defer close(s.out)

	policy := &reconnectPolicy{}
	for ctx.Err() == nil {
		err := s.connectAndServe(ctx, policy)
		if ctx.Err() != nil {
			return
		}
		delay := policy.next()
		s.logger.Warn("MegaStream connection ended; will reconnect",
			"error", err, "delay", delay, "url", s.url.String())
		if !sleep(ctx, delay) {
			return
		}
	}
}

// connectAndServe runs one connection from dial to close and returns the reason it ended.
func (s *Session) connectAndServe(ctx context.Context, policy *reconnectPolicy) (err error) {
	// Every attempt reports how it ended, so the layer above always knows why a session is
	// not up. A refused handshake is the case that matters: without this, a configuration
	// error would reach the log and nothing else.
	defer func() {
		if err != nil {
			s.emit(ctx, Disconnected{Err: err})
		}
	}()

	socket, err := s.dial(ctx)
	if err != nil {
		return err
	}
	socket.SetReadLimit(readLimit)
	defer func() {
		// CloseNow rather than a closing handshake: the connection is already finished, and
		// a peer that has stopped reading must not hold the reconnect up.
		_ = socket.CloseNow()
	}()

	welcome, err := s.handshake(ctx, socket, policy)
	if err != nil {
		return err
	}
	codec, err := wire.NewCodec(welcome.Capabilities)
	if err != nil {
		return err
	}
	defer codec.Close()

	conn := &connection{socket: socket, codec: codec}
	s.current.Store(conn)
	defer s.current.Store(nil)

	policy.reset()
	s.logger.Info("MegaStream established",
		"capabilities", welcome.Capabilities,
		"base", welcome.Base,
		"heartbeatIntervalMs", welcome.HeartbeatIntervalMs,
		"maxScopes", welcome.MaxScopes)

	if !s.emit(ctx, Connected{Welcome: welcome}) {
		return ctx.Err()
	}
	return s.serve(ctx, conn, welcome, policy)
}

// serve reads and dispatches messages until the connection ends.
func (s *Session) serve(
	ctx context.Context,
	conn *connection,
	welcome wire.Welcome,
	policy *reconnectPolicy,
) error {
	// A dead TCP connection can look healthy, so silence bounds how long the relay serves
	// without knowing. Two heartbeat intervals with no inbound traffic means the connection
	// is gone. An interval of zero disables the probes, and with them this timeout.
	silence := 2 * time.Duration(welcome.HeartbeatIntervalMs) * time.Millisecond

	for {
		frame, err := readFrame(ctx, conn.socket, websocket.MessageBinary, silence)
		if err != nil {
			return err
		}
		msg, err := conn.codec.Decode(frame)
		if err != nil {
			// An undecodable frame in a single ordered stream is an unidentifiable message
			// lost, so nothing after it can be trusted to apply. Report it, stop applying,
			// and reconnect presenting the states recorded so far.
			s.reportBadMessage(ctx, err)
			return fmt.Errorf("megastream: undecodable frame: %w", err)
		}
		switch typed := msg.(type) {
		case wire.Heartbeat:
			if err := s.acknowledge(ctx, conn); err != nil {
				return err
			}
		case wire.Goodbye:
			s.applyGoodbye(typed, policy)
			if !s.emit(ctx, typed) {
				return ctx.Err()
			}
			return fmt.Errorf("megastream: server closed the session: %s (%s)",
				typed.Reason, typed.Code)
		case wire.Unknown:
			// Logging an unrecognized type is useful; failing the connection is not. This
			// is the rule that makes capability-gated additions safe to deploy.
			s.logger.Debug("ignoring unrecognized MegaStream message", "type", typed.T)
		default:
			if !s.emit(ctx, msg) {
				return ctx.Err()
			}
		}
	}
}

// handshake sends the hello and waits for the server's answer.
func (s *Session) handshake(
	ctx context.Context,
	socket *websocket.Conn,
	policy *reconnectPolicy,
) (wire.Welcome, error) {
	root, scopes := s.params.Hello()
	if root == "" {
		return wire.Welcome{}, errors.New(
			"megastream: no root credential configured for this environment")
	}
	if len(scopes) == 0 {
		return wire.Welcome{}, errors.New(
			"megastream: no scopes to register; the root credential alone serves nothing")
	}
	frame, err := wire.EncodeHello(wire.Hello{
		RelayVersion: s.params.RelayVersion,
		Capabilities: s.params.Capabilities,
		Root:         root,
		Scopes:       scopes,
		InstanceID:   s.params.InstanceID,
	})
	if err != nil {
		return wire.Welcome{}, err
	}

	writeCtx, cancelWrite := context.WithTimeout(ctx, handshakeTimeout)
	defer cancelWrite()
	// The handshake is JSON in a text frame so that either side can read the other's
	// rejection whatever binary format the session goes on to negotiate.
	if err := socket.Write(writeCtx, websocket.MessageText, frame); err != nil {
		return wire.Welcome{}, fmt.Errorf("megastream: cannot send hello: %w", err)
	}

	reply, err := readFrame(ctx, socket, websocket.MessageText, handshakeTimeout)
	if err != nil {
		return wire.Welcome{}, err
	}
	answer, err := wire.DecodeHandshake(reply)
	if err != nil {
		return wire.Welcome{}, err
	}
	switch typed := answer.(type) {
	case wire.Welcome:
		if typed.Version != wire.ProtocolVersion {
			return wire.Welcome{}, fmt.Errorf(
				"megastream: server speaks protocol version %d, this relay speaks %d",
				typed.Version, wire.ProtocolVersion)
		}
		return typed, nil
	case wire.Goodbye:
		s.applyGoodbye(typed, policy)
		return wire.Welcome{}, fmt.Errorf("megastream: handshake refused: %s (%s)",
			typed.Reason, typed.Code)
	case wire.ErrorMessage:
		return wire.Welcome{}, fmt.Errorf("megastream: handshake failed: %s (%s)",
			typed.Message, typed.Code)
	default:
		return wire.Welcome{}, fmt.Errorf("megastream: unexpected handshake reply %T", answer)
	}
}

// applyGoodbye records what a close tells the reconnect schedule.
func (s *Session) applyGoodbye(goodbye wire.Goodbye, policy *reconnectPolicy) {
	policy.advise(time.Duration(goodbye.BackoffMs) * time.Millisecond)
	if wire.IsConfigurationError(goodbye.Code) {
		// These need an operator, so say what is wrong at a level someone will see and wait
		// a long time before asking again.
		s.logger.Error("MegaStream rejected this relay's configuration",
			"code", goodbye.Code, "reason", goodbye.Reason, "url", s.url.String())
		policy.park()
	}
}

// Register asks the server to register one more credential as a scope.
func (s *Session) Register(ctx context.Context, key string) error {
	conn := s.current.Load()
	if conn == nil {
		return ErrNotConnected
	}
	frame, err := conn.codec.EncodeScopeAdd(key)
	if err != nil {
		return err
	}
	return conn.send(ctx, frame)
}

// Deregister asks the server to stop serving a scope.
func (s *Session) Deregister(ctx context.Context, scope string) error {
	conn := s.current.Load()
	if conn == nil {
		return ErrNotConnected
	}
	frame, err := conn.codec.EncodeScopeRemove(scope)
	if err != nil {
		return err
	}
	return conn.send(ctx, frame)
}

// ReportError sends a diagnostic upstream. The message must not name a credential, because
// it is logged and forwarded more widely than a scope tag.
func (s *Session) ReportError(ctx context.Context, report wire.ErrorMessage) error {
	conn := s.current.Load()
	if conn == nil {
		return ErrNotConnected
	}
	frame, err := conn.codec.EncodeError(report)
	if err != nil {
		return err
	}
	return conn.send(ctx, frame)
}

func (s *Session) acknowledge(ctx context.Context, conn *connection) error {
	frame, err := conn.codec.EncodeHeartbeatAck()
	if err != nil {
		return err
	}
	if err := conn.send(ctx, frame); err != nil {
		return fmt.Errorf("megastream: cannot acknowledge heartbeat: %w", err)
	}
	return nil
}

// reportBadMessage tells the server the relay could not read something. The report is
// advisory and best effort: the connection is ending either way.
func (s *Session) reportBadMessage(ctx context.Context, cause error) {
	report := wire.ErrorMessage{
		Code:    wire.ErrorBadMessage,
		Message: "relay could not decode a message envelope",
	}
	if err := s.ReportError(ctx, report); err != nil {
		s.logger.Debug("could not report an undecodable frame upstream",
			"error", err, "cause", cause)
	}
}

// emit delivers one value to the consumer. It reports false when the context ended first.
func (s *Session) emit(ctx context.Context, msg any) bool {
	select {
	case s.out <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Session) dial(ctx context.Context) (*websocket.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	socket, resp, err := websocket.Dial(dialCtx, s.url.String(), &websocket.DialOptions{
		HTTPClient: s.params.HTTPClient,
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("megastream: cannot connect to %s: %w", s.url.String(), err)
	}
	return socket, nil
}

// readFrame reads one frame of the expected type, failing if none arrives within timeout. A
// timeout of zero waits indefinitely.
//
// The two framings are part of the contract: the handshake is text and everything after it
// is binary. A frame of the wrong type means the peer is not speaking this protocol, which
// is worth failing on rather than guessing at.
func readFrame(
	ctx context.Context,
	socket *websocket.Conn,
	want websocket.MessageType,
	timeout time.Duration,
) ([]byte, error) {
	readCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	kind, frame, err := socket.Read(readCtx)
	if err != nil {
		return nil, fmt.Errorf("megastream: read failed: %w", err)
	}
	if kind != want {
		return nil, fmt.Errorf("megastream: expected a %v frame, got %v", want, kind)
	}
	return frame, nil
}

// websocketURL forms the stream URL from the configured streaming URI.
//
// The socket scheme follows the configured one. Production is https, which becomes wss.
// Development against a local mock server is http, which becomes ws; hardcoding wss would
// make that impossible without giving every mock a certificate.
func websocketURL(base *url.URL) (*url.URL, error) {
	target := *base
	switch base.Scheme {
	case "https", "wss":
		target.Scheme = "wss"
	case "http", "ws":
		target.Scheme = "ws"
	default:
		return nil, fmt.Errorf("megastream: cannot derive a socket scheme from %q", base.Scheme)
	}
	target.Path = strings.TrimSuffix(target.Path, "/") + StreamPath
	return &target, nil
}

// sleep waits for d. It reports false when the context ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
