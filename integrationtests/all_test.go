//go:build integrationtests
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

	defer manager.close(t)

	t.Run("standard mode", func(t *testing.T) {
		testStandardMode(t, manager)
	})

	t.Run("auto-configuration mode", func(t *testing.T) {
		testAutoConfig(t, manager)
	})

	t.Run("offline mode", func(t *testing.T) {
		testOfflineMode(t, manager)
	})

	t.Run("database integrations", func(t *testing.T) {
		testDatabaseIntegrations(t, manager)
	})

	t.Run("big segments", func(t *testing.T) {
		testBigSegments(t, manager)
	})
}
