//go:build integrationtests

package integrationtests

import (
	ldapi "github.com/launchdarkly/api-client-go/v13"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/stretchr/testify/require"
	"testing"
)

type scopedApiHelper struct {
	project   projectInfo
	env       environmentInfo
	apiHelper *apiHelper
}

func newScopedApiHelper(apiHelper *apiHelper) (*scopedApiHelper, error) {
	project, envs, err := apiHelper.createProject(1)
	if err != nil {
		return nil, err
	}
	return &scopedApiHelper{
		apiHelper: apiHelper,
		project:   project,
		env:       envs[0],
	}, nil
}

func (s *scopedApiHelper) envVariables() map[string]string {
	return map[string]string{
		"LD_ENV_" + string(s.env.name):            string(s.env.sdkKey),
		"LD_MOBILE_KEY_" + string(s.env.name):     string(s.env.mobileKey),
		"LD_CLIENT_SIDE_ID_" + string(s.env.name): string(s.env.id),
	}
}

func (s *scopedApiHelper) projAndEnvs() projsAndEnvs {
	return projsAndEnvs{
		s.project: {s.env},
	}
}

func (s *scopedApiHelper) cleanup() {
	s.apiHelper.deleteProject(s.project)
}

func (s *scopedApiHelper) createFlagWithVariations(key string, on bool, variation1 ldvalue.Value, variation2 ldvalue.Value) error {
	return s.apiHelper.createFlagWithVariations(s.project, s.env, key, on, variation1, variation2)
}

func (s *scopedApiHelper) createFlagWithPrerequisites(key string, on bool, variation1 ldvalue.Value, variation2 ldvalue.Value, prereqs []ldapi.Prerequisite) error {
	return s.apiHelper.createFlagWithPrerequisites(s.project, s.env, key, on, variation1, variation2, prereqs)
}

func makeTopLevelPrerequisites(api *scopedApiHelper) (map[string][]string, error) {

	// topLevel -> directPrereq1, directPrereq2
	// directPrereq1 -> indirectPrereqOf1

	if err := api.createFlagWithVariations("indirectPrereqOf1", true, ldvalue.Bool(false), ldvalue.Bool(true)); err != nil {
		return nil, err
	}

	if err := api.createFlagWithPrerequisites("directPrereq1", true, ldvalue.Bool(false), ldvalue.Bool(true), []ldapi.Prerequisite{
		{Key: "indirectPrereqOf1", Variation: 1},
	}); err != nil {
		return nil, err
	}

	if err := api.createFlagWithVariations("directPrereq2", true, ldvalue.Bool(false), ldvalue.Bool(true)); err != nil {
		return nil, err
	}

	// The createFlagWithVariations call sets up two variations, with the second one being used if the flag is on.
	// The test here is to see which prerequisites were evaluated for a given flag. If a prerequisite fails, the eval
	// algorithm is going to short-circuit and we won't see the other prerequisite. So, we'll have two prerequisites,
	// both of which are on, and both of which are satisfied. That way the evaluator will be forced to visit both,
	// and we'll see the list of both when we query the eval endpoint.

	if err := api.createFlagWithPrerequisites("topLevel", true, ldvalue.Bool(false), ldvalue.Bool(true),
		[]ldapi.Prerequisite{
			{Key: "directPrereq1", Variation: 1},
			{Key: "directPrereq2", Variation: 1},
		}); err != nil {
		return nil, err
	}

	return map[string][]string{
		"topLevel":          {"directPrereq1", "directPrereq2"},
		"directPrereq1":     {"indirectPrereqOf1"},
		"directPrereq2":     {},
		"indirectPrereqOf1": {},
	}, nil
}

func makeFailedPrerequisites(api *scopedApiHelper) (map[string][]string, error) {

	// flagOn -> prereq1
	// failedPrereq -> prereq1

	if err := api.createFlagWithVariations("prereq1", true, ldvalue.Bool(false), ldvalue.Bool(true)); err != nil {
		return nil, err
	}

	if err := api.createFlagWithVariations("prereq2", true, ldvalue.Bool(false), ldvalue.Bool(true)); err != nil {
		return nil, err
	}

	if err := api.createFlagWithPrerequisites("flagOn", true, ldvalue.Bool(false), ldvalue.Bool(true), []ldapi.Prerequisite{
		{Key: "prereq1", Variation: 1},
	}); err != nil {
		return nil, err
	}

	if err := api.createFlagWithPrerequisites("flagOff", false, ldvalue.Bool(false), ldvalue.Bool(true), []ldapi.Prerequisite{
		{Key: "prereq1", Variation: 1},
	}); err != nil {
		return nil, err
	}

	if err := api.createFlagWithPrerequisites("failedPrereq", true, ldvalue.Bool(false), ldvalue.Bool(true), []ldapi.Prerequisite{
		{Key: "prereq1", Variation: 0}, // wrong variation!
		{Key: "prereq2", Variation: 1}, // correct variation, but we shouldn't see it since the first prereq failed
	}); err != nil {
		return nil, err
	}

	return map[string][]string{
		"flagOn":       {"prereq1"},
		"flagOff":      {},
		"failedPrereq": {"prereq1"},
		"prereq1":      {},
		"prereq2":      {},
	}, nil

}

// TODO: Make a builder for the API client so that all flag options can be accessed.

func testStandardModeWithPrerequisites(t *testing.T, manager *integrationTestManager) {
	t.Run("includes top-level prerequisites", func(t *testing.T) {
		api, err := newScopedApiHelper(manager.apiHelper)
		require.NoError(t, err)
		defer api.cleanup()

		prerequisites, err := makeTopLevelPrerequisites(api)
		require.NoError(t, err)

		manager.startRelay(t, api.envVariables())
		defer manager.stopRelay(t)

		manager.awaitEnvironments(t, api.projAndEnvs(), nil, func(proj projectInfo, env environmentInfo) string {
			return env.name
		})
		manager.verifyFlagPrerequisites(t, api.projAndEnvs(), prerequisites)
	})

	t.Run("ignores prereqs if not evaluated", func(t *testing.T) {
		api, err := newScopedApiHelper(manager.apiHelper)
		require.NoError(t, err)
		defer api.cleanup()

		prerequisites, err := makeFailedPrerequisites(api)
		require.NoError(t, err)

		manager.startRelay(t, api.envVariables())
		defer manager.stopRelay(t)

		manager.awaitEnvironments(t, api.projAndEnvs(), nil, func(proj projectInfo, env environmentInfo) string {
			return env.name
		})
		manager.verifyFlagPrerequisites(t, api.projAndEnvs(), prerequisites)
	})
}
