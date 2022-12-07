package logging

import (
	"testing"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"

	"github.com/stretchr/testify/assert"
)

func TestDefaultLoggers(t *testing.T) {
	loggers := MakeDefaultLoggers()
	assert.Equal(t, ldlog.Info, loggers.GetMinLevel())
}
