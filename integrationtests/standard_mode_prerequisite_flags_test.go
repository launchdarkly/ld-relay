//go:build integrationtests

package integrationtests

import (
	"testing"

	ldapi "github.com/launchdarkly/api-client-go/v13"
	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"
	"github.com/stretchr/testify/require"
)

// scopedApiHelper is meant to be a wrapper around the base apiHelper which scopes operations to a single
// project/environment. It was created specifically for the prerequisite tests, since we aren't trying to verify
// assertions across projects/environments - just that prerequisites are correct within a single payload.
// This pattern could be extended or refactored into a dedicated helper package if necessary.
type scopedApiHelper struct {
	project   projectInfo
	env       environmentInfo
	apiHelper *apiHelper
}

// newScopedApiHelper wraps an existing apiHelper, automatically creating a project with a single environment.
// Be sure to call cleanup() when done to delete the project, otherwise orphan projects will accumulate in the
// testing account.
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

// envVariables returns all the environment variables needed for Relay to be aware of the environment
// and authenticate with it.
func (s *scopedApiHelper) envVariables() map[string]string {
	return map[string]string{
		"LD_ENV_" + string(s.env.name):            string(s.env.sdkKey),
		"LD_MOBILE_KEY_" + string(s.env.name):     string(s.env.mobileKey),
		"LD_CLIENT_SIDE_ID_" + string(s.env.name): string(s.env.id),
	}
}

// projsAndEnvs returns a map of project -> environment, which is necessary to interact with the integration
// test manager's awaitEnvironments method.
func (s *scopedApiHelper) projAndEnvs() projsAndEnvs {
	return projsAndEnvs{
		s.project: {s.env},
	}
}

// cleanup deletes the project and environment created by this scopedApiHelper. A common pattern in tests would be
// calling newScopedApiHelper, then deferring the cleanup call immediately after.
func (s *scopedApiHelper) cleanup() {
	s.apiHelper.deleteProject(s.project)
}

// newFlag creates a new flag in the project. In LaunchDarkly, flags are created across all environments. The flag
// builder allows configuring different aspects of the flag, such as variations and prerequisites - this configuration
// is scoped to the single environment created by the scopedApiHelper.
func (s *scopedApiHelper) newFlag(key string) *flagBuilder {
	return newFlagBuilder(s.apiHelper, key, s.project.key, s.env.key)
}

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

	t.Run("ignores client-side-only for prereq keys", func(t *testing.T) {
		api, err := newScopedApiHelper(manager.apiHelper)
		require.NoError(t, err)
		defer api.cleanup()

		prerequisites, err := makeIgnoreClientSideOnlyPrereqs(api)
		require.NoError(t, err)

		manager.startRelay(t, api.envVariables())
		defer manager.stopRelay(t)

		manager.awaitEnvironments(t, api.projAndEnvs(), nil, func(proj projectInfo, env environmentInfo) string {
			return env.name
		})

		manager.verifyFlagPrerequisites(t, api.projAndEnvs(), prerequisites)
	})
}

func makeTopLevelPrerequisites(api *scopedApiHelper) (map[string][]string, error) {
	// topLevel -> directPrereq1, directPrereq2
	// directPrereq1 -> indirectPrereqOf1

	if err := api.newFlag("indirectPrereqOf1").
		On(true).
		Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		Create(); err != nil {
		return nil, err
	}

	if err := api.newFlag("directPrereq1").
		On(true).
		Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		Prerequisite("indirectPrereqOf1", 1).
		Create(); err != nil {
		return nil, err
	}

	if err := api.newFlag("directPrereq2").
		On(true).
		Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		Create(); err != nil {
		return nil, err
	}

	if err := api.newFlag("topLevel").
		On(true).
		Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		Prerequisites([]ldapi.Prerequisite{
			{Key: "directPrereq1", Variation: 1},
			{Key: "directPrereq2", Variation: 1},
		}).Create(); err != nil {
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

	if err := api.newFlag("prereq1").
		On(true).
		Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		Create(); err != nil {
		return nil, err
	}

	if err := api.newFlag("prereq2").
		On(true).
		Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		Create(); err != nil {
		return nil, err
	}

	if err := api.newFlag("flagOn").
		On(true).
		Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		Prerequisite("prereq1", 1).
		Create(); err != nil {
		return nil, err
	}

	if err := api.newFlag("flagOff").
		On(false).
		Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		Prerequisite("prereq1", 1).
		Create(); err != nil {
		return nil, err
	}

	if err := api.newFlag("failedPrereq").
		On(true).
		Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		Prerequisites([]ldapi.Prerequisite{
			{Key: "prereq1", Variation: 0}, // wrong variation!
			{Key: "prereq2", Variation: 1}, // correct variation, but we shouldn't see it since the first prereq failed
		}).Create(); err != nil {
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

func makeIgnoreClientSideOnlyPrereqs(api *scopedApiHelper) (map[string][]string, error) {
	// flag -> prereq1, prereq2

	if err := api.newFlag("prereq1").
		On(true).Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		ClientSideUsingEnvironmentID(true).
		Create(); err != nil {
		return nil, err
	}

	if err := api.newFlag("prereq2").
		On(true).Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		ClientSideUsingEnvironmentID(false).
		Create(); err != nil {
		return nil, err
	}
	if err := api.newFlag("flag").
		On(true).Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
		ClientSideUsingEnvironmentID(true).
		Prerequisites([]ldapi.Prerequisite{
			{Key: "prereq1", Variation: 1},
			{Key: "prereq2", Variation: 1},
		}).Create(); err != nil {
		return nil, err
	}

	return map[string][]string{
		"flag":    {"prereq1", "prereq2"},
		"prereq1": {},
	}, nil
}
