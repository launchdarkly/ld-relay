package relay

import (
	"fmt"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v8/config"
	st "github.com/launchdarkly/ld-relay/v8/internal/sharedtest"

	m "github.com/launchdarkly/go-test-helpers/v3/matchers"

	"github.com/stretchr/testify/assert"
)

func TestEndpointsPHPPolling(t *testing.T) {
	sdkKeyMain := st.EnvMain.Config.SDKKey
	sdkKeyWithTTL := st.EnvWithTTL.Config.SDKKey

	specs := []endpointTestParams{
		{"get flag", "GET", fmt.Sprintf("/sdk/flags/%s", st.Flag1ServerSide.Flag.Key), nil, sdkKeyMain,
			http.StatusOK, st.ExpectJSONEntity(st.Flag1ServerSide.Flag)},
		{"get unknown flag", "GET", "/sdk/flags/no-such-flag", nil, sdkKeyMain,
			http.StatusNotFound, st.ExpectNoBody()},
		{"get all flags", "GET", "/sdk/flags", nil, sdkKeyMain,
			http.StatusOK, st.ExpectJSONEntity(st.FlagsMap(st.AllFlags))},
		{"get segment", "GET", fmt.Sprintf("/sdk/segments/%s", st.Segment1.Key), nil, sdkKeyMain,
			http.StatusOK, st.ExpectJSONEntity(st.Segment1)},
		{"get unknown segment", "GET", "/sdk/segments/no-such-segment", nil, sdkKeyMain,
			http.StatusNotFound, st.ExpectNoBody()},
	}

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvWithTTL)

	withStartedRelay(t, config, func(p relayTestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				if s.expectedStatus == http.StatusOK {
					etag := ""

					t.Run("success", func(t *testing.T) {
						result, body := st.DoRequest(s.request(), p.relay)

						if assert.Equal(t, s.expectedStatus, result.StatusCode) {
							st.AssertNonStreamingHeaders(t, result.Header)
							m.In(t).Assert(body, s.bodyMatcher)
							etag := result.Header.Get("Etag")
							assert.NotEqual(t, "", etag)
						}
					})

					t.Run("success - environment has TTL", func(t *testing.T) {
						s1 := s
						s1.credential = sdkKeyWithTTL
						result, _ := st.DoRequest(s1.request(), p.relay)

						if assert.Equal(t, s.expectedStatus, result.StatusCode) {
							assert.NotEqual(t, "", result.Header.Get("Expires"))
						}
					})

					if etag != "" {
						t.Run("query with same ETag is cached", func(t *testing.T) {
							r := s.request()
							r.Header.Set("If-None-Match", etag)
							result, _ := st.DoRequest(r, p.relay)

							assert.Equal(t, http.StatusNotModified, result.StatusCode)
						})

						t.Run("query with different ETag is cached", func(t *testing.T) {
							r := s.request()
							r.Header.Set("If-None-Match", "different-from-"+etag)
							result, _ := st.DoRequest(r, p.relay)

							assert.Equal(t, http.StatusOK, result.StatusCode)
						})
					}
				}
			})
		}
	})
}
