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

type bigSegmentTestData struct {
	includedUserKeys          []string
	excludedUserKeys          []string
	includedByRuleUserKeys    []string
	allUserKeys               []string
	expectedFlagValuesForUser map[string]ldvalue.Value
}

func makeBigSegmentTestDataForEnvs(envs []environmentInfo) []bigSegmentTestData {
	var ret []bigSegmentTestData
	for i := range envs {
		var info bigSegmentTestData
		userKey1 := fmt.Sprintf("included-user1-%d", i)
		userKey2 := fmt.Sprintf("included-user2-%d", i)
		userKey3 := fmt.Sprintf("excluded-user-%d", i)
		info.includedUserKeys = []string{userKey1, userKey2}
		info.excludedUserKeys = []string{userKey3}
		info.includedByRuleUserKeys = []string{userKey3, userKey2}
		info.allUserKeys = []string{userKey1, userKey2, userKey3}
		info.expectedFlagValuesForUser = map[string]ldvalue.Value{
			userKey1: ldvalue.Bool(true),
			userKey2: ldvalue.Bool(true),
			userKey3: ldvalue.Bool(false),
		}
		ret = append(ret, info)
	}
	return ret
}

func testBigSegments(t *testing.T, manager *integrationTestManager) {
	t.Run("Redis", func(t *testing.T) {
		doBigSegmentsTestWithPreExistingSegment(t, manager, redisDatabaseTestParams)
		doBigSegmentsTestWithFirstSegmentAddedAfterStartup(t, manager, redisDatabaseTestParams)
		doBigSegmentsTestWithAnotherSegmentAddedAfterStartup(t, manager, redisDatabaseTestParams)
	})
}

func verifyEvaluationWithBigSegment(
	t *testing.T,
	manager *integrationTestManager,
	projectInfo projectInfo,
	environments []environmentInfo,
	flagKey string,
	segmentTestData []bigSegmentTestData,
) bool {
	latestValuesByEnv := make(map[string]map[string]ldvalue.Value)
	expectedValuesByEnv := make(map[string]map[string]ldvalue.Value)
	for i, env := range environments {
		expectedValuesByEnv[env.key] = segmentTestData[i].expectedFlagValuesForUser
	}
	success := assert.Eventually(t, func() bool {
		for i, env := range environments {
			latestValues := make(map[string]ldvalue.Value)
			for _, userKey := range segmentTestData[i].allUserKeys {
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
	return success
}

func doBigSegmentsTestWithPreExistingSegment(
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
		segmentTestData := makeBigSegmentTestDataForEnvs(environments)
		for i, env := range environments {
			segmentInfo := segmentTestData[i]
			require.NoError(t, manager.apiHelper.createBigSegment(projectInfo, env, segmentKey,
				segmentInfo.includedUserKeys, segmentInfo.excludedUserKeys, segmentInfo.includedByRuleUserKeys))
		}

		flagKey := flagKeyForProj(projectInfo)
		err = manager.apiHelper.createBooleanFlagThatUsesSegment(flagKey, projectInfo, environments, segmentKey)
		require.NoError(t, err)

		dbParams.withStartedRelay(t, manager, dbContainer, environments, nil, func() {
			verifyEvaluationWithBigSegment(t, manager, projectInfo, environments, flagKey, segmentTestData)
		})
	})
}

func doBigSegmentsTestWithFirstSegmentAddedAfterStartup(
	t *testing.T,
	manager *integrationTestManager,
	dbParams databaseTestParams,
) {
	// This is very similar to doBigSegmentsTestWithPreExistingSegment. The difference is that we start Relay
	// first and *then* create the big segment, so we are verifying that it starts the synchronizer and picks
	// up the big segment data as soon as the SDK stream informs it of the big segment's existence.
	dbParams.withContainer(t, manager, func(dbContainer *docker.Container) {
		projectInfo, environments, err := manager.apiHelper.createProject(2)
		require.NoError(t, err)
		defer manager.apiHelper.deleteProject(projectInfo)

		environments[0].prefix = "prefix1"
		environments[1].prefix = "prefix2"

		segmentKey := "big-segment-key"
		segmentTestData := makeBigSegmentTestDataForEnvs(environments)

		dbParams.withStartedRelay(t, manager, dbContainer, environments, nil, func() {
			for i, env := range environments {
				segmentInfo := segmentTestData[i]
				require.NoError(t, manager.apiHelper.createBigSegment(projectInfo, env, segmentKey,
					segmentInfo.includedUserKeys, segmentInfo.excludedUserKeys, segmentInfo.includedByRuleUserKeys))
			}

			flagKey := flagKeyForProj(projectInfo)
			err := manager.apiHelper.createBooleanFlagThatUsesSegment(flagKey, projectInfo, environments, segmentKey)
			require.NoError(t, err)

			verifyEvaluationWithBigSegment(t, manager, projectInfo, environments, flagKey, segmentTestData)
		})
	})
}

func doBigSegmentsTestWithAnotherSegmentAddedAfterStartup(
	t *testing.T,
	manager *integrationTestManager,
	dbParams databaseTestParams,
) {
	// This is different from doBigSegmentsTestWithFirstSegmentAddedAfterStartup as follows: after we've
	// started Relay, created a big segment, and observed that the big segment synchronizer has started,
	// we create *another* big segment which is the one we're actually testing. This verifies that streaming
	// updates are being received.
	dbParams.withContainer(t, manager, func(dbContainer *docker.Container) {
		projectInfo, environments, err := manager.apiHelper.createProject(2)
		require.NoError(t, err)
		defer manager.apiHelper.deleteProject(projectInfo)

		environments[0].prefix = "prefix1"
		environments[1].prefix = "prefix2"

		segmentKey1 := "big-segment-key1"
		segmentKey2 := "big-segment-key2"
		segmentTestData := makeBigSegmentTestDataForEnvs(environments)

		dbParams.withStartedRelay(t, manager, dbContainer, environments, nil, func() {
			for i, env := range environments {
				segmentInfo := segmentTestData[i]
				require.NoError(t, manager.apiHelper.createBigSegment(projectInfo, env, segmentKey1,
					segmentInfo.includedUserKeys, segmentInfo.excludedUserKeys, segmentInfo.includedByRuleUserKeys))
			}
			flagKey1 := flagKeyForProj(projectInfo) + "1"
			err := manager.apiHelper.createBooleanFlagThatUsesSegment(flagKey1, projectInfo, environments, segmentKey1)
			require.NoError(t, err)

			// As soon as flagKey1 starts working, we know the first segment has been received
			if !verifyEvaluationWithBigSegment(t, manager, projectInfo, environments, flagKey1, segmentTestData) {
				return // unexpected failure
			}

			// Now, create the second segment and a corresponding flag
			for i, env := range environments {
				segmentInfo := segmentTestData[i]
				require.NoError(t, manager.apiHelper.createBigSegment(projectInfo, env, segmentKey2,
					segmentInfo.includedUserKeys, segmentInfo.excludedUserKeys, segmentInfo.includedByRuleUserKeys))
			}
			flagKey2 := flagKeyForProj(projectInfo) + "2"
			err = manager.apiHelper.createBooleanFlagThatUsesSegment(flagKey2, projectInfo, environments, segmentKey2)
			require.NoError(t, err)

			verifyEvaluationWithBigSegment(t, manager, projectInfo, environments, flagKey2, segmentTestData)
		})
	})
}
