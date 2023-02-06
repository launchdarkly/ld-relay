//go:build integrationtests
// +build integrationtests

package integrationtests

import (
	"testing"

	"github.com/launchdarkly/ld-relay/v7/integrationtests/docker"
	"github.com/launchdarkly/ld-relay/v7/internal/api"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// See database_params_test.go which defines database-specific behavior

func testDatabaseIntegrations(t *testing.T, manager *integrationTestManager) {
	t.Run("Redis", func(t *testing.T) {
		doDatabaseTest(t, manager, redisDatabaseTestParams)
	})

	t.Run("Redis with password", func(t *testing.T) {
		doDatabaseTest(t, manager, redisWithPasswordDatabaseTestParams)
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

		dbParams.withStartedRelay(t, manager, dbContainer, environments, nil, func(status api.StatusRep) {
			// withStartedRelay has already verified that it started up correctly and is reporting
			// a valid data store status; we'll just additionally verify that it is *not* reporting
			// any Big Segment status, since there isn't any Big Segments data
			for _, e := range status.Environments {
				assert.Nil(t, e.BigSegmentStatus)
			}
		})
	})
}
