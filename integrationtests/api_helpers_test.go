// +build integrationtests

package integrationtests

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/launchdarkly/ld-relay/v6/config"

	ldapi "github.com/launchdarkly/api-client-go"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldtime"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"

	"github.com/antihax/optional"
	"github.com/pborman/uuid"
)

type apiHelper struct {
	apiClient  *ldapi.APIClient
	apiContext context.Context
	apiBaseURL string
	loggers    ldlog.Loggers
}

func (a *apiHelper) logResult(desc string, err error) error {
	if err == nil {
		a.loggers.Infof("%s: OK", desc)
		return nil
	}
	addInfo := ""
	if gse, ok := err.(ldapi.GenericSwaggerError); ok {
		body := string(gse.Body())
		addInfo = " - " + string(body)
	}
	a.loggers.Errorf("%s: FAILED - %s%s", desc, err, addInfo)
	return err
}

func (a *apiHelper) createProjectsAndEnvironments(numProjects, numEnvironments int) (projsAndEnvs, error) {
	ret := make(projsAndEnvs)
	for i := 0; i < numProjects; i++ {
		proj, envs, err := a.createProject(numEnvironments)
		if err != nil {
			_ = a.deleteProjects(ret)
			return nil, err
		}
		ret[proj] = envs
	}
	return ret, nil
}

func (a *apiHelper) deleteProjects(projsAndEnvs projsAndEnvs) error {
	for p := range projsAndEnvs {
		if err := a.deleteProject(p); err != nil {
			return err
		}
	}
	return nil
}

func (a *apiHelper) createProject(numEnvironments int) (projectInfo, []environmentInfo, error) {
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
	project, _, err := a.apiClient.ProjectsApi.PostProject(a.apiContext, projectBody)
	if err != nil {
		return projectInfo{}, nil, a.logResult("Create project", err)
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
	a.loggers.Infof("Created project %q\n", projKey)
	return projectInfo{key: projKey, name: projName}, envInfos, nil
}

func randomApiKey(prefix string) string {
	return (prefix + uuid.New())[0:20]
}

func (a *apiHelper) deleteProject(project projectInfo) error {
	_, err := a.apiClient.ProjectsApi.DeleteProject(a.apiContext, project.key)
	return a.logResult(fmt.Sprintf("Delete project %q", project.key), err)
}

func (a *apiHelper) addEnvironment(project projectInfo) (environmentInfo, error) {
	envKey := randomApiKey("env-")
	envName := envKey
	envBody := ldapi.EnvironmentPost{
		Key:   envKey,
		Name:  envName,
		Color: "000000",
	}
	env, _, err := a.apiClient.EnvironmentsApi.PostEnvironment(a.apiContext, project.key, envBody)
	if err != nil {
		return environmentInfo{}, a.logResult("Create environment", err)
	}
	a.loggers.Infof("created environment %q\n", envKey)
	return environmentInfo{
		id:        config.EnvironmentID(env.Id),
		key:       env.Key,
		name:      env.Name,
		sdkKey:    config.SDKKey(env.ApiKey),
		mobileKey: config.MobileKey(env.MobileKey),
	}, nil
}

func (a *apiHelper) deleteEnvironment(project projectInfo, env environmentInfo) error {
	_, err := a.apiClient.EnvironmentsApi.DeleteEnvironment(a.apiContext, project.key, env.key)
	return a.logResult(fmt.Sprintf("Delete environment %q", env.key), err)
}

func (a *apiHelper) rotateSDKKey(project projectInfo, env environmentInfo, expirationTime time.Time) (
	config.SDKKey, error) {
	var apiOptions *ldapi.EnvironmentsApiResetEnvironmentSDKKeyOpts
	if !expirationTime.IsZero() {
		apiOptions = &ldapi.EnvironmentsApiResetEnvironmentSDKKeyOpts{Expiry: optional.NewInt64(int64(ldtime.UnixMillisFromTime(expirationTime)))}
	}
	envResult, _, err := a.apiClient.EnvironmentsApi.ResetEnvironmentSDKKey(a.apiContext, project.key, env.key, apiOptions)
	var newKey config.SDKKey
	if err == nil {
		newKey = config.SDKKey(envResult.ApiKey)
	}
	return newKey, a.logResult(fmt.Sprintf("Change SDK key for environment %q", env.key), err)
}

func (a *apiHelper) rotateMobileKey(project projectInfo, env environmentInfo) (config.MobileKey, error) {
	envResult, _, err := a.apiClient.EnvironmentsApi.ResetEnvironmentMobileKey(a.apiContext, project.key, env.key, nil)
	var newKey config.MobileKey
	if err == nil {
		newKey = config.MobileKey(envResult.MobileKey)
	}
	return newKey, a.logResult(fmt.Sprintf("Change mobile key for environment %q", env.key), err)
}

func (a *apiHelper) createAutoConfigKey(policyResources []string) (autoConfigID, config.AutoConfigKey, error) {
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
	entity, _, err := a.apiClient.RelayProxyConfigurationsApi.PostRelayAutoConfig(a.apiContext, body)
	return autoConfigID(entity.Id), config.AutoConfigKey(entity.FullKey), a.logResult("Create auto-config key", err)
}

func (a *apiHelper) updateAutoConfigPolicy(id autoConfigID, newPolicyResources []string) error {
	var patchValue interface{} = newPolicyResources
	patchOps := []ldapi.PatchOperation{
		{Op: "replace", Path: "/policy/0/resources", Value: &patchValue},
	}
	_, _, err := a.apiClient.RelayProxyConfigurationsApi.PatchRelayProxyConfig(a.apiContext, string(id), patchOps)
	return a.logResult("Update auto-config policy", err)
}

func (a *apiHelper) deleteAutoConfigKey(id autoConfigID) error {
	_, err := a.apiClient.RelayProxyConfigurationsApi.DeleteRelayProxyConfig(a.apiContext, string(id))
	return a.logResult("Delete auto-config key", err)
}

// createFlag creates a flag in the specified project, and configures each environment to return a specific
// value for that flag in that environment which we'll check for later in verifyFlagValues.
func (a *apiHelper) createFlag(
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

	_, _, err := a.apiClient.FeatureFlagsApi.PostFeatureFlag(a.apiContext, proj.key, flagPost, nil)
	err = a.logResult("Create flag "+flagKey+" in "+proj.key, err)
	if err != nil {
		return err
	}

	for i, env := range envs {
		envPrefix := fmt.Sprintf("/environments/%s", env.key)
		patches := []ldapi.PatchOperation{
			makePatch("replace", envPrefix+"/offVariation", i),
		}
		_, _, err = a.apiClient.FeatureFlagsApi.PatchFeatureFlag(a.apiContext, proj.key, flagKey,
			ldapi.PatchComment{Patch: patches})
		err = a.logResult("Configure flag "+flagKey+" for "+env.key, err)
		if err != nil {
			return err
		}
	}

	return nil
}

func (a *apiHelper) createFlags(projsAndEnvs projsAndEnvs) error {
	for proj, envs := range projsAndEnvs {
		err := a.createFlag(proj, envs, flagKeyForProj(proj), flagValueForEnv)
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *apiHelper) createBooleanFlagThatUsesSegment(
	project projectInfo,
	envs []environmentInfo,
	segmentKey string,
) (string, error) {
	flagKey := flagKeyForProj(project)
	flagPost := ldapi.FeatureFlagBody{
		Name: flagKey,
		Key:  flagKey,
	}
	for _, value := range []bool{true, false} {
		var valueAsInterface interface{} = value
		flagPost.Variations = append(flagPost.Variations, ldapi.Variation{Value: &valueAsInterface})
	}

	_, _, err := a.apiClient.FeatureFlagsApi.PostFeatureFlag(a.apiContext, project.key, flagPost, nil)
	err = a.logResult("Create flag "+flagKey+" in "+project.key, err)
	if err != nil {
		return "", err
	}

	for _, env := range envs {
		envPrefix := fmt.Sprintf("/environments/%s", env.key)
		rulesJSON := fmt.Sprintf(`[
			{"variation": 0, "clauses": [{"attribute": "", "op": "segmentMatch", "values":["%s"]}]}
		]`, segmentKey)
		patches := []ldapi.PatchOperation{
			makePatch("replace", envPrefix+"/on", true),
			makePatch("replace", envPrefix+"/rules", json.RawMessage(rulesJSON)),
			makePatch("replace", envPrefix+"/fallthrough/variation", 1),
		}
		_, _, err = a.apiClient.FeatureFlagsApi.PatchFeatureFlag(a.apiContext, project.key, flagKey,
			ldapi.PatchComment{Patch: patches})
		err = a.logResult("Configure flag "+flagKey+" for "+env.key, err)
		if err != nil {
			return "", err
		}
	}
	return flagKey, nil
}

func (a *apiHelper) createBigSegment(
	project projectInfo,
	env environmentInfo,
	segmentKey string,
	userKeysIncluded []string,
	userKeysExcluded []string,
	userKeysIncludedViaRule []string,
) error {
	userSegmentBody := ldapi.UserSegmentBody{
		Key:       segmentKey,
		Name:      segmentKey,
		Unbounded: true,
	}
	_, _, err := a.apiClient.UserSegmentsApi.PostUserSegment(a.apiContext, project.key, env.key, userSegmentBody)
	if err = a.logResult("Create "+segmentKey, err); err != nil {
		return err
	}

	if len(userKeysIncludedViaRule) > 0 {
		rulesJSON := fmt.Sprintf(`[
			{"clauses": [{"attribute": "key", "op": "in", "values":%v}]}
		]`, ldvalue.CopyArbitraryValue(userKeysIncludedViaRule))
		patches := []ldapi.PatchOperation{
			makePatch("replace", "/rules", json.RawMessage(rulesJSON)),
		}
		_, _, err := a.apiClient.UserSegmentsApi.PatchUserSegment(a.apiContext, project.key, env.key,
			segmentKey, patches)
		if err = a.logResult("Add rule to "+segmentKey, err); err != nil {
			return err
		}
	}

	updates := ldapi.BigSegmentTargetsBody{
		Included: &ldapi.BigSegmentTargetChanges{Add: userKeysIncluded},
		Excluded: &ldapi.BigSegmentTargetChanges{Add: userKeysExcluded},
	}
	_, err = a.apiClient.UserSegmentsApi.UpdatedBigSegmentTargets(a.apiContext, project.key, env.key,
		segmentKey, updates)
	return a.logResult("Update "+segmentKey, err)
}

func makePatch(op, path string, value interface{}) ldapi.PatchOperation {
	return ldapi.PatchOperation{Op: op, Path: path, Value: &value}
}
