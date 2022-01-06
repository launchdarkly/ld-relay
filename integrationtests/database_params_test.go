//go:build integrationtests
// +build integrationtests

package integrationtests

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/launchdarkly/ld-relay/v6/integrationtests/docker"
	"github.com/launchdarkly/ld-relay/v6/internal/core"

	"github.com/stretchr/testify/require"
)

const (
	dynamoDBTableName = "test-table"
	awsRegion         = "us-east-1"
	awsKey            = "fake-user"
	awsSecret         = "fake-secret"
)

type databaseTestParams struct {
	dbImageName      string
	dbDockerParams   []string
	hostnamePrefix   string
	setupFn          func(*integrationTestManager, *docker.Container) error
	envVarsFn        func(*docker.Container) map[string]string
	expectedStatusFn func(*docker.Container) core.DataStoreStatusRep
}

func (p databaseTestParams) withContainer(t *testing.T, manager *integrationTestManager, action func(*docker.Container)) {
	manager.withExtraContainer(t, p.dbImageName, p.dbDockerParams, p.hostnamePrefix, func(dbContainer *docker.Container) {
		containersOnNetwork, err := manager.dockerNetwork.GetContainerIDs()
		require.NoError(t, err)
		require.Len(t, containersOnNetwork, 1, "database container did not start or did not attach to the test network")

		if p.setupFn != nil {
			require.NoError(t, p.setupFn(manager, dbContainer))
		}

		action(dbContainer)
	})
}

func (p databaseTestParams) withStartedRelay(
	t *testing.T,
	manager *integrationTestManager,
	dbContainer *docker.Container,
	environments []environmentInfo,
	addVars map[string]string,
	action func(core.StatusRep),
) {
	vars := p.envVarsFn(dbContainer)
	for k, v := range addVars {
		vars[k] = v
	}
	for _, env := range environments {
		vars["LD_ENV_"+env.name] = string(env.sdkKey)
		vars["LD_PREFIX_"+env.name] = env.prefix
	}
	manager.startRelay(t, vars)
	defer manager.stopRelay(t)

	expectedDataStoreStatuses := make(map[string]core.DataStoreStatusRep)
	for _, env := range environments {
		expected := p.expectedStatusFn(dbContainer)
		expected.State = "VALID"
		expected.StateSince = 0
		expected.DBPrefix = env.prefix
		expectedDataStoreStatuses[env.name] = expected
	}

	lastStatus, success := manager.awaitRelayStatus(t, func(status core.StatusRep) bool {
		for key, envRep := range status.Environments {
			envRep.DataStoreStatus.StateSince = 0
			status.Environments[key] = envRep
		}
		if len(status.Environments) == len(environments) {
			allStatuses := make(map[string]core.DataStoreStatusRep)
			for key, rep := range status.Environments {
				allStatuses[key] = rep.DataStoreStatus
			}
			return reflect.DeepEqual(allStatuses, expectedDataStoreStatuses)
		}
		return false
	})
	if !success {
		fmt.Println("Expected to see data store statuses:", expectedDataStoreStatuses)
		jsonStatus, _ := json.Marshal(lastStatus)
		fmt.Println("Last status received was:", string(jsonStatus))
	}

	action(lastStatus)
}

var redisDatabaseTestParams = databaseTestParams{
	dbImageName:    "redis",
	hostnamePrefix: "redis",
	envVarsFn: func(dbContainer *docker.Container) map[string]string {
		return map[string]string{
			"USE_REDIS":  "true",
			"REDIS_HOST": dbContainer.GetName(),
		}
	},
	expectedStatusFn: func(dbContainer *docker.Container) core.DataStoreStatusRep {
		return core.DataStoreStatusRep{
			Database: "redis",
			DBServer: fmt.Sprintf("redis://%s:6379", dbContainer.GetName()),
		}
	},
}

var redisWithPasswordDatabaseTestParams = databaseTestParams{
	dbImageName:    "redis",
	dbDockerParams: []string{"--requirepass", "secret"},
	hostnamePrefix: "redis",
	envVarsFn: func(dbContainer *docker.Container) map[string]string {
		return map[string]string{
			"USE_REDIS":      "true",
			"REDIS_HOST":     dbContainer.GetName(),
			"REDIS_PASSWORD": "secret",
		}
	},
	expectedStatusFn: func(dbContainer *docker.Container) core.DataStoreStatusRep {
		return core.DataStoreStatusRep{
			Database: "redis",
			DBServer: fmt.Sprintf("redis://%s:6379", dbContainer.GetName()),
		}
	},
}

var consulDatabaseTestParams = databaseTestParams{
	dbImageName:    "consul",
	hostnamePrefix: "consul",
	envVarsFn: func(dbContainer *docker.Container) map[string]string {
		return map[string]string{
			"USE_CONSUL":  "true",
			"CONSUL_HOST": makeConsulAddress(dbContainer),
		}
	},
	expectedStatusFn: func(dbContainer *docker.Container) core.DataStoreStatusRep {
		return core.DataStoreStatusRep{
			Database: "consul",
			DBServer: makeConsulAddress(dbContainer),
		}
	},
}

func makeConsulAddress(dbContainer *docker.Container) string {
	return fmt.Sprintf("%s:8500", dbContainer.GetName())
}

var dynamoDBDatabaseTestParams = databaseTestParams{
	dbImageName:    "amazon/dynamodb-local",
	hostnamePrefix: "dynamodb",
	setupFn:        dynamoDBSetup,
	envVarsFn: func(dbContainer *docker.Container) map[string]string {
		return map[string]string{
			"USE_DYNAMODB":          "true",
			"DYNAMODB_TABLE":        dynamoDBTableName,
			"DYNAMODB_URL":          dynamoDBEndpointURL(dbContainer),
			"AWS_REGION":            awsRegion,
			"AWS_ACCESS_KEY_ID":     awsKey,
			"AWS_SECRET_ACCESS_KEY": awsSecret,
		}
	},
	expectedStatusFn: func(dbContainer *docker.Container) core.DataStoreStatusRep {
		return core.DataStoreStatusRep{
			Database: "dynamodb",
			DBServer: dynamoDBEndpointURL(dbContainer),
			DBTable:  dynamoDBTableName,
		}
	},
}

func dynamoDBSetup(manager *integrationTestManager, dbContainer *docker.Container) error {
	awsCLIImage, err := docker.PullImage("amazon/aws-cli")
	if err != nil {
		return err
	}
	return awsCLIImage.NewContainerBuilder().
		Network(manager.dockerNetwork).
		EnvVar("AWS_REGION", awsRegion).
		EnvVar("AWS_ACCESS_KEY_ID", awsKey).
		EnvVar("AWS_SECRET_ACCESS_KEY", awsSecret).
		EnvVar("AWS_MAX_ATTEMPTS", "10"). // increase AWS CLI retries because DynamoDB container might be slow to start
		ContainerParams("dynamodb", "create-table",
			"--endpoint-url", dynamoDBEndpointURL(dbContainer),
			"--table-name", dynamoDBTableName,
			"--attribute-definitions", "AttributeName=namespace,AttributeType=S", "AttributeName=key,AttributeType=S",
			"--key-schema", "AttributeName=namespace,KeyType=HASH", "AttributeName=key,KeyType=RANGE",
			"--provisioned-throughput", "ReadCapacityUnits=1,WriteCapacityUnits=1",
		).Run()
}

func dynamoDBEndpointURL(dbContainer *docker.Container) string {
	return fmt.Sprintf("http://%s:8000", dbContainer.GetName())
}
