package filedata

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-sdk-common/v3/ldlogtest"
	helpers "github.com/launchdarkly/go-test-helpers/v3"

	"github.com/stretchr/testify/assert"
)

const (
	testRetryInterval = time.Millisecond * 100
)

type archiveManagerTestParams struct {
	t                   *testing.T
	filePath            string
	archiveManager      *ArchiveManager
	archiveManagerError error
	messageHandler      *testMessageHandler
	mockLog             *ldlogtest.MockLog
}

type testMessage struct {
	id     config.EnvironmentID
	add    *ArchiveEnvironment
	update *ArchiveEnvironment
	failed *envFailedMessage
	delete *config.EnvironmentID
}

type envFailedMessage struct {
	envID config.EnvironmentID
	err   error
}

func archiveManagerTest(t *testing.T, setupFile func(filePath string), action func(p archiveManagerTestParams)) {
	helpers.WithTempFile(func(filePath string) {
		_ = os.Remove(filePath) // used WithTempFile to generate a path, but don't want a file by default
		setupFile(filePath)

		mockLog := ldlogtest.NewMockLog()
		mockLog.Loggers.SetMinLevel(ldlog.Debug)
		defer mockLog.DumpIfTestFailed(t)

		messageHandler := newTestMessageHandler()

		archiveManager, err := NewArchiveManager(
			filePath,
			messageHandler,
			testRetryInterval,
			mockLog.Loggers,
		)
		if archiveManager != nil {
			defer archiveManager.Close()
		}

		params := archiveManagerTestParams{t, filePath, archiveManager, err, messageHandler, mockLog}
		action(params)
	})
}

func (m testMessage) String() string {
	if m.add != nil {
		return fmt.Sprintf("add(%+v)", *m.add)
	}
	if m.update != nil {
		return fmt.Sprintf("update(%+v)", *m.update)
	}
	if m.failed != nil {
		return fmt.Sprintf("failed(%s,%s)", string(m.failed.envID), m.failed.err)
	}
	if m.delete != nil {
		return fmt.Sprintf("delete(%+v)", *m.delete)
	}
	return "???"
}

type testMessageHandler struct {
	received chan testMessage
}

func newTestMessageHandler() *testMessageHandler {
	return &testMessageHandler{
		received: make(chan testMessage, 10),
	}
}

func (h *testMessageHandler) AddEnvironment(params ArchiveEnvironment) {
	h.received <- testMessage{id: params.Params.EnvID, add: &params}
}

func (h *testMessageHandler) UpdateEnvironment(params ArchiveEnvironment) {
	h.received <- testMessage{id: params.Params.EnvID, update: &params}
}

func (h *testMessageHandler) EnvironmentFailed(id config.EnvironmentID, err error) {
	h.received <- testMessage{id: id, failed: &envFailedMessage{id, err}}
}

func (h *testMessageHandler) DeleteEnvironment(id config.EnvironmentID) {
	h.received <- testMessage{id: id, delete: &id}
}

func sortMessages(messages []testMessage) []testMessage {
	ret := make([]testMessage, len(messages))
	copy(ret, messages)
	sort.Slice(ret, func(i, j int) bool { return ret[i].id < ret[j].id })
	return ret
}

func (p archiveManagerTestParams) requireMessage() testMessage {
	return helpers.RequireValue(p.t, p.messageHandler.received, 2*time.Second, "timed out waiting for message")
}

func (p archiveManagerTestParams) requireNoMoreMessages() {
	if !helpers.AssertNoMoreValues(p.t, p.messageHandler.received, 50*time.Millisecond, "received unexpected message") {
		p.t.FailNow()
	}
}

func (p archiveManagerTestParams) expectReloaded() {
	p.mockLog.AssertMessageMatch(p.t, true, ldlog.Warn,
		regexp.QuoteMeta(fmt.Sprintf("Reloaded data from %s", p.filePath)))
}

func (p archiveManagerTestParams) expectEnvironmentsAdded(envs ...testEnv) {
	var messages []testMessage
	for range envs {
		messages = append(messages, p.requireMessage())
	}
	p.requireNoMoreMessages()
	messages = sortMessages(messages)

	for i, te := range sortTestEnvs(envs) {
		p.t.Run(fmt.Sprintf("added environment %d", i+1), func(t *testing.T) {
			msg := messages[i]
			assert.Equal(p.t, te.id(), msg.id)
			assert.NotNil(p.t, msg.add)
			verifyEnvironmentData(t, te, *msg.add)

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info,
				regexp.QuoteMeta(fmt.Sprintf("Added environment %s (%s %s)", te.id(), te.rep.ProjName, te.rep.EnvName)))
		})
	}
}

func (p archiveManagerTestParams) expectEnvironmentsUpdated(envs ...testEnv) {
	var messages []testMessage
	for range envs {
		messages = append(messages, p.requireMessage())
	}
	p.requireNoMoreMessages()
	messages = sortMessages(messages)

	for i, te := range sortTestEnvs(envs) {
		p.t.Run(fmt.Sprintf("updated environment %d", i+1), func(t *testing.T) {
			msg := messages[i]
			assert.Equal(p.t, te.id(), msg.id)
			assert.NotNil(p.t, msg.update)
			verifyEnvironmentData(t, te, *msg.update)

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info,
				fmt.Sprintf(regexp.QuoteMeta("Updated environment %s (%s %s)"), te.id(), te.rep.ProjName, te.rep.EnvName))
		})
	}
}

func (p archiveManagerTestParams) expectEnvironmentsDeleted(ids ...config.EnvironmentID) {
	sortedIDs := make([]config.EnvironmentID, 0, len(ids))
	copy(sortedIDs, ids)
	sort.Slice(sortedIDs, func(i, j int) bool { return sortedIDs[i] < sortedIDs[j] })

	var messages []testMessage
	for range ids {
		messages = append(messages, p.requireMessage())
	}
	p.requireNoMoreMessages()
	messages = sortMessages(messages)

	for i, id := range sortedIDs {
		p.t.Run(fmt.Sprintf("deleted environment %d", i+1), func(t *testing.T) {
			msg := messages[i]
			assert.NotNil(p.t, msg.delete)
			assert.Equal(p.t, id, *msg.delete)

			p.mockLog.AssertMessageMatch(t, true, ldlog.Info,
				fmt.Sprintf(regexp.QuoteMeta("Deleted environment %s (x)"), id))
		})
	}
}
