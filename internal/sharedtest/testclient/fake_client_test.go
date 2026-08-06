package testclient

import (
	"sync"
	"testing"

	"github.com/launchdarkly/ld-relay/v8/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeLDClientCloseIsIdempotent(t *testing.T) {
	c := &FakeLDClient{Key: config.SDKKey("key"), CloseCh: make(chan struct{})}

	require.NoError(t, c.Close())
	require.NoError(t, c.Close())

	select {
	case <-c.CloseCh:
	default:
		assert.Fail(t, "CloseCh was not closed")
	}
}

func TestFakeLDClientCloseIsSafeForConcurrentCallers(t *testing.T) {
	c := &FakeLDClient{Key: config.SDKKey("key"), CloseCh: make(chan struct{})}

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			assert.NoError(t, c.Close())
		})
	}
	wg.Wait()

	select {
	case <-c.CloseCh:
	default:
		assert.Fail(t, "CloseCh was not closed")
	}
}
