// Package retry implements the backoff behavior that LaunchDarkly's RETRY specification
// defines for long-running components.
//
// A long-running component repeatedly initiates operations against one service, and its
// lifetime matches the lifetime of the process. The Relay Proxy's big segment synchronizer
// and auto-configuration stream are both long-running components. No response and no
// transport failure makes such a component stop permanently. Instead the component sorts
// each failure into one of two classes and backs off proportionally.
//
// A "normal" failure is transient, so the component retries on a short curve. An
// "unexpected" failure is unlikely to correct itself soon, so the component raises its
// ceiling and retries far more slowly. An invalid credential is the motivating example: the
// component keeps trying, because an operator can make the credential valid again without
// the component knowing, but it must not hammer the service in the meantime.
package retry

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math"
	"math/rand"
	"time"
)

// FailureClass sorts a failure by how likely it is to correct itself soon.
type FailureClass int

const (
	// Normal is a transient failure. The component retries on the normal curve.
	Normal FailureClass = iota
	// Unexpected is a failure that is unlikely to correct itself soon. The component raises
	// its ceiling to the extended value and keeps retrying.
	Unexpected
)

// ClassifyHTTPStatus returns the class for a failed HTTP response.
//
// Call this only for a status that the caller treats as a failure. Most 4xx statuses mean a
// bad credential or a bad request shape, and neither corrects itself quickly. The three
// exceptions are transient: a 400 can come from an intermediary, a 408 is a timeout, and a
// 429 asks the caller to slow down.
func ClassifyHTTPStatus(statusCode int) FailureClass {
	if statusCode >= 400 && statusCode < 500 {
		switch statusCode {
		case 400, 408, 429:
			return Normal
		default:
			return Unexpected
		}
	}
	return Normal
}

// ClassifyTransportError returns the class for a transport-level failure.
//
// A TLS or certificate failure comes from a misconfiguration, such as an expired
// certificate or an untrusted issuer, so it does not correct itself without operator
// action. Every other network failure is transient.
func ClassifyTransportError(err error) FailureClass {
	if err == nil {
		return Normal
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return Unexpected
	}
	var unknownAuthorityErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityErr) {
		return Unexpected
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return Unexpected
	}
	var invalidErr x509.CertificateInvalidError
	if errors.As(err, &invalidErr) {
		return Unexpected
	}
	return Normal
}

// Config holds the timing values for one component. Each component binds its own values.
type Config struct {
	// InitialDelay is the base delay for the normal curve.
	InitialDelay time.Duration
	// NormalCeiling is the largest delay the normal curve produces.
	NormalCeiling time.Duration
	// ExtendedInitialDelay is the base delay after the first unexpected failure.
	ExtendedInitialDelay time.Duration
	// ExtendedCeiling is the largest delay the extended curve produces.
	ExtendedCeiling time.Duration
	// ResetThreshold is how long the component must operate healthily before its retry
	// state returns to the normal curve.
	ResetThreshold time.Duration
}

// Strategy holds the retry state for one component and computes each wait.
//
// A Strategy is not safe for concurrent use. Each component owns one Strategy and calls it
// from a single goroutine.
type Strategy struct {
	cfg    Config
	now    func() time.Time
	jitter func(limit time.Duration) time.Duration

	// attempts is the exponent input. NextWait raises 2 to attempts-1.
	attempts int
	// maxDelay is the current ceiling. It rises to the extended value on an unexpected
	// failure and only a reset lowers it again.
	maxDelay time.Duration
	// initialDelay is the base delay for the current curve.
	initialDelay time.Duration
	// inExtended records whether the extended curve is active, so that the first unexpected
	// failure is told apart from later ones.
	inExtended bool
	// healthySince is when the current unbroken healthy period started. A zero value means
	// the component is not currently healthy.
	healthySince time.Time
}

// Option changes how a Strategy measures time or computes jitter. Tests use these to make
// the output deterministic.
type Option func(*Strategy)

// WithClock replaces the clock the reset condition reads.
func WithClock(now func() time.Time) Option {
	return func(s *Strategy) { s.now = now }
}

// WithJitter replaces the jitter source. The function receives the exclusive upper bound
// and returns the amount to subtract from the computed delay.
func WithJitter(jitter func(limit time.Duration) time.Duration) Option {
	return func(s *Strategy) { s.jitter = jitter }
}

// NewStrategy creates a Strategy for one component.
func NewStrategy(cfg Config, options ...Option) *Strategy {
	//nolint:gosec // jitter is not a cryptographic use, so a weak source is fine
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	s := &Strategy{
		cfg:          cfg,
		now:          time.Now,
		jitter:       func(limit time.Duration) time.Duration { return time.Duration(rng.Int63n(int64(limit))) },
		maxDelay:     cfg.NormalCeiling,
		initialDelay: cfg.InitialDelay,
	}
	for _, o := range options {
		o(s)
	}
	return s
}

// OnFailure records a failure and returns true if this call activated the extended curve.
// The caller can use the return value to log the change exactly once.
//
// A failure always ends the current healthy period, so the reset condition starts over.
func (s *Strategy) OnFailure(class FailureClass) (engagedExtended bool) {
	s.healthySince = time.Time{}

	if class == Unexpected {
		// The ceiling never comes back down except through a reset, so raise it before
		// checking whether this is the first unexpected failure.
		s.maxDelay = s.cfg.ExtendedCeiling
		if !s.inExtended {
			// Start the extended curve at its own base delay rather than continuing the
			// normal curve's exponent, so the first extended wait is exactly
			// ExtendedInitialDelay.
			s.attempts = 1
			s.initialDelay = s.cfg.ExtendedInitialDelay
			s.inExtended = true
			return true
		}
	}

	s.attempts++
	return false
}

// OnHealthy records that the component is doing what it exists to do. The caller reports
// this repeatedly while healthy. Once the component has been healthy without interruption
// for the reset threshold, the retry state returns to the normal curve.
func (s *Strategy) OnHealthy() {
	now := s.now()
	if s.healthySince.IsZero() {
		s.healthySince = now
		return
	}
	if now.Sub(s.healthySince) >= s.cfg.ResetThreshold {
		s.attempts = 0
		s.maxDelay = s.cfg.NormalCeiling
		s.initialDelay = s.cfg.InitialDelay
		s.inExtended = false
	}
}

// NextWait returns how long to wait before the next attempt.
//
// The delay doubles with each accumulated failure and stops growing at the current ceiling.
// Jitter then removes up to half of it. Jitter matters because a server that closes many
// connections at once would otherwise make every Relay Proxy retry in the same instant.
func (s *Strategy) NextWait() time.Duration {
	if s.attempts <= 0 {
		return s.cfg.InitialDelay
	}
	delay := time.Duration(math.Min(
		float64(s.initialDelay)*math.Pow(2, float64(s.attempts-1)),
		float64(s.maxDelay),
	))
	if halfDelay := delay / 2; halfDelay > 0 {
		delay -= s.jitter(halfDelay)
	}
	return delay
}

// InExtendedRegime reports whether the extended curve is active. Status reporting and tests
// use this; the wait computation does not.
func (s *Strategy) InExtendedRegime() bool {
	return s.inExtended
}
