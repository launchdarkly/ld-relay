package relay

import (
	"github.com/launchdarkly/ld-relay/v9/config"
	"github.com/launchdarkly/ld-relay/v9/internal/initwrite"
)

// newClientWriteLimits returns the write-deadline limits that the Main.ClientWrite* options
// configure, or nil if Main.ClientWriteMinBytesPerSecond is not set. config.ValidateConfig has
// already checked that the slack is positive and that the cap is at least the slack.
func newClientWriteLimits(c config.MainConfig) *initwrite.Limits {
	if !c.ClientWriteMinBytesPerSecond.IsDefined() {
		return nil
	}
	return &initwrite.Limits{
		MinBytesPerSecond: c.ClientWriteMinBytesPerSecond.GetOrElse(0),
		Slack:             c.ClientWriteSlack.GetOrElse(config.DefaultClientWriteSlack),
		MaxHold:           c.ClientWriteMaxTime.GetOrElse(0),
	}
}
