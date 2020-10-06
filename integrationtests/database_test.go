// +build integrationtests

package integrationtests

import (
	"fmt"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/integrationtests/docker"
	"github.com/launchdarkly/ld-relay/v6/integrationtests/oshelpers"
	"github.com/launchdarkly/ld-relay/v6/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDatabaseIntegrations(t *testing.T, manager *integrationTestManager) {
	t.Run("Redis", func(t *testing.T) {
		testRedisIntegration(t, manager)
	})

	t.Run("Consul", func(t *testing.T) {
		testConsulIntegration(t, manager)
	})

	t.Run("DynamoDB", func(t *testing.T) {
		testDynamoDBIntegration(t, manager)
	})
}

func doDatabaseTest(
	t *testing.T,
	manager *integrationTestManager,
	dbImageName string,
	hostnamePrefix string,
	setupFn func(dbContainer *docker.Container) error,
	envVarsFn func(dbContainer *docker.Container) map[string]string,
	expectedStatusFn func(dbContainer *docker.Container) core.DataStoreStatusRep,
) {
	manager.withExtraContainer(t, dbImageName, hostnamePrefix, func(dbContainer *docker.Container) {
		containersOnNetwork, err := manager.dockerNetwork.GetContainerIDs()
		require.Len(t, containersOnNetwork, 1, "database container did not start or did not attach to the test network")

		if setupFn != nil {
			require.NoError(t, setupFn(dbContainer))
		}

		projectInfo, environments, err := manager.createProject(2)
		require.NoError(t, err)
		defer manager.deleteProject(projectInfo)

		env1Name, env2Name := "env1", "env2"
		env1Prefix, env2Prefix := "prefix1", "prefix2"

		envVars := map[string]string{
			"LD_ENV_" + env1Name:    string(environments[0].sdkKey),
			"LD_PREFIX_" + env1Name: env1Prefix,
			"LD_ENV_" + env2Name:    string(environments[1].sdkKey),
			"LD_PREFIX_" + env2Name: env2Prefix,
		}
		for k, v := range envVarsFn(dbContainer) {
			envVars[k] = v
		}
		manager.startRelay(t, envVars)
		defer manager.stopRelay()

		expectedBase := expectedStatusFn(dbContainer)
		expectedBase.State = "VALID"
		expected1 := expectedBase
		expected1.DBPrefix = env1Prefix
		expected2 := expectedBase
		expected2.DBPrefix = env2Prefix

		statusWithoutTimestamp := func(s core.DataStoreStatusRep) core.DataStoreStatusRep {
			s.StateSince = 0
			return s
		}

		lastStatus, success := manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
			if len(status.Environments) == 2 {
				return expected1 == statusWithoutTimestamp(status.Environments[env1Name].DataStoreStatus) &&
					expected2 == statusWithoutTimestamp(status.Environments[env2Name].DataStoreStatus)
			}
			return false
		})
		if !success {
			// We already know these values don't match the expected values, but calling assert.Equal will
			// give us helpful error output showing which parts didn't match.
			assert.Equal(t, expected1, statusWithoutTimestamp(lastStatus.Environments[env1Name].DataStoreStatus))
			assert.Equal(t, expected2, statusWithoutTimestamp(lastStatus.Environments[env2Name].DataStoreStatus))
		}
	})
}

func testRedisIntegration(t *testing.T, manager *integrationTestManager) {
	doDatabaseTest(t, manager, "redis", "redis",
		nil,
		func(dbContainer *docker.Container) map[string]string {
			return map[string]string{
				"USE_REDIS":  "true",
				"REDIS_HOST": dbContainer.GetName(),
			}
		},
		func(dbContainer *docker.Container) core.DataStoreStatusRep {
			return core.DataStoreStatusRep{
				Database: "redis",
				DBServer: fmt.Sprintf("redis://%s:6379", dbContainer.GetName()),
			}
		},
	)
}

func testConsulIntegration(t *testing.T, manager *integrationTestManager) {
	consulAddress := func(dbContainer *docker.Container) string {
		return fmt.Sprintf("%s:8500", dbContainer.GetName())
	}

	doDatabaseTest(t, manager, "consul", "consul",
		nil,
		func(dbContainer *docker.Container) map[string]string {
			return map[string]string{
				"USE_CONSUL":  "true",
				"CONSUL_HOST": consulAddress(dbContainer),
			}
		},
		func(dbContainer *docker.Container) core.DataStoreStatusRep {
			return core.DataStoreStatusRep{
				Database: "consul",
				DBServer: consulAddress(dbContainer),
			}
		},
	)
}

func testDynamoDBIntegration(t *testing.T, manager *integrationTestManager) {
	tableName := "test-table"
	awsRegion := "us-east-1"
	awsKey := "fake-user"
	awsSecret := "fake-secret"
	ddbEndpointURL := func(dbContainer *docker.Container) string {
		return fmt.Sprintf("http://%s:8000", dbContainer.GetName())
	}

	doDatabaseTest(t, manager, "amazon/dynamodb-local", "dynamodb",
		func(dbContainer *docker.Container) error {
			cliParams := []string{
				"run", "--rm",
				"-e", "AWS_REGION=" + awsRegion,
				"-e", "AWS_ACCESS_KEY_ID=" + awsKey,
				"-e", "AWS_SECRET_ACCESS_KEY=" + awsSecret,
				"-e", "AWS_MAX_ATTEMPTS=10", // increase AWS CLI retries because DynamoDB container might be slow to start
				"--network", manager.dockerNetwork.GetName(),
				"amazon/aws-cli",
				"dynamodb", "create-table",
				"--endpoint-url", ddbEndpointURL(dbContainer),
				"--table-name", tableName,
				"--attribute-definitions", "AttributeName=namespace,AttributeType=S", "AttributeName=key,AttributeType=S",
				"--key-schema", "AttributeName=namespace,KeyType=HASH", "AttributeName=key,KeyType=RANGE",
				"--provisioned-throughput", "ReadCapacityUnits=1,WriteCapacityUnits=1",
			}
			return oshelpers.Command("docker", cliParams...).Run()
		},
		func(dbContainer *docker.Container) map[string]string {
			return map[string]string{
				"USE_DYNAMODB":          "true",
				"DYNAMODB_TABLE":        tableName,
				"DYNAMODB_URL":          ddbEndpointURL(dbContainer),
				"AWS_REGION":            awsRegion,
				"AWS_ACCESS_KEY_ID":     awsKey,
				"AWS_SECRET_ACCESS_KEY": awsSecret,
			}
		},
		func(dbContainer *docker.Container) core.DataStoreStatusRep {
			return core.DataStoreStatusRep{
				Database: "dynamodb",
				DBServer: ddbEndpointURL(dbContainer),
				DBTable:  tableName,
			}
		},
	)
}
