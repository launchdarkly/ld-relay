package retry

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testConfig mirrors the shape of a real binding with values that make the arithmetic easy
// to read: the normal curve runs 1s -> 2s -> 4s with a 4s ceiling, and the extended curve
// starts at 60s with a 240s ceiling.
func testConfig() Config {
	return Config{
		InitialDelay:         time.Second,
		NormalCeiling:        4 * time.Second,
		ExtendedInitialDelay: 60 * time.Second,
		ExtendedCeiling:      240 * time.Second,
		ResetThreshold:       30 * time.Second,
	}
}

// noJitter removes jitter so a test can assert an exact delay. Jitter itself is covered by
// TestJitterStaysWithinHalfTheDelay.
func noJitter() Option {
	return WithJitter(func(time.Duration) time.Duration { return 0 })
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestClassifyHTTPStatus(t *testing.T) {
	for _, p := range []struct {
		status int
		want   FailureClass
	}{
		{400, Normal},     // an intermediary can produce this
		{401, Unexpected}, // invalid credential
		{403, Unexpected}, // invalid credential
		{404, Unexpected},
		{408, Normal}, // timeout
		{418, Unexpected},
		{429, Normal}, // asked to slow down
		{500, Normal},
		{502, Normal},
		{503, Normal},
	} {
		t.Run(fmt.Sprint(p.status), func(t *testing.T) {
			assert.Equal(t, p.want, ClassifyHTTPStatus(p.status))
		})
	}
}

func TestClassifyTransportError(t *testing.T) {
	assert.Equal(t, Normal, ClassifyTransportError(nil))
	assert.Equal(t, Normal, ClassifyTransportError(errors.New("connection refused")))

	// A certificate problem needs operator action, so it is not transient.
	assert.Equal(t, Unexpected, ClassifyTransportError(&tls.CertificateVerificationError{}))
	assert.Equal(t, Unexpected, ClassifyTransportError(x509.UnknownAuthorityError{}))
	assert.Equal(t, Unexpected, ClassifyTransportError(x509.HostnameError{Host: "example.com"}))
	assert.Equal(t, Unexpected, ClassifyTransportError(x509.CertificateInvalidError{}))

	// Classification looks through a wrapped error.
	assert.Equal(t, Unexpected,
		ClassifyTransportError(fmt.Errorf("dial failed: %w", x509.UnknownAuthorityError{})))
}

func TestNormalCurveDoublesAndStopsAtTheCeiling(t *testing.T) {
	s := NewStrategy(testConfig(), noJitter())

	for _, want := range []time.Duration{
		time.Second,     // 1s * 2^0
		2 * time.Second, // 1s * 2^1
		4 * time.Second, // 1s * 2^2
		4 * time.Second, // clamped
		4 * time.Second, // clamped
	} {
		require.False(t, s.OnFailure(Normal))
		assert.Equal(t, want, s.NextWait())
	}
	assert.False(t, s.InExtendedRegime())
}

func TestFirstUnexpectedFailureWaitsTheExtendedBaseDelay(t *testing.T) {
	s := NewStrategy(testConfig(), noJitter())

	// Accumulate normal failures first, so the test also shows that the extended curve
	// starts from its own base rather than continuing the normal curve's exponent.
	s.OnFailure(Normal)
	s.OnFailure(Normal)
	require.Equal(t, 2*time.Second, s.NextWait())

	assert.True(t, s.OnFailure(Unexpected), "the first unexpected failure reports the change")
	assert.True(t, s.InExtendedRegime())
	assert.Equal(t, 60*time.Second, s.NextWait(), "the first extended wait is the extended base delay")
}

func TestExtendedCurveDoublesAndStopsAtTheExtendedCeiling(t *testing.T) {
	s := NewStrategy(testConfig(), noJitter())

	require.True(t, s.OnFailure(Unexpected))
	for _, want := range []time.Duration{
		60 * time.Second,  // 60s * 2^0
		120 * time.Second, // 60s * 2^1
		240 * time.Second, // 60s * 2^2
		240 * time.Second, // clamped
	} {
		assert.Equal(t, want, s.NextWait())
		assert.False(t, s.OnFailure(Unexpected), "only the first unexpected failure reports the change")
	}
}

func TestNormalFailureDoesNotLowerTheExtendedCeiling(t *testing.T) {
	s := NewStrategy(testConfig(), noJitter())

	require.True(t, s.OnFailure(Unexpected))
	require.Equal(t, 60*time.Second, s.NextWait())

	// A normal failure after an unexpected one keeps the raised ceiling, so a service that
	// alternates between a 401 and a 503 does not fall back to the fast curve.
	s.OnFailure(Normal)
	assert.Equal(t, 120*time.Second, s.NextWait())
	assert.True(t, s.InExtendedRegime())
}

func TestHealthyOperationResetsOnlyAfterTheThreshold(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	s := NewStrategy(testConfig(), noJitter(), WithClock(clock.now))

	require.True(t, s.OnFailure(Unexpected))
	require.Equal(t, 60*time.Second, s.NextWait())

	// Start a healthy period, then report health again just short of the threshold.
	s.OnHealthy()
	clock.advance(29 * time.Second)
	s.OnHealthy()
	assert.True(t, s.InExtendedRegime(), "29s of health is below the 30s threshold")

	clock.advance(time.Second)
	s.OnHealthy()
	assert.False(t, s.InExtendedRegime(), "30s of continuous health returns to the normal curve")

	s.OnFailure(Normal)
	assert.Equal(t, time.Second, s.NextWait(), "the reset restored the normal base delay")
}

func TestFailureBreaksTheHealthyPeriod(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	s := NewStrategy(testConfig(), noJitter(), WithClock(clock.now))

	require.True(t, s.OnFailure(Unexpected))

	s.OnHealthy()
	clock.advance(29 * time.Second)

	// A failure one second before the threshold means the clock starts over, so the
	// following stretch of health does not reset even though the two spans together exceed
	// the threshold.
	s.OnFailure(Normal)
	s.OnHealthy()
	clock.advance(29 * time.Second)
	s.OnHealthy()

	assert.True(t, s.InExtendedRegime(), "health must be continuous to count")
}

func TestJitterStaysWithinHalfTheDelay(t *testing.T) {
	s := NewStrategy(testConfig())
	s.OnFailure(Normal)
	s.OnFailure(Normal)
	s.OnFailure(Normal) // exponent puts the undithered delay at the 4s ceiling

	sawBelowCeiling := false
	for range 200 {
		wait := s.NextWait()
		assert.GreaterOrEqual(t, wait, 2*time.Second, "jitter removes at most half the delay")
		assert.LessOrEqual(t, wait, 4*time.Second, "jitter never adds to the delay")
		if wait < 4*time.Second {
			sawBelowCeiling = true
		}
	}
	assert.True(t, sawBelowCeiling, "jitter should actually vary the delay")
}

func TestNextWaitBeforeAnyFailure(t *testing.T) {
	// Nothing calls NextWait before a failure, but it must not raise 2 to a negative power
	// if something ever does.
	s := NewStrategy(testConfig(), noJitter())
	assert.Equal(t, time.Second, s.NextWait())
}
