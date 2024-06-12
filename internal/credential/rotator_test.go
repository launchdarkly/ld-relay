package credential

import (
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

type testCred string

func (k testCred) Compare(_ AutoConfig) (SDKCredential, Status) {
	return nil, Unchanged
}

func (k testCred) GetAuthorizationHeaderValue() string { return "" }

func (k testCred) Defined() bool {
	return true
}

func (k testCred) String() string {
	return string(k)
}

func (k testCred) Masked() string { return "masked<" + string(k) + ">" }

func TestNewRotator(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()
	rotator := NewRotator(mockLog.Loggers, time.Now)
	assert.NotNil(t, rotator)
}

func TestDeprecation(t *testing.T) {
	mockLog := ldlogtest.NewMockLog()

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)

	rotator := NewRotator(mockLog.Loggers, func() time.Time { return now })
	defer rotator.Stop()

	cred := testCred("foobar-1234")

	assert.True(t, rotator.Deprecate(cred, future))
	assert.True(t, rotator.Deprecated(cred))
	assert.False(t, rotator.Expired(cred))
}
