package relay

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/debug"
	"sync"
	"testing"
	"time"

	c "github.com/launchdarkly/ld-relay/v9/config"

	"github.com/launchdarkly/go-configtypes"
	"github.com/launchdarkly/go-sdk-common/v3/ldlog"
	"github.com/launchdarkly/go-test-helpers/v3/httphelpers"

	"github.com/klauspost/compress/gzhttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRelayRejectsConfigWithNoEnvironmentsInManualConfigMode(t *testing.T) {
	config := c.Config{}
	relay, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
	assert.Equal(t, errNoEnvironments, err)
	assert.Nil(t, relay)
}

func TestNewRelayAllowsConfigWithNoEnvironmentsIfAutoConfigKeyIsSet(t *testing.T) {
	stubStreamHandler, stream := httphelpers.SSEHandler(nil)
	defer stream.Close()
	httphelpers.WithServer(stubStreamHandler, func(server *httptest.Server) {
		streamURI, _ := configtypes.NewOptURLAbsoluteFromString(server.URL)
		config := c.Config{
			Main: c.MainConfig{
				StreamURI: streamURI,
			},
			AutoConfig: c.AutoConfigConfig{
				Key: "x",
			},
		}
		relay, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
		require.NoError(t, err)
		defer relay.Close()
	})
}

func TestNewRelayAllowsConfigWithNoEnvironmentsIfFileDataSourceIsSet(t *testing.T) {
	config := c.Config{
		OfflineMode: c.OfflineModeConfig{
			FileDataSource: "x",
		},
	}
	_, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)

	// There will be an error, since we don't actually have a data file, but it should not be a
	// configuration error.
	require.Error(t, err)
	assert.NotEqual(t, errNoEnvironments, err)
}

func TestNewRelayDisallowsFiltersWhenNoEnvironmentsSpecified(t *testing.T) {
	config := c.Config{
		Filters: map[string]*c.FiltersConfig{
			"proj": {
				Keys: configtypes.NewOptStringList([]string{"foo"}),
			},
		},
	}
	_, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
}

func TestNewRelayDisallowsFiltersWhenProjKeyNotSpecified(t *testing.T) {
	config := c.Config{
		Environment: map[string]*c.EnvConfig{
			"a": {
				SDKKey:  "123",
				ProjKey: "proj",
			},
			"b": {
				SDKKey: "234",
				// missing project key
			},
		},
		Filters: map[string]*c.FiltersConfig{
			"proj": {
				Keys: configtypes.NewOptStringList([]string{"foo"}),
			},
		},
	}
	_, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
}

func TestNewRelayDisallowsFiltersWithUnmatchedProjects(t *testing.T) {
	config := c.Config{
		Environment: map[string]*c.EnvConfig{
			"a": {
				SDKKey:  "123",
				ProjKey: "proj",
			},
		},
		Filters: map[string]*c.FiltersConfig{
			"notProj": {
				Keys: configtypes.NewOptStringList([]string{"foo"}),
			},
		},
	}
	_, err := NewRelay(config, ldlog.NewDisabledLoggers(), nil)
	require.Error(t, err)
}

func TestMakeFilteredEnvironments_NoFilters(t *testing.T) {
	cfg := &c.Config{Environment: map[string]*c.EnvConfig{
		"a": {
			SDKKey: "123",
		},
		"b": {
			SDKKey: "234",
		},
	}}
	envs := makeFilteredEnvironments(cfg)
	for _, id := range []string{"a", "b"} {
		require.Contains(t, envs, id)
	}
}

func TestMakeFilteredEnvironments_OneFilter_OneEnvironment(t *testing.T) {
	cfg := &c.Config{
		Environment: map[string]*c.EnvConfig{
			"a": {
				SDKKey:  "123",
				ProjKey: "proj",
			},
		},
		Filters: map[string]*c.FiltersConfig{
			"proj": {Keys: configtypes.NewOptStringList([]string{"foo", "bar"})},
		},
	}
	envs := makeFilteredEnvironments(cfg)
	for _, id := range []string{"a", "a/foo", "a/bar"} {
		require.Contains(t, envs, id)
	}
}

func TestMakeFilteredEnvironments_ManyFilters_ManyEnvironments(t *testing.T) {
	cfg := &c.Config{
		Environment: map[string]*c.EnvConfig{
			"a": {
				SDKKey:  "123",
				ProjKey: "projA",
			},
			"b": {
				SDKKey:  "123",
				ProjKey: "projA",
			},
			"c": {
				SDKKey:  "123",
				ProjKey: "projB",
			},
		},
		Filters: map[string]*c.FiltersConfig{
			"projA": {Keys: configtypes.NewOptStringList([]string{"foo", "bar"})},
			"projB": {Keys: configtypes.NewOptStringList([]string{"baz"})},
		},
	}
	envs := makeFilteredEnvironments(cfg)
	for _, id := range []string{"a", "b", "c", "a/foo", "a/bar", "b/foo", "b/bar", "c/baz"} {
		assert.Contains(t, envs, id)
	}
}

func TestCompressionIsAppliedWhenEnabled(t *testing.T) {
	// Test with compression enabled
	configWithCompression := c.Config{
		HTTP: c.HTTPConfig{
			EnableCompression: true,
		},
		Environment: map[string]*c.EnvConfig{
			"test": {
				SDKKey: "test-key",
			},
		},
	}

	withStartedRelay(t, configWithCompression, func(p relayTestParams) {
		// Create a request to the flags endpoint which returns more data
		req, _ := http.NewRequest("GET", "/sdk/flags", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Authorization", "test-key")

		w := httptest.NewRecorder()
		p.relay.ServeHTTP(w, req)

		// Verify the response is compressed
		assert.Equal(t, http.StatusOK, w.Result().StatusCode)
		assert.Equal(t, "gzip", w.Header().Get("Content-Encoding"))

		// Verify the content is actually compressed
		body := w.Body.Bytes()
		assert.Greater(t, len(body), 0, "Response body should not be empty")

		// Try to decompress the body to verify it's actually gzipped
		reader, err := gzip.NewReader(io.NopCloser(bytes.NewReader(body)))
		require.NoError(t, err, "Response should be valid gzip content")
		defer reader.Close()

		decompressed, err := io.ReadAll(reader)
		assert.NoError(t, err, "Should be able to read decompressed content")
		assert.Greater(t, len(decompressed), 0, "Decompressed content should not be empty")

		// Verify the decompressed content is substantial enough to test compression
		assert.Greater(t, len(decompressed), gzhttp.DefaultMinSize, fmt.Sprintf("Decompressed content should be larger than %d bytes to properly test compression", gzhttp.DefaultMinSize))
	})
}

func TestCompressionIsNotAppliedWhenDisabled(t *testing.T) {
	// Test with compression disabled
	configWithoutCompression := c.Config{
		HTTP: c.HTTPConfig{
			EnableCompression: false,
		},
		Environment: map[string]*c.EnvConfig{
			"test": {
				SDKKey: "test-key",
			},
		},
	}

	withStartedRelay(t, configWithoutCompression, func(p relayTestParams) {
		// Create a request to the flags endpoint which returns more data
		req, _ := http.NewRequest("GET", "/sdk/flags", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Authorization", "test-key")

		w := httptest.NewRecorder()
		p.relay.ServeHTTP(w, req)

		// Verify the response is not compressed
		assert.Equal(t, http.StatusOK, w.Result().StatusCode)
		assert.Equal(t, "", w.Header().Get("Content-Encoding"))

		// Verify the content is not compressed
		body := w.Body.Bytes()
		assert.Greater(t, len(body), 0, "Response body should not be empty")

		// Verify the uncompressed content is substantial enough
		assert.Greater(t, len(body), gzhttp.DefaultMinSize, fmt.Sprintf("Uncompressed content should be larger than %d bytes", gzhttp.DefaultMinSize))

		t.Logf("Body size: %d bytes", len(body))
		// Try to decompress the body - this should fail since it's not compressed
		_, err := gzip.NewReader(io.NopCloser(bytes.NewReader(body)))
		assert.Error(t, err, "Response should not be gzip content when compression is disabled")
	})
}

func TestLoadCompression(t *testing.T) {
	// Set minimal memory limit for the test
	originalLimit := debug.SetMemoryLimit(10 * 1024 * 1024) // 10MB limit
	defer debug.SetMemoryLimit(originalLimit)

	// Force garbage collection before starting
	runtime.GC()

	// Test with compression enabled
	configWithCompression := c.Config{
		HTTP: c.HTTPConfig{
			EnableCompression: true,
		},
		Environment: map[string]*c.EnvConfig{
			"test": {
				SDKKey: "test-key",
			},
		},
	}

	withStartedRelay(t, configWithCompression, func(p relayTestParams) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		var requestCount int
		var lastError error
		var failed bool
		var maxAlloc uint64

		// Start with a reasonable number of concurrent requests
		concurrency := 100
		maxRequests := 1000

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				for {
					mu.Lock()
					if failed || requestCount >= maxRequests {
						mu.Unlock()
						return
					}
					currentCount := requestCount
					requestCount++
					mu.Unlock()

					// Create a request to the flags endpoint
					req, err := http.NewRequest("GET", "/sdk/flags", nil)
					if err != nil {
						mu.Lock()
						lastError = err
						failed = true
						mu.Unlock()
						return
					}

					req.Header.Set("Accept-Encoding", "gzip")
					req.Header.Set("Authorization", "test-key")

					w := httptest.NewRecorder()
					p.relay.ServeHTTP(w, req)

					// Check if the request succeeded
					if w.Result().StatusCode != http.StatusOK {
						mu.Lock()
						lastError = fmt.Errorf("request failed with status: %d", w.Result().StatusCode)
						failed = true
						mu.Unlock()
						return
					}

					// Verify compression is working
					if w.Header().Get("Content-Encoding") != "gzip" {
						mu.Lock()
						lastError = fmt.Errorf("compression not applied at request %d", currentCount)
						failed = true
						mu.Unlock()
						return
					}

					// Check memory usage
					var m runtime.MemStats
					runtime.ReadMemStats(&m)

					// Track maximum memory usage
					mu.Lock()
					if m.Alloc > maxAlloc {
						maxAlloc = m.Alloc
					}
					mu.Unlock()

					// Small delay to prevent overwhelming the system
					time.Sleep(1 * time.Millisecond)
				}
			}()
		}

		wg.Wait()

		// Report results
		t.Logf("Load test completed: %d requests processed", requestCount)

		if failed {
			t.Fatalf("Test failed: %v", lastError)
		}

		t.Logf("Test completed successfully with %d requests", requestCount)
		assert.Greater(t, requestCount, 0, "Should have processed requests successfully")

		// Final memory stats
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		t.Logf("Final memory usage: Alloc=%d MB, TotalAlloc=%d MB, NumGC=%d",
			m.Alloc/1024/1024, m.TotalAlloc/1024/1024, m.NumGC)
		t.Logf("Peak memory usage: MaxAlloc=%d MB",
			maxAlloc/1024/1024)
	})
}
