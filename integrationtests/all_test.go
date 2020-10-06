// +build integrationtests

package integrationtests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// See package_info.go for how these integration tests are configured and run.

func TestEndToEnd(t *testing.T) {
	// All of the end-to-end tests are grouped together here so that we only have to do the
	// newIntegrationTestManager step once; that step is slow because it has to create the Relay
	// Docker image from scratch.
	manager, err := newIntegrationTestManager()
	require.NoError(t, err)

	defer manager.close()

	t.Run("auto-config", func(t *testing.T) {
		testAutoConfig(t, manager)
	})
}
