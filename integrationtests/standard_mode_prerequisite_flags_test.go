//go:build integrationtests

package integrationtests

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func withStandardModePrerequisitesTestData(t *testing.T, manager *integrationTestManager, fn func(data standardModeTestData)) {
	project, envs, err := manager.apiHelper.createProject(1)
	require.NoError(t, err)
	defer manager.apiHelper.deleteProject(project)

	manager.apiHelper.createFlag()

	testData := standardModeTestData{
		projsAndEnvs: projsAndEnvs,
	}
	// This is here because the backend takes a while to setup the filters, otherwise we get 404s when connecting.
	time.Sleep(10 * time.Second)
	fn(testData)
}
