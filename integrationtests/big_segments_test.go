//go:build integrationtests
// +build integrationtests

package integrationtests

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v6/integrationtests/docker"
	"github.com/launchdarkly/ld-relay/v6/internal/core"

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
	explanationForUser        map[string]string
}

var enableBigSegmentsTraceLoggingVar = map[string]string{
	"LD_TRACE_LOG_BIG_SEGMENTS": "true",
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
		info.explanationForUser = map[string]string{
			userKey1: "this user should be included in the big segment and not excluded;" +
				" a false value indicates that we did not receive the patch that included the user",
			userKey2: "this user should be included in a segment rule and not excluded;" +
				" a false value indicates that we did not receive the patch that included the user," +
				" and also did not receive the regular SDK data with the segment rule",
			userKey3: "this user should be included in a segment rule but excluded from the big segment;" +
				" a true value indicates that we did not receive the patch that excluded the user",
		}
		ret = append(ret, info)
	}
	return ret
}

func testBigSegments(t *testing.T, manager *integrationTestManager) {
	doAll := func(t *testing.T, dbParams databaseTestParams) {
		t.Run("big segment exists before Relay is started", func(t *testing.T) {
			doBigSegmentsTestWithPreExistingSegment(t, manager, dbParams)
		})
		t.Run("first big segment is created after Relay is started", func(t *testing.T) {
			doBigSegmentsTestWithFirstSegmentAddedAfterStartup(t, manager, dbParams)
		})
		t.Run("another big segment is created after synchronizer has started", func(t *testing.T) {
			doBigSegmentsTestWithAnotherSegmentAddedAfterStartup(t, manager, dbParams)
		})
	}
	t.Run("Redis", func(t *testing.T) {
		doAll(t, redisDatabaseTestParams)
	})
	t.Run("Redis with password", func(t *testing.T) {
		// Here we're doing just the basic test, not the whole big segments test suite, because
		// we've already tested the Redis integration for big segments in general above; the point
		// of this part is just to make sure connecting with a password also works
		doBigSegmentsTestWithPreExistingSegment(t, manager, redisWithPasswordDatabaseTestParams)
	})
	t.Run("DynamoDB", func(t *testing.T) {
		doAll(t, dynamoDBDatabaseTestParams)
	})
}

func verifyEvaluationWithBigSegment(
	t *testing.T,
	manager *integrationTestManager,
	projectInfo projectInfo,
	environments []environmentInfo,
	flagKey, segmentKey string,
	segmentTestData []bigSegmentTestData,
) bool {
	testDataByEnv := make(map[string]bigSegmentTestData)
	latestValuesByEnv := make(map[string]map[string]ldvalue.Value)
	expectedValuesByEnv := make(map[string]map[string]ldvalue.Value)
	envsByKey := make(map[string]environmentInfo)
	for i, env := range environments {
		testDataByEnv[env.key] = segmentTestData[i]
		expectedValuesByEnv[env.key] = segmentTestData[i].expectedFlagValuesForUser
		envsByKey[env.key] = env
	}

	// Poll the evaluation endpoint until we see the expected flag values. We're using a
	// longer timeout here than we use in tests that don't involve big segments, because
	// the user segment state caching inside the SDK makes it hard to say how soon we'll
	// see the effect of an update.
	success := assert.Eventually(t, func() bool {
		ok := true
		for i, env := range environments {
			latestValues := make(map[string]ldvalue.Value)
			for _, userKey := range segmentTestData[i].allUserKeys {
				userJSON := fmt.Sprintf(`{"key":"%s"}`, userKey)
				latestValues[userKey] = manager.getFlagValues(t, projectInfo, env, userJSON).GetByKey(flagKey)
			}
			latestValuesByEnv[env.key] = latestValues
			if !reflect.DeepEqual(latestValues, testDataByEnv[env.key].expectedFlagValuesForUser) {
				ok = false
			}
		}
		return ok
	}, time.Second*20, time.Millisecond*100, "Did not see expected flag values from Relay")

	if !success {
		manager.loggers.Infof("EXPLANATION OF TEST FAILURE FOLLOWS:")
		manager.loggers.Infof("I evaluated the flag %q, which tests membership in the segment %q, for multiple users in each test environment...",
			flagKey, segmentKey)
		for envKey, testData := range testDataByEnv {
			actualValues := latestValuesByEnv[envKey]
			expectedValues := testData.expectedFlagValuesForUser
			if !reflect.DeepEqual(actualValues, expectedValues) {
				env := envsByKey[envKey]
				manager.loggers.Infof("Got wrong results for the following users in environment ID %q (SDK key %q, shown as [env: ...%s] in logs):",
					env.id, env.sdkKey, env.sdkKey[len(env.sdkKey)-4:])
				for userKey, expectedValue := range expectedValues {
					actualValue := actualValues[userKey]
					if !actualValue.Equal(expectedValue) {
						userHash := hashForUserKey(userKey)
						manager.loggers.Infof("--- User %q (hash value %q): expected value %s, actual value %s. Explanation: %s",
							userKey, userHash, expectedValue, actualValue, testData.explanationForUser[userKey])
					}
				}
			}
		}
		return false
	}

	// If the evaluation succeeded, then Big Segments polling must have successfully started by now,
	// so we'll also verify that Relay is now reporting that status correctly.
	if status, ok := manager.awaitRelayStatus(t,
		func(status core.StatusRep) bool {
			for _, envRep := range status.Environments {
				if envRep.BigSegmentStatus == nil ||
					!envRep.BigSegmentStatus.Available || envRep.BigSegmentStatus.PotentiallyStale {
					return false
				}
			}
			return true
		}); !ok {
		jsonStatus, _ := json.Marshal(status)
		assert.Fail(t, "Did not see expected valid big segments status", "Last status received was: %s",
			string(jsonStatus))
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

		dbParams.withStartedRelay(t, manager, dbContainer, environments, enableBigSegmentsTraceLoggingVar, func(status core.StatusRep) {
			// Since a Big Segment already existed when we started the SDK client, Relay should be aware
			// that Big Segments are active and should be including Big Segment status info in the status.
			// It might or might not be reporting the status as valid yet, depending on whether a poll
			// has happened.
			for _, e := range status.Environments {
				assert.NotNil(t, e.BigSegmentStatus)
			}
			verifyEvaluationWithBigSegment(t, manager, projectInfo, environments, flagKey, segmentKey, segmentTestData)
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

		dbParams.withStartedRelay(t, manager, dbContainer, environments, enableBigSegmentsTraceLoggingVar, func(core.StatusRep) {
			for i, env := range environments {
				segmentInfo := segmentTestData[i]
				require.NoError(t, manager.apiHelper.createBigSegment(projectInfo, env, segmentKey,
					segmentInfo.includedUserKeys, segmentInfo.excludedUserKeys, segmentInfo.includedByRuleUserKeys))
			}

			flagKey := flagKeyForProj(projectInfo)
			err := manager.apiHelper.createBooleanFlagThatUsesSegment(flagKey, projectInfo, environments, segmentKey)
			require.NoError(t, err)

			verifyEvaluationWithBigSegment(t, manager, projectInfo, environments, flagKey, segmentKey, segmentTestData)
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

		dbParams.withStartedRelay(t, manager, dbContainer, environments, enableBigSegmentsTraceLoggingVar, func(core.StatusRep) {
			for i, env := range environments {
				segmentInfo := segmentTestData[i]
				require.NoError(t, manager.apiHelper.createBigSegment(projectInfo, env, segmentKey1,
					segmentInfo.includedUserKeys, segmentInfo.excludedUserKeys, segmentInfo.includedByRuleUserKeys))
			}
			flagKey1 := flagKeyForProj(projectInfo) + "1"
			err := manager.apiHelper.createBooleanFlagThatUsesSegment(flagKey1, projectInfo, environments, segmentKey1)
			require.NoError(t, err)

			// As soon as flagKey1 starts working, we know the first segment has been received
			if !verifyEvaluationWithBigSegment(t, manager, projectInfo, environments, flagKey1, segmentKey1, segmentTestData) {
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

			verifyEvaluationWithBigSegment(t, manager, projectInfo, environments, flagKey2, segmentKey2, segmentTestData)
		})
	})
}

// This logic is duplicated from the Go SDK because the implementation is not exported
func hashForUserKey(key string) string {
	hashBytes := sha256.Sum256([]byte(key))
	return base64.StdEncoding.EncodeToString(hashBytes[:])
}
