// +build integrationtests

package integrationtests

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/integrationtests/docker"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// See database_params_test.go which defines database-specific behavior

func testBigSegments(t *testing.T, manager *integrationTestManager) {
	t.Run("Redis", func(t *testing.T) {
		doBigSegmentsTests(t, manager, redisDatabaseTestParams)
	})
}

func doBigSegmentsTests(
	t *testing.T,
	manager *integrationTestManager,
	dbParams databaseTestParams,
) {
	// The test logic here is:
	// 1. Create a project with two environments (so we can prove that they coexist OK in the store).
	// 2. For each environment, create a big segment that has "included-user1" included and "excluded-user"
	// excluded in its big segment data, and that also has a regular segment rule that matches "included-user2"
	// *and* "excluded-user" (to prove that the SDK's matching logic checks these things in the right order,
	// i.e. excluded-user should not be matched because it's excluded, regardless of the rule).
	// 3. Also create a feature flag that (in every environment) returns true if the user matches that segment.
	// 4. Start Relay, configured with those two environments and a persistent data store.
	// 5. Using the evaluation endpoints, verify that the various user keys return the expected flag values
	// for each environment (true for the "included-" users, false for the "excluded-" ones). Relay may not
	// sync up immediately, so we'll keep polling the values till they're correct or we time out and give up.
	dbParams.withContainer(t, manager, func(dbContainer *docker.Container) {
		projectInfo, environments, err := manager.apiHelper.createProject(2)
		require.NoError(t, err)
		defer manager.apiHelper.deleteProject(projectInfo)

		environments[0].prefix = "prefix1"
		environments[1].prefix = "prefix2"

		segmentKey := "big-segment-key"

		type bigSegmentForEnvInfo struct {
			includedUserKey1 string
			includedUserKey2 string
			excludedUserKey  string
		}
		var bigSegments []bigSegmentForEnvInfo
		for i, env := range environments {
			var segmentInfo bigSegmentForEnvInfo
			segmentInfo.includedUserKey1 = fmt.Sprintf("included-user1-%d", i)
			segmentInfo.includedUserKey2 = fmt.Sprintf("included-user2-%d", i)
			segmentInfo.excludedUserKey = fmt.Sprintf("excluded-user-%d", i)
			bigSegments = append(bigSegments, segmentInfo)
			included := []string{segmentInfo.includedUserKey1}
			excluded := []string{segmentInfo.excludedUserKey}
			includedByRule := []string{segmentInfo.excludedUserKey, segmentInfo.includedUserKey2}
			require.NoError(t, manager.apiHelper.createBigSegment(projectInfo, env, segmentKey,
				included, excluded, includedByRule))
		}

		flagKey, err := manager.apiHelper.createBooleanFlagThatUsesSegment(projectInfo, environments, segmentKey)
		require.NoError(t, err)

		dbParams.withStartedRelay(t, manager, dbContainer, environments, nil, func() {
			latestValuesByEnv := make(map[string]map[string]ldvalue.Value)
			expectedValuesByEnv := make(map[string]map[string]ldvalue.Value)
			for i, env := range environments {
				expectedValuesByEnv[env.key] = map[string]ldvalue.Value{
					bigSegments[i].includedUserKey1: ldvalue.Bool(true),
					bigSegments[i].includedUserKey2: ldvalue.Bool(true),
					bigSegments[i].excludedUserKey:  ldvalue.Bool(false),
				}
			}
			success := assert.Eventually(t, func() bool {
				for i, env := range environments {
					latestValues := make(map[string]ldvalue.Value)
					for _, userKey := range []string{bigSegments[i].includedUserKey1, bigSegments[i].includedUserKey2, bigSegments[i].excludedUserKey} {
						userJSON := fmt.Sprintf(`{"key":"%s"}`, userKey)
						latestValues[userKey] = manager.getFlagValues(t, projectInfo, env, userJSON).GetByKey(flagKey)
					}
					latestValuesByEnv[env.key] = latestValues
				}
				return reflect.DeepEqual(latestValuesByEnv, expectedValuesByEnv)
			}, time.Second*10, time.Millisecond*100, "Did not see expected flag values from Relay")
			if !success {
				manager.loggers.Infof("Last values for each environment and user were: %s", latestValuesByEnv)
				manager.loggers.Infof("Expected: %s", expectedValuesByEnv)
			}
		})
	})
}
