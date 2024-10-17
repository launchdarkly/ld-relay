//go:build integrationtests

package integrationtests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	ldapi "github.com/launchdarkly/api-client-go/v13"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldtime"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"

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
	var gse *ldapi.GenericOpenAPIError
	if errors.As(err, &gse) {
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

func (a *apiHelper) createProjectsAndEnvironmentsWithFilters(numProjects, numEnvironments, numFilters int) (projsAndEnvs, error) {
	ret := make(projsAndEnvs)
	for i := 0; i < numProjects; i++ {
		proj, envs, err := a.createProject(numEnvironments)
		if err != nil {
			_ = a.deleteProjects(ret)
			return nil, err
		}
		filters, err := a.createFilters(proj.key, numFilters)
		if err != nil {
			_ = a.deleteProjects(ret)
			return nil, err
		}
		proj.filters = strings.Join(filters, ",")
		ret[proj] = envs
	}
	return ret, nil
}

func (a *apiHelper) createProjectsAndEnvironmentsWithSpecificFilters(numProjects, numEnvironments int, filters map[string]filterRules) (projsAndEnvs, error) {
	ret := make(projsAndEnvs)
	for i := 0; i < numProjects; i++ {
		proj, envs, err := a.createProject(numEnvironments)
		if err != nil {
			_ = a.deleteProjects(ret)
			return nil, err
		}

		var createdFilters []string
		for filterKey, filterRules := range filters {
			filter, err := a.createSpecificFilter(proj.key, filterKey, filterRules)
			if err != nil {
				_ = a.deleteProjects(ret)
				return nil, err
			}
			createdFilters = append(createdFilters, filter)
		}

		proj.filters = strings.Join(createdFilters, ",")

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
	projKey := "relayi9n-" + strings.ReplaceAll(time.Now().Format("2006_01_02_15_04_05.0000"), "_", "")
	projName := projKey
	projectBody := ldapi.ProjectPost{
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

	project, _, err := a.apiClient.ProjectsApi.
		PostProject(a.apiContext).
		ProjectPost(projectBody).
		Execute()

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
			projKey:   projKey,
		})
	}
	a.loggers.Infof("Created project %q\n", projKey)
	return projectInfo{key: projKey, name: projName}, envInfos, nil
}

type filterCondition struct {
	kind     string
	property string
	regex    string
}
type filterRule struct {
	action    string
	condition filterCondition
}

type filterRules []filterRule

// This exists because the API doesn't have a specific representation struct for the rules.
// Remove it when it does.
func (f filterRules) ToOpaqueRep() []map[string]interface{} {
	var opaqueRules []map[string]interface{}
	for _, r := range f {
		rule := map[string]interface{}{
			"action": r.action,
			"condition": map[string]string{
				"kind":     r.condition.kind,
				"property": r.condition.property,
				"regex":    r.condition.regex,
			},
		}
		opaqueRules = append(opaqueRules, rule)
	}
	return opaqueRules
}
func (a *apiHelper) createSpecificFilter(projKey string, filterKey string, rules filterRules) (string, error) {
	filterRep, _, err := a.apiClient.PayloadFiltersApi.PostPayloadFilters(a.apiContext, projKey).PostFilterRep(ldapi.PostFilterRep{
		Key:         filterKey,
		Name:        "Relay Integration test filter",
		Description: "Test filter for Relay Proxy",
		Rules:       rules.ToOpaqueRep(),
	}).Execute()
	if err != nil {
		return "", a.logResult("postPayloadFilter", err)
	}
	a.loggers.Infof("Created filter %q\n", filterRep.Key)
	return filterRep.Key, nil
}

func (a *apiHelper) createFilters(projKey string, numFilters int) ([]string, error) {
	var filters []ldapi.PostFilterRep
	for i := 0; i < numFilters; i++ {
		filters = append(filters, ldapi.PostFilterRep{
			Key:         fmt.Sprintf("%s-%s-%v", projKey, "relay-integration-test-filter", i),
			Name:        fmt.Sprintf("Relay Integration test filter (%s) (%v)", projKey, i),
			Description: "Test filter for Relay Proxy",
		})
	}

	var filterKeys []string
	for _, filter := range filters {
		filterRep, _, err := a.apiClient.PayloadFiltersApi.PostPayloadFilters(a.apiContext, projKey).PostFilterRep(filter).Execute()
		if err != nil {
			return filterKeys, a.logResult("postPayloadFilter", err)
		}
		a.loggers.Infof("Created filter %q\n", filterRep.Key)
		filterKeys = append(filterKeys, filterRep.Key)
	}

	return filterKeys, nil
}

func randomApiKey(prefix string) string {
	return (prefix + uuid.New())[0:20]
}

func (a *apiHelper) deleteProject(project projectInfo) error {
	_, err := a.apiClient.ProjectsApi.DeleteProject(a.apiContext, project.key).Execute()
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
	env, _, err := a.apiClient.EnvironmentsApi.
		PostEnvironment(a.apiContext, project.key).
		EnvironmentPost(envBody).
		Execute()

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
	_, err := a.apiClient.EnvironmentsApi.DeleteEnvironment(a.apiContext, project.key, env.key).Execute()
	return a.logResult(fmt.Sprintf("Delete environment %q", env.key), err)
}

func (a *apiHelper) rotateSDKKey(project projectInfo, env environmentInfo, expirationTime time.Time) (
	config.SDKKey, error) {
	req := a.apiClient.EnvironmentsApi.ResetEnvironmentSDKKey(a.apiContext, project.key, env.key)
	if !expirationTime.IsZero() {
		req = req.Expiry(int64(ldtime.UnixMillisFromTime(expirationTime)))
	}
	envResult, _, err := req.Execute()
	var newKey config.SDKKey
	if err == nil {
		newKey = config.SDKKey(envResult.ApiKey)
	}
	return newKey, a.logResult(fmt.Sprintf("Change SDK key for environment %q", env.key), err)
}

func (a *apiHelper) rotateMobileKey(project projectInfo, env environmentInfo) (config.MobileKey, error) {
	envResult, _, err := a.apiClient.EnvironmentsApi.
		ResetEnvironmentMobileKey(a.apiContext, project.key, env.key).
		Execute()

	var newKey config.MobileKey
	if err == nil {
		newKey = config.MobileKey(envResult.MobileKey)
	}
	return newKey, a.logResult(fmt.Sprintf("Change mobile key for environment %q", env.key), err)
}

func (a *apiHelper) createAutoConfigKey(policyResources []string) (autoConfigID, config.AutoConfigKey, error) {
	body := ldapi.RelayAutoConfigPost{
		Name: fmt.Sprintf("relayi9n-%s", uuid.New()),
		Policy: []ldapi.Statement{
			{
				Resources: policyResources,
				Actions:   []string{"*"},
				Effect:    "allow",
			},
		},
	}

	entity, _, err := a.apiClient.RelayProxyConfigurationsApi.
		PostRelayAutoConfig(a.apiContext).
		RelayAutoConfigPost(body).
		Execute()

	return autoConfigID(entity.Id), config.AutoConfigKey(entity.FullKey), a.logResult("Create auto-config key", err)
}

func (a *apiHelper) updateAutoConfigPolicy(id autoConfigID, newPolicyResources []string) error {
	var patchValue interface{} = newPolicyResources

	patchOps := ldapi.PatchWithComment{
		Patch: []ldapi.PatchOperation{
			{Op: "replace", Path: "/policy/0/resources", Value: &patchValue},
		},
	}
	_, _, err := a.apiClient.RelayProxyConfigurationsApi.
		PatchRelayAutoConfig(a.apiContext, string(id)).
		PatchWithComment(patchOps).
		Execute()

	return a.logResult("Update auto-config policy", err)
}

func (a *apiHelper) deleteAutoConfigKey(id autoConfigID) error {
	_, err := a.apiClient.RelayProxyConfigurationsApi.DeleteRelayAutoConfig(a.apiContext, string(id)).Execute()
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

	_, _, err := a.apiClient.FeatureFlagsApi.
		PostFeatureFlag(a.apiContext, proj.key).
		FeatureFlagBody(flagPost).
		Execute()

	err = a.logResult("Create flag "+flagKey+" in "+proj.key, err)
	if err != nil {
		return err
	}

	for i, env := range envs {
		envPrefix := fmt.Sprintf("/environments/%s", env.key)
		patch := ldapi.PatchWithComment{
			Patch: []ldapi.PatchOperation{
				makePatch("replace", envPrefix+"/offVariation", i),
			},
		}
		_, _, err = a.apiClient.FeatureFlagsApi.
			PatchFeatureFlag(a.apiContext, proj.key, flagKey).
			PatchWithComment(patch).
			Execute()

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

func (a *apiHelper) createEvenAndOddFlags(projsAndEnvs projsAndEnvs) error {
	for proj, envs := range projsAndEnvs {
		if err := a.createFlag(proj, envs, evenFlagKeyForProj(proj), flagValueForEnv); err != nil {
			return err
		}
		if err := a.createFlag(proj, envs, oddFlagKeyForProj(proj), flagValueForEnv); err != nil {
			return err
		}
	}
	return nil
}

func (a *apiHelper) createBooleanFlagThatUsesSegment(
	flagKey string,
	project projectInfo,
	envs []environmentInfo,
	segmentKey string,
) error {
	flagPost := ldapi.FeatureFlagBody{
		Name: flagKey,
		Key:  flagKey,
	}
	for _, value := range []bool{true, false} {
		var valueAsInterface interface{} = value
		flagPost.Variations = append(flagPost.Variations, ldapi.Variation{Value: &valueAsInterface})
	}

	_, _, err := a.apiClient.FeatureFlagsApi.
		PostFeatureFlag(a.apiContext, project.key).
		FeatureFlagBody(flagPost).
		Execute()

	err = a.logResult("Create flag "+flagKey+" in "+project.key, err)
	if err != nil {
		return err
	}

	for _, env := range envs {
		envPrefix := fmt.Sprintf("/environments/%s", env.key)
		rulesJSON := fmt.Sprintf(`[
			{"variation": 0, "clauses": [{"attribute": "", "op": "segmentMatch", "values":["%s"]}]}
		]`, segmentKey)
		patch := ldapi.PatchWithComment{
			Patch: []ldapi.PatchOperation{
				makePatch("replace", envPrefix+"/on", true),
				makePatch("replace", envPrefix+"/rules", json.RawMessage(rulesJSON)),
				makePatch("replace", envPrefix+"/fallthrough/variation", 1),
			},
		}
		_, _, err = a.apiClient.FeatureFlagsApi.
			PatchFeatureFlag(a.apiContext, project.key, flagKey).
			PatchWithComment(patch).
			Execute()

		err = a.logResult("Configure flag "+flagKey+" for "+env.key, err)
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *apiHelper) createBigSegment(
	project projectInfo,
	env environmentInfo,
	segmentKey string,
	userKeysIncluded []string,
	userKeysExcluded []string,
	userKeysIncludedViaRule []string,
) error {
	userSegmentBody := ldapi.SegmentBody{
		Key:       segmentKey,
		Name:      segmentKey,
		Unbounded: ldapi.PtrBool(true),
	}
	_, _, err := a.apiClient.SegmentsApi.
		PostSegment(a.apiContext, project.key, env.key).
		SegmentBody(userSegmentBody).
		Execute()

	if err = a.logResult("Create segment '"+segmentKey+"' with unbounded set to true", err); err != nil {
		return err
	}

	if len(userKeysIncludedViaRule) > 0 {
		rulesJSON := fmt.Sprintf(`[
			{"clauses": [{"attribute": "key", "op": "in", "values":%v}]}
		]`, ldvalue.CopyArbitraryValue(userKeysIncludedViaRule))
		patches := ldapi.PatchWithComment{
			Patch: []ldapi.PatchOperation{
				makePatch("replace", "/rules", json.RawMessage(rulesJSON)),
			},
		}
		_, _, err := a.apiClient.SegmentsApi.
			PatchSegment(a.apiContext, project.key, env.key, segmentKey).
			PatchWithComment(patches).
			Execute()

		if err = a.logResult("Add rule to "+segmentKey, err); err != nil {
			return err
		}
	}

	updates := ldapi.SegmentUserState{
		Included: &ldapi.SegmentUserList{Add: userKeysIncluded},
		Excluded: &ldapi.SegmentUserList{Add: userKeysExcluded},
	}
	_, err = a.apiClient.SegmentsApi.
		UpdateBigSegmentTargets(a.apiContext, project.key, env.key, segmentKey).
		SegmentUserState(updates).
		Execute()

	return a.logResult("Update big segment targets for "+segmentKey, err)
}

func makePatch(op, path string, value interface{}) ldapi.PatchOperation {
	return ldapi.PatchOperation{Op: op, Path: path, Value: &value}
}
