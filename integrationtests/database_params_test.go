//go:build integrationtests

package integrationtests

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/launchdarkly/ld-relay/v9/integrationtests/docker"
	"github.com/launchdarkly/ld-relay/v9/internal/api"
	"github.com/launchdarkly/ld-relay/v9/internal/sharedtest"

	"github.com/stretchr/testify/require"
)

const (
	dynamoDBTableName = "test-table"
	awsRegion         = "us-east-1"
	awsKey            = "FAKEUSER"
	awsSecret         = "FAKESECRET"
)

type databaseTestParams struct {
	dbImageName    string
	dbDockerParams []string
	hostnamePrefix string
	// mountFn, if set, is called with the database container's hostname before the container is
	// created, and returns a host directory to mount into it.
	mountFn          func(t *testing.T, m *integrationTestManager, hostname string) (hostDir, containerDir string, err error)
	setupFn          func(*integrationTestManager, *docker.Container) error
	envVarsFn        func(*docker.Container) map[string]string
	expectedStatusFn func(*docker.Container) api.DataStoreStatusRep
}

func (p databaseTestParams) withContainer(t *testing.T, manager *integrationTestManager, action func(*docker.Container)) {
	var mountFn func(string) (string, string, error)
	if p.mountFn != nil {
		mountFn = func(hostname string) (string, string, error) { return p.mountFn(t, manager, hostname) }
	}
	manager.withExtraContainer(t, p.dbImageName, p.dbDockerParams, p.hostnamePrefix, mountFn, func(dbContainer *docker.Container) {
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
	action func(api.StatusRep),
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

	expectedDataStoreStatuses := make(map[string]api.DataStoreStatusRep)
	for _, env := range environments {
		expected := p.expectedStatusFn(dbContainer)
		expected.State = "VALID"
		expected.StateSince = 0
		expected.DBPrefix = env.prefix
		expectedDataStoreStatuses[env.name] = expected
	}

	lastStatus, success := manager.awaitRelayStatus(t, func(status api.StatusRep) bool {
		for key, envRep := range status.Environments {
			envRep.DataStoreStatus.StateSince = 0
			status.Environments[key] = envRep
		}
		if len(status.Environments) == len(environments) {
			allStatuses := make(map[string]api.DataStoreStatusRep)
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
	expectedStatusFn: func(dbContainer *docker.Container) api.DataStoreStatusRep {
		return api.DataStoreStatusRep{
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
	expectedStatusFn: func(dbContainer *docker.Container) api.DataStoreStatusRep {
		return api.DataStoreStatusRep{
			Database: "redis",
			DBServer: fmt.Sprintf("redis://%s:6379", dbContainer.GetName()),
		}
	},
}

var redisWithACLDatabaseTestParams = databaseTestParams{
	dbImageName:    "redis",
	dbDockerParams: []string{"--requirepass", "secret"},
	setupFn: func(manager *integrationTestManager, container *docker.Container) error {
		redisCmd := "ACL SETUSER alice on +@all >secret"
		return container.CommandInContainer("/bin/sh", "-c", "redis-cli", redisCmd).Run()
	},
	hostnamePrefix: "redis",
	envVarsFn: func(dbContainer *docker.Container) map[string]string {
		return map[string]string{
			"USE_REDIS":      "true",
			"REDIS_HOST":     dbContainer.GetName(),
			"REDIS_PASSWORD": "secret",
			"REDIS_USER":     "alice",
		}
	},
	expectedStatusFn: func(dbContainer *docker.Container) api.DataStoreStatusRep {
		return api.DataStoreStatusRep{
			Database: "redis",
			DBServer: fmt.Sprintf("redis://%s:6379", dbContainer.GetName()),
		}
	},
}

// Redis with TLS and required client certificates. The certificates are generated per test run in a
// subdirectory of the Relay shared directory, so Relay sees them under relayContainerSharedDir and the
// Redis container sees them under /tls.
const (
	redisMTLSSubdir            = "redis-mtls"
	redisMTLSContainerTLSDir   = "/tls"
	redisMTLSRelayContainerDir = relayContainerSharedDir + "/" + redisMTLSSubdir
)

var redisMTLSDatabaseTestParams = databaseTestParams{
	dbImageName: "redis",
	dbDockerParams: []string{
		"--port", "0",
		"--tls-port", "6379",
		"--tls-cert-file", redisMTLSContainerTLSDir + "/server.pem",
		"--tls-key-file", redisMTLSContainerTLSDir + "/server.key",
		"--tls-ca-cert-file", redisMTLSContainerTLSDir + "/ca.pem",
		"--tls-auth-clients", "yes",
	},
	hostnamePrefix: "redis",
	mountFn: func(t *testing.T, m *integrationTestManager, hostname string) (string, string, error) {
		hostDir := filepath.Join(m.relaySharedDir, redisMTLSSubdir)
		if err := os.MkdirAll(hostDir, 0o755); err != nil {
			return "", "", err
		}
		// The server certificate must be valid for the container hostname, since Relay verifies it.
		sharedtest.NewMTLSFilesInDir(t, hostDir, []string{hostname}, nil)
		return hostDir, redisMTLSContainerTLSDir, nil
	},
	envVarsFn: func(dbContainer *docker.Container) map[string]string {
		return map[string]string{
			"USE_REDIS":              "true",
			"REDIS_HOST":             dbContainer.GetName(),
			"REDIS_TLS":              "true",
			"REDIS_CA_FILE":          redisMTLSRelayContainerDir + "/ca.pem",
			"REDIS_CLIENT_CERT_FILE": redisMTLSRelayContainerDir + "/client.pem",
			"REDIS_CLIENT_KEY_FILE":  redisMTLSRelayContainerDir + "/client.key",
		}
	},
	expectedStatusFn: func(dbContainer *docker.Container) api.DataStoreStatusRep {
		return api.DataStoreStatusRep{
			Database: "redis",
			DBServer: fmt.Sprintf("rediss://%s:6379", dbContainer.GetName()),
		}
	},
}

var consulDatabaseTestParams = databaseTestParams{
	dbImageName:    "hashicorp/consul",
	hostnamePrefix: "consul",
	envVarsFn: func(dbContainer *docker.Container) map[string]string {
		return map[string]string{
			"USE_CONSUL":  "true",
			"CONSUL_HOST": makeConsulAddress(dbContainer),
		}
	},
	expectedStatusFn: func(dbContainer *docker.Container) api.DataStoreStatusRep {
		return api.DataStoreStatusRep{
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
	expectedStatusFn: func(dbContainer *docker.Container) api.DataStoreStatusRep {
		return api.DataStoreStatusRep{
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
