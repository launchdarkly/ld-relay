package logging

import (
	"testing"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"

	"github.com/stretchr/testify/assert"
)

func TestDefaultLoggers(t *testing.T) {
	loggers := MakeDefaultLoggers()
	assert.Equal(t, ldlog.Info, loggers.GetMinLevel())
}
