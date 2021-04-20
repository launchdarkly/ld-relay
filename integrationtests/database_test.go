// +build integrationtests

package integrationtests

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v6/integrationtests/docker"

	"github.com/stretchr/testify/require"
)

// See database_params_test.go which defines database-specific behavior

func testDatabaseIntegrations(t *testing.T, manager *integrationTestManager) {
	t.Run("Redis", func(t *testing.T) {
		doDatabaseTest(t, manager, redisDatabaseTestParams)
	})

	t.Run("Consul", func(t *testing.T) {
		doDatabaseTest(t, manager, consulDatabaseTestParams)
	})

	t.Run("DynamoDB", func(t *testing.T) {
		doDatabaseTest(t, manager, dynamoDBDatabaseTestParams)
	})
}

func doDatabaseTest(
	t *testing.T,
	manager *integrationTestManager,
	dbParams databaseTestParams,
) {
	dbParams.withContainer(t, manager, func(dbContainer *docker.Container) {
		projectInfo, environments, err := manager.apiHelper.createProject(2)
		require.NoError(t, err)
		defer manager.apiHelper.deleteProject(projectInfo)

		environments[0].prefix = "prefix1"
		environments[1].prefix = "prefix2"

		dbParams.withStartedRelay(t, manager, dbContainer, environments, nil, func() {
			// withStartedRelay verifies that it started up correctly and is reporting
			// a valid data store status - we don't need to do anything additional here
		})
	})
}
