//go:build integrationtests

package integrationtests

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/internal/credential"

	"github.com/launchdarkly/ld-relay/v8/config"
	"github.com/launchdarkly/ld-relay/v8/integrationtests/docker"
	"github.com/launchdarkly/ld-relay/v8/integrationtests/oshelpers"
	"github.com/launchdarkly/ld-relay/v8/internal/api"

	ldapi "github.com/launchdarkly/api-client-go/v13"
	ct "github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"

	"github.com/pborman/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	defaultAPIBaseURL       = "https://ld-stg.launchdarkly.com"
	defaultStreamBaseURL    = "https://stream-stg.launchdarkly.com"
	defaultSDKBaseURL       = "https://sdk-stg.launchdarkly.com"
	defaultClientSDKBaseURL = "https://clientsdk-stg.launchdarkly.com"
	// 10 seconds because the previous value of 5 resulted in flaky autoconfig tests.
	defaultStatusPollTimeout = time.Second * 10
	// 1 second because the previous value of 100ms seemed unnecessarily aggressive.
	defaultStatusPollInterval = 1 * time.Second
	relayContainerSharedDir   = "/tmp/relay-shared"
)

type autoConfigID string

type integrationTestParams struct {
	LDAPIBaseURL    ct.OptString `conf:"LD_API_URL"`
	LDStreamBaseURL ct.OptString `conf:"LD_STREAM_URL"`
	LDSDKBaseURL    ct.OptString `conf:"LD_SDK_URL"`
	LDClientSDKURL  ct.OptString `conf:"LD_CLIENT_SDK_URL"`
	APIToken        string       `conf:"LD_API_TOKEN,required"`
	RelayTagOrSHA   string       `conf:"RELAY_TAG_OR_SHA"`
	HTTPLogging     bool         `conf:"HTTP_LOGGING"`
}

// integrationTestManager is the base logic for all of the integration tests. It's responsible for starting Relay
// in a Docker container; starting any other Docker containers we use in a test; making API requests to LaunchDarkly
// to create projects, auto-config keys, etc.; making HTTP requests to Relay; and doing some standard kinds of test
// assertions like querying Relay's status until it matches some expected condition.
type integrationTestManager struct {
	params             integrationTestParams
	apiURL             string
	streamURL          string
	sdkURL             string
	clientSDKURL       string
	httpClient         *http.Client
	apiHelper          *apiHelper
	dockerImage        *docker.Image
	dockerContainer    *docker.Container
	dockerNetwork      *docker.Network
	relayBaseURL       string
	relaySharedDir     string
	statusPollTimeout  time.Duration
	statusPollInterval time.Duration
	loggers            ldlog.Loggers
	requestLogger      *requestLogger
	relayLog           []string
	relayLogLock       sync.Mutex
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

	apiBaseURL := params.LDAPIBaseURL.GetOrElse(defaultAPIBaseURL)
	streamURL := params.LDStreamBaseURL.GetOrElse(defaultStreamBaseURL)
	sdkURL := params.LDSDKBaseURL.GetOrElse(defaultSDKBaseURL)
	clientSDKURL := params.LDClientSDKURL.GetOrElse(defaultClientSDKBaseURL)

	requestLogger := &requestLogger{transport: &http.Transport{}, enabled: params.HTTPLogging, loggers: loggers}
	requestLogger.loggers.SetPrefix("[HTTP]")

	hc := *http.DefaultClient
	httpClient := &hc
	httpClient.Transport = requestLogger

	apiConfig := ldapi.NewConfiguration()
	apiConfig.Servers = []ldapi.ServerConfiguration{
		{
			URL:         apiBaseURL,
			Description: "StagingOrProd",
		},
	}
	apiConfig.HTTPClient = httpClient
	apiConfig.UserAgent = "ld-relay-integration-tests"
	apiConfig.AddDefaultHeader("LD-API-Version", "beta")

	// This is here because some API calls - which don't have bodies - do not include this header.
	// The calls fail with "415 Unsupported Media Type" because the backend appears to unconditionally require the header.
	apiConfig.AddDefaultHeader("Content-Type", "application/json")

	apiClient := ldapi.NewAPIClient(apiConfig)
	apiContext := context.WithValue(context.Background(), ldapi.ContextAPIKeys, map[string]ldapi.APIKey{
		"ApiKey": {
			Key: params.APIToken,
		},
	})

	network, err := docker.NewNetwork()
	if err != nil {
		return nil, err
	}

	dockerImage, err := getRelayDockerImage(params.RelayTagOrSHA, loggers)
	if err != nil {
		return nil, err
	}

	relaySharedDir, err := os.MkdirTemp("", "relay-i9ntest-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(relaySharedDir, 0755); err != nil {
		return nil, err
	}

	apiHelper := &apiHelper{
		apiClient:  apiClient,
		apiContext: apiContext,
		apiBaseURL: apiBaseURL,
		loggers:    loggers,
	}

	return &integrationTestManager{
		params:             params,
		apiURL:             apiBaseURL,
		streamURL:          streamURL,
		sdkURL:             sdkURL,
		clientSDKURL:       clientSDKURL,
		httpClient:         httpClient,
		apiHelper:          apiHelper,
		dockerImage:        dockerImage,
		dockerNetwork:      network,
		relaySharedDir:     relaySharedDir,
		statusPollTimeout:  defaultStatusPollTimeout,
		statusPollInterval: defaultStatusPollInterval,
		loggers:            loggers,
		requestLogger:      requestLogger,
	}, nil
}

func (m *integrationTestManager) close(t *testing.T) {
	m.stopRelay(t)
	if m.dockerImage.IsCustomBuild() {
		_ = m.dockerImage.Delete()
	}
	_ = m.dockerNetwork.Delete()
	_ = os.RemoveAll(m.relaySharedDir)
}

func (m *integrationTestManager) startRelay(t *testing.T, envVars map[string]string) error {
	require.Nil(t, m.dockerContainer, "called startRelay when Relay was already running")

	cb := m.dockerImage.NewContainerBuilder().
		Name("relay-"+uuid.New()).
		Network(m.dockerNetwork).
		PublishPort(config.DefaultPort, config.DefaultPort).
		SharedVolume(m.relaySharedDir, relayContainerSharedDir).
		EnvVar("LOG_LEVEL", "debug")
	// Set the Relay config variables for base URIs only if we're *not* using the production defaults.
	// This verifies that Relay's own default behavior is correct.
	if m.streamURL != "https://stream.launchdarkly.com" {
		cb.EnvVar("STREAM_URI", m.streamURL)
	}
	if m.sdkURL != "https://sdk.launchdarkly.com" {
		cb.EnvVar("BASE_URI", m.sdkURL)
	}
	if m.clientSDKURL != "https://clientsdk.launchdarkly.com" {
		cb.EnvVar("CLIENT_SIDE_BASE_URI", m.clientSDKURL)
	}

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
		// just write directly to stdout here, because Relay already adds its own log timestamps -
		// but suppress debug output because we'll dump it later if the test failed
		if !strings.Contains(line, " DEBUG: ") {
			fmt.Println("[Relay] ", line)
		}
		m.relayLogLock.Lock()
		m.relayLog = append(m.relayLog, line)
		m.relayLogLock.Unlock()
	}))

	m.relayBaseURL = fmt.Sprintf("http://localhost:%d", config.DefaultPort)
	return nil
}

func (m *integrationTestManager) stopRelay(t *testing.T) error {
	if m.dockerContainer != nil {
		if t.Failed() {
			logs := m.getRelayLog()
			if len(logs) > 0 {
				fmt.Println("===")
				fmt.Println("Dumping full Relay log, including debug output, because the test failed:")
				for _, line := range logs {
					fmt.Println("[Relay] ", line)
				}
				fmt.Println("===")
			}
		}
		if err := m.dockerContainer.Stop(); err != nil {
			return err
		}
		if err := m.dockerContainer.Delete(); err != nil {
			return err
		}
		m.dockerContainer = nil
		m.relayLogLock.Lock()
		m.relayLog = nil
		m.relayLogLock.Unlock()
	}
	return nil
}

func (m *integrationTestManager) getRelayLog() []string {
	m.relayLogLock.Lock()
	ret := append([]string(nil), m.relayLog...)
	m.relayLogLock.Unlock()
	return ret
}

func (m *integrationTestManager) makeHTTPRequestToRelay(request *http.Request) (*http.Response, error) {
	// This method provides logging of the request and response, and allows us to add any other special
	// logic we might need for connecting to the Relay port.
	m.requestLogger.logRequest(request)
	resp, err := http.DefaultClient.Do(request)
	if err == nil {
		m.requestLogger.logResponse(resp, true)
	}
	return resp, err
}

func (m *integrationTestManager) awaitRelayStatus(t *testing.T, fn func(api.StatusRep) bool) (api.StatusRep, bool) {
	require.NotNil(t, m.dockerContainer, "Relay was not started")
	var lastOutput, lastError string
	var lastStatus api.StatusRep
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
		outData, err := io.ReadAll(resp.Body)
		output := string(outData)
		if output != lastOutput {
			if !m.params.HTTPLogging {
				m.loggers.Infof("Got status: %s", output)
			}
			lastOutput = output
		}
		var status api.StatusRep
		require.NoError(t, json.Unmarshal([]byte(output), &status))
		lastStatus = status
		return fn(status)
	}, m.statusPollTimeout, m.statusPollInterval, "did not see expected status data from Relay")
	return lastStatus, success
}

func (m *integrationTestManager) awaitEnvironments(t *testing.T, projsAndEnvs projsAndEnvs, expectations *envPropertyExpectations, envMapKeyFn func(proj projectInfo, env environmentInfo) string) {
	_, success := m.awaitRelayStatus(t, func(status api.StatusRep) bool {
		if len(status.Environments) != projsAndEnvs.countEnvs() {
			return false
		}
		ok := true
		projsAndEnvs.enumerateEnvs(func(proj projectInfo, env environmentInfo) {
			mapKey := envMapKeyFn(proj, env)
			if envStatus, found := status.Environments[mapKey]; found {
				verifyEnvProperties(t, proj, env, envStatus, expectations)
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

func (m *integrationTestManager) rotateSDKKeys(t *testing.T, existing projsAndEnvs, expiry time.Time) projsAndEnvs {
	updated := make(projsAndEnvs)
	for proj, envs := range existing {
		updated[proj] = make([]environmentInfo, 0)
		for _, env := range envs {
			newKey, err := m.apiHelper.rotateSDKKey(proj, env, expiry)
			require.NoError(t, err, "failed to rotate SDK key for environment %s", env.id)
			if expiry.IsZero() {
				env.expiringSdkKey = ""
			} else {
				env.expiringSdkKey = env.sdkKey
			}
			env.sdkKey = newKey
			updated[proj] = append(updated[proj], env)
		}
	}
	return updated
}

// verifyFlagValues hits Relay's polling evaluation endpoint and verifies that it returns the expected
// flags and values, based on the standard way we create flags for our test environments in createFlag.
func (m *integrationTestManager) verifyFlagValues(t *testing.T, projsAndEnvs projsAndEnvs) {
	userJSON := `{"key":"any-user-key"}`

	projsAndEnvs.enumerateEnvs(func(proj projectInfo, env environmentInfo) {
		valuesObject := m.getFlagValues(t, proj, env, userJSON)
		expectedValue := flagValueForEnv(env)
		if expectedValue.Equal(valuesObject.GetByKey(flagKeyForProj(proj))) {
			m.loggers.Infof("Got expected flag values for environment %s with SDK key %s", env.key, env.sdkKey)
		} else {
			m.loggers.Errorf("Did not get expected flag values for environment %s with SDK key %s", env.key, env.sdkKey)
			m.loggers.Errorf("Response was: %s", valuesObject)
			t.Fail()
		}
	})
}

func (m *integrationTestManager) verifyEvenOddFlagKeys(t *testing.T, projsAndEnvs projsAndEnvs) {
	userJSON := `{"key":"any-user-key"}`

	projsAndEnvs.enumerateEnvs(func(proj projectInfo, env environmentInfo) {
		switch env.filterKey {
		case config.DefaultFilter:
			// Since this is an unfiltered environment, both even and odd flags should return values.

			valuesObject := m.getFlagValues(t, proj, env, userJSON)
			expectedValue := flagValueForEnv(env)
			if expectedValue.Equal(valuesObject.GetByKey(evenFlagKeyForProj(proj))) &&
				expectedValue.Equal(valuesObject.GetByKey(oddFlagKeyForProj(proj))) {
				m.loggers.Infof("Got expected flag values for environment %s (no filter) with SDK key %s", env.key, env.sdkKey)
			} else {
				m.loggers.Errorf("Did not get expected flag values for environment %s (no filter) with SDK key %s", env.key, env.sdkKey)
				m.loggers.Errorf("Response was: %s", valuesObject)
				t.Fail()
			}
		case "even-flags":
			// Since this is filtered by "even-flags", only the even flag key should return a value;
			// odd should be null.
			valuesObject := m.getFlagValues(t, proj, env, userJSON)
			expectedValue := flagValueForEnv(env)
			if expectedValue.Equal(valuesObject.GetByKey(evenFlagKeyForProj(proj))) && valuesObject.GetByKey(oddFlagKeyForProj(proj)).IsNull() {
				m.loggers.Infof("Got expected flag values for environment %s (%s) with SDK key %s", env.key, env.filterKey, env.sdkKey)
			} else {
				m.loggers.Errorf("Did not get expected flag values for environment %s (%s) with SDK key %s", env.key, env.filterKey, env.sdkKey)
				m.loggers.Errorf("Response was: %s", valuesObject)
				t.Fail()
			}
		case "odd-flags":
			// Likewise since this is filtered by "odd-flags", only the odd flag key should return a value;
			// even should be null.
			valuesObject := m.getFlagValues(t, proj, env, userJSON)
			expectedValue := flagValueForEnv(env)
			if expectedValue.Equal(valuesObject.GetByKey(oddFlagKeyForProj(proj))) && valuesObject.GetByKey(evenFlagKeyForProj(proj)).IsNull() {
				m.loggers.Infof("Got expected flag values for environment %s (%s) with SDK key %s", env.key, env.filterKey, env.sdkKey)
			} else {
				m.loggers.Errorf("Did not get expected flag values for environment %s (%s) with SDK key %s", env.key, env.filterKey, env.sdkKey)
				m.loggers.Errorf("Response was: %s", valuesObject)
				t.Fail()
			}
		}
	})
}

func (m *integrationTestManager) getFlagValues(t *testing.T, proj projectInfo, env environmentInfo, userJSON string) ldvalue.Value {
	userBase64 := base64.URLEncoding.EncodeToString([]byte(userJSON))

	u, err := url.Parse(m.relayBaseURL + "/sdk/evalx/users/" + userBase64)
	if err != nil {
		t.Fatalf("couldn't parse flag evaluation URL: %v", err)
	}

	if env.filterKey != config.DefaultFilter {
		u.RawQuery = url.Values{
			"filter": []string{string(env.filterKey)},
		}.Encode()
	}

	req, err := http.NewRequest("GET", u.String(), nil)
	require.NoError(t, err)
	req.Header.Add("Authorization", string(env.sdkKey))
	resp, err := m.makeHTTPRequestToRelay(req)
	require.NoError(t, err)
	if assert.Equal(t, 200, resp.StatusCode, "requested flags for environment "+env.key) {
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		flagData := ldvalue.Parse(data)
		if !flagData.Equal(ldvalue.Null()) {
			valuesObject := ldvalue.ObjectBuild()
			for _, key := range flagData.Keys(nil) {
				valuesObject.Set(key, flagData.GetByKey(key).GetByKey("value"))
			}
			return valuesObject.Build()
		}
		m.loggers.Errorf("Flags poll request returned invalid response for environment %s with SDK key %s: %s",
			env.key, env.sdkKey, string(data))
		t.FailNow()
	} else {
		m.loggers.Errorf("Flags poll request for environment %s with SDK key %s failed with status %d",
			env.key, env.sdkKey, resp.StatusCode)
		t.FailNow()
	}
	return ldvalue.Null()
}

// getFlagPrerequisites fetches a payload from the given URL, which is expected to be a Relay polling evaluation
// endpoint, and returns the "prerequisites" field of the flags in the payload.
func (m *integrationTestManager) getFlagPrerequisites(t *testing.T, envKey string,
	url *url.URL, auth credential.SDKCredential) ldvalue.Value {
	req, err := http.NewRequest("GET", url.String(), nil)
	require.NoError(t, err)
	req.Header.Add("Authorization", auth.GetAuthorizationHeaderValue())
	resp, err := m.makeHTTPRequestToRelay(req)
	require.NoError(t, err)
	if assert.Equal(t, 200, resp.StatusCode, "requested flags for environment %s with credential %s", envKey, auth.Masked()) {
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		flagData := ldvalue.Parse(data)
		if !flagData.Equal(ldvalue.Null()) {
			valuesObject := ldvalue.ObjectBuild()
			for _, key := range flagData.Keys(nil) {
				valuesObject.Set(key, flagData.GetByKey(key).GetByKey("prerequisites"))
			}
			return valuesObject.Build()
		}
		m.loggers.Errorf("Flags poll request returned invalid response for environment %s with credential %s: %s",
			envKey, auth.Masked(), string(data))
		t.FailNow()
	} else {
		m.loggers.Errorf("Flags poll request for environment %s with credential %s failed with status %d",
			envKey, auth.Masked(), resp.StatusCode)
		t.FailNow()
	}
	return ldvalue.Null()
}

func (m *integrationTestManager) msdkEvalxUsersRoute(t *testing.T, userJSON string) *url.URL {
	userBase64 := base64.URLEncoding.EncodeToString([]byte(userJSON))

	u, err := url.Parse(m.relayBaseURL + "/msdk/evalx/users/" + userBase64)
	if err != nil {
		t.Fatalf("couldn't parse flag evaluation URL: %v", err)
	}

	return u
}

func (m *integrationTestManager) sdkEvalxUsersRoute(t *testing.T, envID config.EnvironmentID, userJSON string) *url.URL {
	userBase64 := base64.URLEncoding.EncodeToString([]byte(userJSON))

	u, err := url.Parse(m.relayBaseURL + "/sdk/evalx/" + envID.String() + "/users/" + userBase64)
	if err != nil {
		t.Fatalf("couldn't parse flag evaluation URL: %v", err)
	}

	return u
}

func (m *integrationTestManager) withExtraContainer(
	t *testing.T,
	imageName string,
	args []string, hostnamePrefix string,
	action func(*docker.Container),
) {
	image, err := docker.PullImage(imageName)
	require.NoError(t, err)
	hostname := hostnamePrefix + "-" + uuid.New()
	container, err := image.NewContainerBuilder().Name(hostname).Network(m.dockerNetwork).ContainerParams(args...).Build()
	require.NoError(t, err)
	container.Start()
	go container.FollowLogs(oshelpers.NewLogWriter(os.Stdout, hostnamePrefix))
	defer func() {
		container.Stop()
		container.Delete()
	}()
	action(container)
}

// Expectations of the test when checking the status response from Relay.
type envPropertyExpectations struct {
	// The environment/project have names + keys
	nameAndKey bool
	// The environments have sdkKey and expiringSdkKey that match the expected values. Matching is determined
	// by masking the keys and comparing those masked values, since the status response obscures most of the key except
	// for the last few characters.
	sdkKeys bool
}

func verifyEnvProperties(t *testing.T, project projectInfo, environment environmentInfo, envStatus api.EnvironmentStatusRep, expectations *envPropertyExpectations) {
	assert.Equal(t, string(environment.id), envStatus.EnvID)
	if expectations == nil {
		return
	}
	if expectations.nameAndKey {
		assert.Equal(t, environment.name, envStatus.EnvName)
		assert.Equal(t, environment.key, envStatus.EnvKey)
		assert.Equal(t, project.name, envStatus.ProjName)
		assert.Equal(t, project.key, envStatus.ProjKey)
	}
	if expectations.sdkKeys {
		if !environment.expiringSdkKey.Defined() {
			assert.Empty(t, envStatus.ExpiringSDKKey, "expected no expiring SDK key to be defined")
		} else {
			assert.Equal(t, environment.expiringSdkKey.Masked(), config.SDKKey(envStatus.ExpiringSDKKey).Masked(), "expected expiring SDK key to match")
		}
		if !environment.sdkKey.Defined() {
			assert.Empty(t, envStatus.SDKKey, "expected no SDK key to be defined")
		} else {
			assert.Equal(t, environment.sdkKey.Masked(), config.SDKKey(envStatus.SDKKey).Masked(), "expected SDK key to match")
		}
	}
}

func flagKeyForProj(proj projectInfo) string {
	return "flag-for-" + proj.key
}

func evenFlagKeyForProj(proj projectInfo) string {
	return "flag0-for-" + proj.key
}

func oddFlagKeyForProj(proj projectInfo) string {
	return "flag1-for-" + proj.key
}

func flagValueForEnv(env environmentInfo) ldvalue.Value {
	return ldvalue.String("value-for-" + env.key)
}
