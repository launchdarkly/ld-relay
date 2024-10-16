//go:build integrationtests

package integrationtests

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/exp/maps"

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

		flagSetup := func() error {
			if err := api.newFlag("indirectPrereqOf1").
				On(true).
				Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
				Create(); err != nil {
				return err
			}

			if err := api.newFlag("directPrereq1").
				On(true).
				Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
				Prerequisite("indirectPrereqOf1", 1).
				Create(); err != nil {
				return err
			}

			if err := api.newFlag("directPrereq2").
				On(true).
				Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
				Create(); err != nil {
				return err
			}

			return api.newFlag("topLevel").
				On(true).
				Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
				Prerequisites([]ldapi.Prerequisite{
					{Key: "directPrereq1", Variation: 1},
					{Key: "directPrereq2", Variation: 1},
				}).Create()
		}

		require.NoError(t, flagSetup())

		manager.startRelay(t, api.envVariables())
		defer manager.stopRelay(t)

		manager.awaitEnvironments(t, api.projAndEnvs(), nil, func(proj projectInfo, env environmentInfo) string {
			return env.name
		})

		userJSON := `{"key":"any-user-key"}`

		url := manager.sdkEvalxUsersRoute(t, api.env.id, userJSON)
		gotPrerequisites := manager.getFlagPrerequisites(t, api.env.key, url, api.env.id)

		expectedPrerequisites := map[string][]string{
			"topLevel":          {"directPrereq1", "directPrereq2"},
			"directPrereq1":     {"indirectPrereqOf1"},
			"directPrereq2":     {},
			"indirectPrereqOf1": {},
		}

		requirePrerequisitesEqual(t, expectedPrerequisites, gotPrerequisites)
	})

	t.Run("ignores prereqs if not evaluated", func(t *testing.T) {
		api, err := newScopedApiHelper(manager.apiHelper)
		require.NoError(t, err)
		defer api.cleanup()

		flagSetup := func() error {
			if err := api.newFlag("prereq1").
				On(true).
				Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
				Create(); err != nil {
				return err
			}

			if err := api.newFlag("prereq2").
				On(true).
				Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
				Create(); err != nil {
				return err
			}

			if err := api.newFlag("flagOn").
				On(true).
				Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
				Prerequisite("prereq1", 1).
				Create(); err != nil {
				return err
			}

			if err := api.newFlag("flagOff").
				On(false).
				Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
				Prerequisite("prereq1", 1).
				Create(); err != nil {
				return err
			}

			return api.newFlag("failedPrereq").
				On(true).
				Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
				Prerequisites([]ldapi.Prerequisite{
					{Key: "prereq1", Variation: 0}, // wrong variation!
					{Key: "prereq2", Variation: 1}, // correct variation, but we shouldn't see it since the first prereq failed
				}).Create()
		}

		require.NoError(t, flagSetup())

		manager.startRelay(t, api.envVariables())
		defer manager.stopRelay(t)

		manager.awaitEnvironments(t, api.projAndEnvs(), nil, func(proj projectInfo, env environmentInfo) string {
			return env.name
		})

		userJSON := `{"key":"any-user-key"}`

		url := manager.sdkEvalxUsersRoute(t, api.env.id, userJSON)
		gotPrerequisites := manager.getFlagPrerequisites(t, api.env.key, url, api.env.id)

		expectedPrerequisites := map[string][]string{
			"flagOn":       {"prereq1"},
			"flagOff":      {},
			"failedPrereq": {"prereq1"},
			"prereq1":      {},
			"prereq2":      {},
		}
		requirePrerequisitesEqual(t, expectedPrerequisites, gotPrerequisites)
	})

	t.Run("exposes prerequisite relationship even if prereq is hidden from clients", func(t *testing.T) {
		t.Run("partially visible to environment ID", func(t *testing.T) {
			api, err := newScopedApiHelper(manager.apiHelper)
			require.NoError(t, err)
			defer api.cleanup()

			flagSetup := func() error {
				if err := api.newFlag("prereq1").
					On(true).Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
					ClientSideUsingEnvironmentID(true).
					ClientSideUsingMobileKey(false).
					Create(); err != nil {
					return err
				}

				if err := api.newFlag("prereq2").
					On(true).Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
					ClientSideUsingEnvironmentID(false).
					ClientSideUsingMobileKey(false).
					Create(); err != nil {
					return err
				}

				return api.newFlag("flag").
					On(true).Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
					ClientSideUsingEnvironmentID(true).
					ClientSideUsingMobileKey(false).
					Prerequisites([]ldapi.Prerequisite{
						{Key: "prereq1", Variation: 1},
						{Key: "prereq2", Variation: 1},
					}).Create()
			}

			require.NoError(t, flagSetup())

			manager.startRelay(t, api.envVariables())
			defer manager.stopRelay(t)

			manager.awaitEnvironments(t, api.projAndEnvs(), nil, func(proj projectInfo, env environmentInfo) string {
				return env.name
			})

			userJSON := `{"key":"any-user-key"}`

			url := manager.sdkEvalxUsersRoute(t, api.env.id, userJSON)
			gotPrerequisites := manager.getFlagPrerequisites(t, api.env.key, url, api.env.id)

			// prereq1 is visible to env ID, but prereq2 is not. The top level flag
			// is visible to env ID.  We should see an eval result for the top level flag (with prereqs),
			// and for prereq1, but not for prereq2.
			expectedPrerequisites := map[string][]string{
				"flag":    {"prereq1", "prereq2"},
				"prereq1": {},
			}

			requirePrerequisitesEqual(t, expectedPrerequisites, gotPrerequisites)
		})

		t.Run("partially visible to mobile key", func(t *testing.T) {
			api, err := newScopedApiHelper(manager.apiHelper)
			require.NoError(t, err)
			defer api.cleanup()

			flagSetup := func() error {
				if err := api.newFlag("prereq1").
					On(true).Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
					ClientSideUsingMobileKey(true).
					ClientSideUsingEnvironmentID(false).
					Create(); err != nil {
					return err
				}

				if err := api.newFlag("prereq2").
					On(true).Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
					ClientSideUsingMobileKey(false).
					ClientSideUsingEnvironmentID(false).
					Create(); err != nil {
					return err
				}

				return api.newFlag("flag").
					On(true).Variations(ldvalue.Bool(false), ldvalue.Bool(true)).
					ClientSideUsingMobileKey(true).
					ClientSideUsingEnvironmentID(false).
					Prerequisites([]ldapi.Prerequisite{
						{Key: "prereq1", Variation: 1},
						{Key: "prereq2", Variation: 1},
					}).Create()
			}

			require.NoError(t, flagSetup())

			manager.startRelay(t, api.envVariables())
			defer manager.stopRelay(t)

			manager.awaitEnvironments(t, api.projAndEnvs(), nil, func(proj projectInfo, env environmentInfo) string {
				return env.name
			})

			userJSON := `{"key":"any-user-key"}`

			// Note: 'msdk' not 'sdk' like the environment ID test.
			url := manager.msdkEvalxUsersRoute(t, userJSON)
			// Note: passing in mobile key here, not environment ID.
			gotPrerequisites := manager.getFlagPrerequisites(t, api.env.key, url, api.env.mobileKey)

			// prereq1 is visible to mobile keys, but prereq2 is not. The top level flag
			// is visible to mobile keys.  We should see an eval result for the top level flag (with prereqs),
			// and for prereq1, but not for prereq2.
			expectedPrerequisites := map[string][]string{
				"flag":    {"prereq1", "prereq2"},
				"prereq1": {},
			}

			requirePrerequisitesEqual(t, expectedPrerequisites, gotPrerequisites)
		})
	})
}

func requirePrerequisitesEqual(t *testing.T, expected map[string][]string, got ldvalue.Value) {
	expectedKeys := maps.Keys(expected)
	gotKeys := got.Keys(nil)

	require.ElementsMatch(t, expectedKeys, gotKeys)

	for flagKey, prereqKeys := range expected {
		prereqArray := got.GetByKey(flagKey).AsValueArray()

		actualCount := 0
		if prereqArray.IsDefined() {
			actualCount = prereqArray.Count()
		}

		assert.Equal(t, len(prereqKeys), actualCount)

		for i, expectedPrereqKey := range prereqKeys {
			actualPrereqKey := prereqArray.Get(i).StringValue()
			assert.Equal(t, expectedPrereqKey, actualPrereqKey, "prerequisites of flag %s @ index %d", flagKey, i)
		}
	}
}
