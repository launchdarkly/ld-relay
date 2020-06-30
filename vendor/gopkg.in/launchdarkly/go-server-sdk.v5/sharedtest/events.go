package sharedtest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// ExpectFlagChangeEvents asserts that a channel receives flag change events for the specified keys (in
// any order) and then does not receive any more events for the next 100ms.
func ExpectFlagChangeEvents(t *testing.T, ch <-chan interfaces.FlagChangeEvent, keys ...string) {
	expectedChangedFlagKeys := make(map[string]bool)
	for _, key := range keys {
		expectedChangedFlagKeys[key] = true
	}
	actualChangedFlagKeys := make(map[string]bool)
ReadLoop:
	for i := 0; i < len(keys); i++ {
		select {
		case event, ok := <-ch:
			if !ok {
				break ReadLoop
			}
			actualChangedFlagKeys[event.Key] = true
		case <-time.After(time.Second):
			assert.Fail(t, "did not receive expected event", "expected: %v, received: %v",
				expectedChangedFlagKeys, actualChangedFlagKeys)
			return
		}
	}
	assert.Equal(t, expectedChangedFlagKeys, actualChangedFlagKeys)
	ExpectNoMoreFlagChangeEvents(t, ch)
}

// ExpectNoMoreFlagChangeEvents asserts that a channel does not receive any flag change events for the
// next 100ms.
func ExpectNoMoreFlagChangeEvents(t *testing.T, ch <-chan interfaces.FlagChangeEvent) {
	select {
	case event, ok := <-ch:
		if !ok {
			return
		}
		assert.Fail(t, "received unexpected event", "event: %+v", event)
	case <-time.After(100 * time.Millisecond):
	}
}

// ExpectNoMoreFlagValueChangeEvents asserts that a channel does not receive any flag value change events
// for the next 100ms.
func ExpectNoMoreFlagValueChangeEvents(t *testing.T, ch <-chan interfaces.FlagValueChangeEvent) {
	select {
	case event, ok := <-ch:
		if !ok {
			return
		}
		assert.Fail(t, "received unexpected event", "event: %+v", event)
	case <-time.After(100 * time.Millisecond):
	}
}
