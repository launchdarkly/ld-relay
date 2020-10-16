// +build integrationtests

package integrationtests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/integrationtests/docker"
	"github.com/launchdarkly/ld-relay/v6/integrationtests/oshelpers"
	"github.com/launchdarkly/ld-relay/v6/internal/core"

	ldapi "github.com/launchdarkly/api-client-go"
	ct "github.com/launchdarkly/go-configtypes"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"

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
	HTTPLogging     bool         `conf:"HTTP_LOGGING"`
}

type integrationTestManager struct {
	params             integrationTestParams
	baseURL            string
	streamURL          string
	httpClient         *http.Client
	apiClient          *ldapi.APIClient
	apiContext         context.Context
	apiBaseURL         string
	dockerImage        *docker.Image
	dockerContainer    *docker.Container
	dockerNetwork      *docker.Network
	relayBaseURL       string
	statusPollTimeout  time.Duration
	statusPollInterval time.Duration
	loggers            ldlog.Loggers
	requestLogger      *requestLogger
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

type projsAndEnvs map[projectInfo][]environmentInfo

func (pe projsAndEnvs) enumerateEnvs(fn func(projectInfo, environmentInfo)) {
	for proj, envs := range pe {
		for _, env := range envs {
			fn(proj, env)
		}
	}
}

func (pe projsAndEnvs) countEnvs() int {
	n := 0
	pe.enumerateEnvs(func(projectInfo, environmentInfo) { n++ })
	return n
}

func newIntegrationTestManager() (*integrationTestManager, error) {
	var params integrationTestParams

	var loggers ldlog.Loggers
	loggers.SetBaseLogger(log.New(os.Stdout, "[IntegrationTests] ", log.LstdFlags))

	reader := ct.NewVarReaderFromEnvironment()
	reader.ReadStruct(&params, false)
	if err := reader.Result().GetError(); err != nil {
		return nil, err
	}

	baseURL := params.LDAppBaseURL.GetOrElse(defaultAppBaseURL)
	streamURL := params.LDStreamBaseURL.GetOrElse(defaultStreamBaseURL)
	apiBaseURL := baseURL + "/api/v2"

	requestLogger := &requestLogger{transport: &http.Transport{}, enabled: params.HTTPLogging, loggers: loggers}
	requestLogger.loggers.SetPrefix("[HTTP]")
	httpClient := http.DefaultClient
	httpClient.Transport = requestLogger

	apiConfig := ldapi.NewConfiguration()
	apiConfig.BasePath = apiBaseURL
	apiConfig.HTTPClient = httpClient
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

	dockerImage, err := getRelayDockerImage(params.RelayTagOrSHA, loggers)
	if err != nil {
		return nil, err
	}

	return &integrationTestManager{
		params:             params,
		baseURL:            baseURL,
		streamURL:          streamURL,
		httpClient:         httpClient,
		apiClient:          apiClient,
		apiContext:         apiContext,
		apiBaseURL:         apiBaseURL,
		dockerImage:        dockerImage,
		dockerNetwork:      network,
		statusPollTimeout:  defaultStatusPollTimeout,
		statusPollInterval: defaultStatusPollInterval,
		loggers:            loggers,
		requestLogger:      requestLogger,
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
		return projectInfo{}, nil, m.logResult("Create project", err)
	}
	var envInfos []environmentInfo
	for _, env := range project.Environments {
		envInfos = append(envInfos, environmentInfo{
			id:        config.EnvironmentID(env.Id),
			key:       env.Key,
			name:      env.Name,
			sdkKey:    config.SDKKey(env.ApiKey),
			mobileKey: config.MobileKey(env.MobileKey),
		})
	}
	m.loggers.Infof("Created project %q\n", projKey)
	return projectInfo{key: projKey, name: projName}, envInfos, nil
}

func randomApiKey(prefix string) string {
	return (prefix + uuid.New())[0:20]
}

func (m *integrationTestManager) deleteProject(project projectInfo) error {
	_, err := m.apiClient.ProjectsApi.DeleteProject(m.apiContext, project.key)
	return m.logResult(fmt.Sprintf("Delete project %q", project.key), err)
}

func (m *integrationTestManager) logResult(desc string, err error) error {
	if err == nil {
		m.loggers.Infof("%s: OK", desc)
		return nil
	}
	addInfo := ""
	if gse, ok := err.(ldapi.GenericSwaggerError); ok {
		body := string(gse.Body())
		addInfo = " - " + string(body)
	}
	m.loggers.Errorf("%s: FAILED - %s%s", desc, err, addInfo)
	return err
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
		return environmentInfo{}, m.logResult("Create environment", err)
	}
	m.loggers.Infof("created environment %q\n", envKey)
	return environmentInfo{
		id:        config.EnvironmentID(env.Id),
		key:       env.Key,
		name:      env.Name,
		sdkKey:    config.SDKKey(env.ApiKey),
		mobileKey: config.MobileKey(env.MobileKey),
	}, nil
}

func (m *integrationTestManager) deleteEnvironment(project projectInfo, env environmentInfo) error {
	_, err := m.apiClient.EnvironmentsApi.DeleteEnvironment(m.apiContext, project.key, env.key)
	return m.logResult(fmt.Sprintf("Delete environment %q", env.key), err)
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
	return newKey, m.logResult(fmt.Sprintf("Change SDK key for environment %q", env.key), err)
}

func (m *integrationTestManager) rotateMobileKey(project projectInfo, env environmentInfo) (config.MobileKey, error) {
	envResult, _, err := m.apiClient.EnvironmentsApi.ResetEnvironmentMobileKey(m.apiContext, project.key, env.key, nil)
	var newKey config.MobileKey
	if err == nil {
		newKey = config.MobileKey(envResult.MobileKey)
	}
	return newKey, m.logResult(fmt.Sprintf("Change mobile key for environment %q", env.key), err)
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
	return autoConfigID(entity.Id), config.AutoConfigKey(entity.FullKey), m.logResult("Create auto-config key", err)
}

func (m *integrationTestManager) updateAutoConfigPolicy(id autoConfigID, newPolicyResources []string) error {
	var patchValue interface{} = newPolicyResources
	patchOps := []ldapi.PatchOperation{
		{Op: "replace", Path: "/policy/0/resources", Value: &patchValue},
	}
	_, _, err := m.apiClient.RelayProxyConfigurationsApi.PatchRelayProxyConfig(m.apiContext, string(id), patchOps)
	return m.logResult("Update auto-config policy", err)
}

func (m *integrationTestManager) deleteAutoConfigKey(id autoConfigID) error {
	_, err := m.apiClient.RelayProxyConfigurationsApi.DeleteRelayProxyConfig(m.apiContext, string(id))
	return m.logResult("Delete auto-config key", err)
}

func (m *integrationTestManager) createFlag(
	proj projectInfo,
	envs []environmentInfo,
	flagKey string,
	valueForEnv func(environmentInfo) ldvalue.Value,
) error {

	flagPost := ldapi.FeatureFlagBody{
		Name: flagKey,
		Key:  flagKey,
	}
	for _, env := range envs {
		valueAsInterface := valueForEnv(env).AsArbitraryValue()
		flagPost.Variations = append(flagPost.Variations, ldapi.Variation{Value: &valueAsInterface})
	}

	_, _, err := m.apiClient.FeatureFlagsApi.PostFeatureFlag(m.apiContext, proj.key, flagPost, nil)
	err = m.logResult("Create flag "+flagKey+" in "+proj.key, err)
	if err != nil {
		return err
	}

	for i, env := range envs {
		var varIndex interface{} = i
		envPrefix := fmt.Sprintf("/environments/%s", env.key)
		patches := []ldapi.PatchOperation{
			{Op: "replace", Path: envPrefix + "/offVariation", Value: &varIndex},
		}
		_, _, err = m.apiClient.FeatureFlagsApi.PatchFeatureFlag(m.apiContext, proj.key, flagKey,
			ldapi.PatchComment{Patch: patches})
		err = m.logResult("Configure flag "+flagKey+" for "+env.key, err)
		if err != nil {
			return err
		}
	}

	return nil
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

	go container.FollowLogs(oshelpers.NewLineParsingWriter(func(line string) {
		fmt.Println("[Relay] " + line)
	}))

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

func (m *integrationTestManager) makeHTTPRequestToRelay(request *http.Request) (*http.Response, error) {
	// Here we're using a somewhat roundabout way to hit a Relay endpoint: we execute curl inside of
	// the Relay container. We can't just use Docker port mapping (like, run it with -p 9999:8030 and
	// then make an HTTP request to http://localhost:9999) because in CircleCI the container belongs
	// to a special Docker host whose network isn't accessible in that way.
	m.requestLogger.logRequest(request)
	curlArgs := []string{"curl", "-i", "--silent"}
	for k, vv := range request.Header {
		for _, v := range vv {
			curlArgs = append(curlArgs, "-H")
			curlArgs = append(curlArgs, fmt.Sprintf("%s:%s", k, v))
		}
	}
	curlArgs = append(curlArgs, request.URL.String())
	output, err := m.dockerContainer.CommandInContainer(curlArgs...).ShowOutput(false).RunAndGetOutput()
	if err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader([]byte(output))), request)
	m.requestLogger.logResponse(resp)
	return resp, err
}

func (m *integrationTestManager) awaitRelayStatus(t *testing.T, fn func(core.StatusRep) bool) (core.StatusRep, bool) {
	require.NotNil(t, m.dockerContainer, "Relay was not started")
	var lastOutput, lastError string
	var lastStatus core.StatusRep
	success := assert.Eventually(t, func() bool {
		request, _ := http.NewRequest("GET", fmt.Sprintf("%s/status", m.relayBaseURL), nil)
		resp, err := m.makeHTTPRequestToRelay(request)
		if err != nil {
			if lastError != err.Error() {
				fmt.Printf("error querying status resource: %s\n", err.Error())
				lastError = err.Error()
			}
			return false
		}
		outData, err := ioutil.ReadAll(resp.Body)
		output := string(outData)
		if output != lastOutput {
			if !m.params.HTTPLogging {
				m.loggers.Infof("Got status: %s", output)
			}
			lastOutput = output
		}
		var status core.StatusRep
		require.NoError(t, json.Unmarshal([]byte(output), &status))
		lastStatus = status
		return fn(status)
	}, m.statusPollTimeout, m.statusPollInterval, "did not see expected status data from Relay")
	return lastStatus, success
}

func (m *integrationTestManager) awaitEnvironments(t *testing.T, projsAndEnvs projsAndEnvs, isAutoConfig bool) {
	_, success := m.awaitRelayStatus(t, func(status core.StatusRep) bool {
		if len(status.Environments) != projsAndEnvs.countEnvs() {
			return false
		}
		ok := true
		projsAndEnvs.enumerateEnvs(func(proj projectInfo, env environmentInfo) {
			mapKey := string(env.key)
			if isAutoConfig {
				mapKey = string(env.id)
			}
			if envStatus, found := status.Environments[mapKey]; found {
				verifyEnvProperties(t, proj, env, envStatus, isAutoConfig)
				if envStatus.Status != "connected" {
					ok = false
				}
			} else {
				ok = false
			}
		})
		return ok
	})
	if !success {
		t.FailNow()
	}
}

func (m *integrationTestManager) verifyFlagValues(t *testing.T, projsAndEnvs projsAndEnvs) {
	userBase64 := "eyJrZXkiOiJmb28ifQ" // properties don't matter, just has to be a valid base64 user object

	projsAndEnvs.enumerateEnvs(func(proj projectInfo, env environmentInfo) {
		req, err := http.NewRequest("GET", m.relayBaseURL+"/msdk/evalx/users/"+userBase64, nil)
		require.NoError(t, err)
		req.Header.Add("Authorization", string(env.mobileKey))

		resp, err := m.makeHTTPRequestToRelay(req)
		require.NoError(t, err)
		if assert.Equal(t, 200, resp.StatusCode, "requested flags for environment "+env.key) {
			defer resp.Body.Close()
			data, err := ioutil.ReadAll(resp.Body)
			require.NoError(t, err)

			respJSON := ldvalue.Parse(data)
			expectedValue := flagValueForEnv(env)
			if expectedValue.Equal(respJSON.GetByKey(flagKeyForProj(proj)).GetByKey("value")) {
				m.loggers.Infof("Got expected flag values for enviroment %s", env.key)
			} else {
				m.loggers.Errorf("Did not get expected flag values for enviroment %s", env.key)
				m.loggers.Errorf("Response was: %s", respJSON)
				t.Fail()
			}
		}
	})
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
	containerLogger := log.New(os.Stdout, fmt.Sprintf("[%s] ", hostnamePrefix), log.LstdFlags)
	go container.FollowLogs(oshelpers.NewLineParsingWriter(func(line string) { containerLogger.Println(line) }))
	defer func() {
		container.Stop()
		container.Delete()
	}()
	action(container)
}

func verifyEnvProperties(t *testing.T, project projectInfo, environment environmentInfo, envStatus core.EnvironmentStatusRep, isAutoConfig bool) {
	assert.Equal(t, string(environment.id), envStatus.EnvID)
	if isAutoConfig {
		assert.Equal(t, environment.name, envStatus.EnvName)
		assert.Equal(t, environment.key, envStatus.EnvKey)
		assert.Equal(t, project.name, envStatus.ProjName)
		assert.Equal(t, project.key, envStatus.ProjKey)
	}
}

func flagKeyForProj(proj projectInfo) string {
	return "flag-for-" + proj.key
}

func flagValueForEnv(env environmentInfo) ldvalue.Value {
	return ldvalue.String("value-for-" + env.key)
}
