// +build integrationtests

package integrationtests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/integrationtests/docker"
	"github.com/launchdarkly/ld-relay/v6/internal/core"

	ldapi "github.com/launchdarkly/api-client-go"
	ct "github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"

	"github.com/antihax/optional"
	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	defaultAppBaseURL         = "https://ld-stg.launchdarkly.com"
	defaultStreamBaseURL      = "https://stream-stg.launchdarkly.com"
	defaultStatusPollTimeout  = time.Second * 5
	defaultStatusPollInterval = time.Millisecond * 100
)

type autoConfigID string

type integrationTestParams struct {
	LDAppBaseURL    ct.OptString `conf:"LD_BASE_URL`
	LDStreamBaseURL ct.OptString `conf:"LD_STREAM_URL`
	APIToken        string       `conf:"LD_API_TOKEN,required"`
	RelayTagOrSHA   string       `conf:"RELAY_TAG_OR_SHA"`
}

type integrationTestManager struct {
	params             integrationTestParams
	baseURL            string
	streamURL          string
	apiClient          *ldapi.APIClient
	apiContext         context.Context
	apiBaseURL         string
	dockerImage        *docker.Image
	dockerContainer    *docker.Container
	dockerNetwork      *docker.Network
	relayBaseURL       string
	statusPollTimeout  time.Duration
	statusPollInterval time.Duration
}

type projectInfo struct {
	key  string
	name string
}

type environmentInfo struct {
	id        config.EnvironmentID
	key       string
	name      string
	sdkKey    config.SDKKey
	mobileKey config.MobileKey
}

func newIntegrationTestManager() (*integrationTestManager, error) {
	var params integrationTestParams

	reader := ct.NewVarReaderFromEnvironment()
	reader.ReadStruct(&params, false)
	if err := reader.Result().GetError(); err != nil {
		return nil, err
	}

	baseURL := params.LDAppBaseURL.GetOrElse(defaultAppBaseURL)
	streamURL := params.LDStreamBaseURL.GetOrElse(defaultStreamBaseURL)
	apiBaseURL := baseURL + "/api/v2"

	apiConfig := ldapi.NewConfiguration()
	apiConfig.BasePath = apiBaseURL
	apiConfig.HTTPClient = http.DefaultClient
	apiConfig.UserAgent = "ld-relay-integration-tests"
	apiConfig.AddDefaultHeader("LD-API-Version", "beta")
	apiClient := ldapi.NewAPIClient(apiConfig)
	apiContext := context.WithValue(context.Background(), ldapi.ContextAPIKey, ldapi.APIKey{
		Key: params.APIToken,
	})

	network, err := docker.NewNetwork()
	if err != nil {
		return nil, err
	}

	dockerImage, err := getRelayDockerImage(params.RelayTagOrSHA)
	if err != nil {
		return nil, err
	}

	return &integrationTestManager{
		params:             params,
		baseURL:            baseURL,
		streamURL:          streamURL,
		apiClient:          apiClient,
		apiContext:         apiContext,
		apiBaseURL:         apiBaseURL,
		dockerImage:        dockerImage,
		dockerNetwork:      network,
		statusPollTimeout:  defaultStatusPollTimeout,
		statusPollInterval: defaultStatusPollInterval,
	}, nil
}

func (m *integrationTestManager) close() {
	m.stopRelay()
	if m.dockerImage.IsCustomBuild() {
		_ = m.dockerImage.Delete()
	}
	_ = m.dockerNetwork.Delete()
}

func (m *integrationTestManager) createProject(numEnvironments int) (projectInfo, []environmentInfo, error) {
	projKey := randomApiKey("relayi9n-")
	projName := projKey
	projectBody := ldapi.ProjectBody{
		Name: projName,
		Key:  projKey,
	}
	for i := 0; i < numEnvironments; i++ {
		envKey := randomApiKey("env-")
		envName := envKey
		projectBody.Environments = append(projectBody.Environments, ldapi.EnvironmentPost{
			Name:  envName,
			Key:   envKey,
			Color: "000000",
		})
	}
	project, _, err := m.apiClient.ProjectsApi.PostProject(m.apiContext, projectBody)
	if err != nil {
		return projectInfo{}, nil, apiClientResult("creating project", err)
	}
	var envInfos []environmentInfo
	for _, env := range project.Environments {
		envInfos = append(envInfos, environmentInfo{
			id:     config.EnvironmentID(env.Id),
			key:    env.Key,
			name:   env.Name,
			sdkKey: config.SDKKey(env.ApiKey),
		})
	}
	fmt.Printf("created project %q\n", projKey)
	return projectInfo{key: projKey, name: projName}, envInfos, nil
}

func randomApiKey(prefix string) string {
	return (prefix + uuid.New())[0:20]
}

func (m *integrationTestManager) deleteProject(project projectInfo) error {
	_, err := m.apiClient.ProjectsApi.DeleteProject(m.apiContext, project.key)
	return apiClientResult(fmt.Sprintf("deleting project %q", project.key), err)
}

func apiClientResult(desc string, err error) error {
	if err == nil {
		fmt.Printf("%s: success\n", desc)
		return nil
	}
	addInfo := ""
	if gse, ok := err.(ldapi.GenericSwaggerError); ok {
		body := string(gse.Body())
		addInfo = " - " + string(body)
	}
	return fmt.Errorf("error in %s: %w%s", desc, err, addInfo)
}

func (m *integrationTestManager) addEnvironment(project projectInfo) (environmentInfo, error) {
	envKey := randomApiKey("env-")
	envName := envKey
	envBody := ldapi.EnvironmentPost{
		Key:   envKey,
		Name:  envName,
		Color: "000000",
	}
	env, _, err := m.apiClient.EnvironmentsApi.PostEnvironment(m.apiContext, project.key, envBody)
	if err != nil {
		return environmentInfo{}, apiClientResult("creating environment", err)
	}
	fmt.Printf("created environment %q\n", envKey)
	return environmentInfo{
		id:     config.EnvironmentID(env.Id),
		key:    env.Key,
		name:   env.Name,
		sdkKey: config.SDKKey(env.ApiKey),
	}, nil
}

func (m *integrationTestManager) deleteEnvironment(project projectInfo, env environmentInfo) error {
	_, err := m.apiClient.EnvironmentsApi.DeleteEnvironment(m.apiContext, project.key, env.key)
	return apiClientResult(fmt.Sprintf("deleting environment %q", env.key), err)
}

func (m *integrationTestManager) rotateSDKKey(project projectInfo, env environmentInfo, expirationTime time.Time) (
	config.SDKKey, error) {
	var apiOptions *ldapi.ResetEnvironmentSDKKeyOpts
	if !expirationTime.IsZero() {
		apiOptions = &ldapi.ResetEnvironmentSDKKeyOpts{Expiry: optional.NewInt64(int64(ldtime.UnixMillisFromTime(expirationTime)))}
	}
	envResult, _, err := m.apiClient.EnvironmentsApi.ResetEnvironmentSDKKey(m.apiContext, project.key, env.key, apiOptions)
	var newKey config.SDKKey
	if err == nil {
		newKey = config.SDKKey(envResult.ApiKey)
	}
	return newKey, apiClientResult(fmt.Sprintf("changing SDK key for environment %q", env.key), err)
}

func (m *integrationTestManager) rotateMobileKey(project projectInfo, env environmentInfo) (config.MobileKey, error) {
	envResult, _, err := m.apiClient.EnvironmentsApi.ResetEnvironmentMobileKey(m.apiContext, project.key, env.key, nil)
	var newKey config.MobileKey
	if err == nil {
		newKey = config.MobileKey(envResult.MobileKey)
	}
	return newKey, apiClientResult(fmt.Sprintf("changing mobile key for environment %q", env.key), err)
}

func (m *integrationTestManager) createAutoConfigKey(policyResources []string) (autoConfigID, config.AutoConfigKey, error) {
	body := ldapi.RelayProxyConfigBody{
		Name: uuid.New(),
		Policy: []ldapi.Policy{
			{
				Resources: policyResources,
				Actions:   []string{"*"},
				Effect:    "allow",
			},
		},
	}
	entity, _, err := m.apiClient.RelayProxyConfigurationsApi.PostRelayAutoConfig(m.apiContext, body)
	return autoConfigID(entity.Id), config.AutoConfigKey(entity.FullKey), apiClientResult("creating auto-config key", err)
}

func (m *integrationTestManager) updateAutoConfigPolicy(id autoConfigID, newPolicyResources []string) error {
	var patchValue interface{} = newPolicyResources
	patchOps := []ldapi.PatchOperation{
		{Op: "replace", Path: "/policy/0/resources", Value: &patchValue},
	}
	_, _, err := m.apiClient.RelayProxyConfigurationsApi.PatchRelayProxyConfig(m.apiContext, string(id), patchOps)
	return apiClientResult("updating auto-config policy", err)
}

func (m *integrationTestManager) deleteAutoConfigKey(id autoConfigID) error {
	_, err := m.apiClient.RelayProxyConfigurationsApi.DeleteRelayProxyConfig(m.apiContext, string(id))
	return apiClientResult("deleting auto-config key", err)
}

func (m *integrationTestManager) startRelay(t *testing.T, envVars map[string]string) error {
	require.Nil(t, m.dockerContainer, "called startRelay when Relay was already running")

	cb := m.dockerImage.NewContainerBuilder().
		Name("relay-"+uuid.New()).
		Network(m.dockerNetwork).
		EnvVar("BASE_URI", m.baseURL).
		EnvVar("STREAM_URI", m.streamURL).
		EnvVar("DISABLE_INTERNAL_USAGE_METRICS", "true")
	for k, v := range envVars {
		cb.EnvVar(k, v)
	}

	container, err := cb.Build()
	if err != nil {
		return err
	}
	m.dockerContainer = container
	if err := container.Start(); err != nil {
		return err
	}

	go container.FollowLogs()

	m.relayBaseURL = fmt.Sprintf("http://localhost:%d", config.DefaultPort)
	return nil
}

func (m *integrationTestManager) stopRelay() error {
	if m.dockerContainer != nil {
		if err := m.dockerContainer.Stop(); err != nil {
			return err
		}
		if err := m.dockerContainer.Delete(); err != nil {
			return err
		}
		m.dockerContainer = nil
	}
	return nil
}

func (m *integrationTestManager) awaitRelayStatus(t *testing.T, fn func(core.StatusRep) bool) (core.StatusRep, bool) {
	require.NotNil(t, m.dockerContainer, "Relay was not started")
	var lastOutput, lastError string
	var lastStatus core.StatusRep
	success := assert.Eventually(t, func() bool {
		// Here we're using a somewhat roundabout way to hit the status endpoint: we execute curl inside of
		// the Relay container. We can't just use Docker port mapping (like, run it with -p 9999:8030 and
		// then make an HTTP request to http://localhost:9999/status) because in CircleCI the container
		// belongs to a special Docker host whose network isn't accessible in that way.
		output, err := m.dockerContainer.CommandInContainer("curl", "--silent",
			fmt.Sprintf("%s/status", m.relayBaseURL)).ShowOutput(false).RunAndGetOutput()
		if err != nil {
			if lastError != err.Error() {
				fmt.Printf("error querying status resource: %s\n", err.Error())
				lastError = err.Error()
			}
			return false
		}
		if output != lastOutput {
			fmt.Println("got status:", output)
			lastOutput = output
		}
		var status core.StatusRep
		require.NoError(t, json.Unmarshal([]byte(output), &status))
		lastStatus = status
		return fn(status)
	}, m.statusPollTimeout, m.statusPollInterval, "did not see expected status data from Relay")
	return lastStatus, success
}

func (m *integrationTestManager) withExtraContainer(
	t *testing.T,
	imageName, hostnamePrefix string,
	action func(*docker.Container),
) {
	image, err := docker.PullImage(imageName)
	require.NoError(t, err)
	hostname := hostnamePrefix + "-" + uuid.New()
	container, err := image.NewContainerBuilder().Name(hostname).Network(m.dockerNetwork).Build()
	require.NoError(t, err)
	container.Start()
	go container.FollowLogs()
	defer func() {
		container.Stop()
		container.Delete()
	}()
	action(container)
}
