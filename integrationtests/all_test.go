//go:build integrationtests

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

	t.Run("standard mode with payload filters", func(t *testing.T) {
		t.Run("default filters", func(t *testing.T) {
			// This case is similar to the "standard mode" test above, except with payload filtering in the picture.
			// The test setup creates filters (with no rules), which should result in filtered environments that are
			// *equivalent* to the normal, non-filtered ones.
			//
			// The purpose is to assert that Relay can connect to the right amount of filtered and unfiltered environments,
			// with expected flag data present - e.g. "2 projects * 2 environments * 2 filters = 12 environments"
			// (8 filtered, plus 4 unfiltered).
			testStandardModeWithDefaultFilters(t, manager)
		})

		t.Run("specific filters", func(t *testing.T) {
			// This case is intended to check that payload filtering functionality actually works - that is, subsets of
			// flag data are downloaded, rather than all flags in an environment.
			//
			// It does so by setting up an "odd" filter and an "even" filter, which limit flags to those with keys like
			// flag0246... and flag1357...
			// It creates an odd and even flag, and then asserts that the flags present in the filtered environments are
			// as expected.
			testStandardModeWithSpecificFilters(t, manager)
		})
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
