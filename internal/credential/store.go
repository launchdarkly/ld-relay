package credential

import (
	"github.com/launchdarkly/ld-relay/v8/config"
	"time"
)

type rotatedKey struct {
	key     config.SDKKey
	expired bool
	expiry  time.Time
}

func (r *rotatedKey) deprecated() bool {
	return !r.expiry.IsZero()
}

func (r *rotatedKey) preferred() bool {
	return !r.deprecated()
}

type Store struct {
	// Can be rotated. The tail of this list is the active key.
	mobileKeys []config.MobileKey
	// Can be rotated, with a deprecation period. The tail of this list is the preferred key.
	sdkKeys []config.SDKKey
	// Can never change
	envID config.EnvironmentID
}
