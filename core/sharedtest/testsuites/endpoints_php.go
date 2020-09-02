package testsuites

import (
	"fmt"
	"net/http"
	"testing"

	c "github.com/launchdarkly/ld-relay/v6/core/config"
	st "github.com/launchdarkly/ld-relay/v6/core/sharedtest"

	"github.com/stretchr/testify/assert"
)

func DoPHPPollingEndpointsTests(t *testing.T, constructor TestConstructor) {
	sdkKeyMain := st.EnvMain.Config.SDKKey
	sdkKeyWithTTL := st.EnvWithTTL.Config.SDKKey

	specs := []endpointTestParams{
		{"get flag", "GET", fmt.Sprintf("/sdk/flags/%s", st.Flag1ServerSide.Flag.Key), nil, sdkKeyMain,
			http.StatusOK, st.ExpectJSONEntity(st.Flag1ServerSide.Flag)},
		{"get unknown flag", "GET", "/sdk/flags/no-such-flag", nil, sdkKeyMain,
			http.StatusNotFound, nil},
		{"get all flags", "GET", "/sdk/flags", nil, sdkKeyMain,
			http.StatusOK, st.ExpectJSONEntity(st.FlagsMap(st.AllFlags))},
		{"get segment", "GET", fmt.Sprintf("/sdk/segments/%s", st.Segment1.Key), nil, sdkKeyMain,
			http.StatusOK, st.ExpectJSONEntity(st.Segment1)},
		{"get unknown segment", "GET", "/sdk/segments/no-such-segment", nil, sdkKeyMain,
			http.StatusNotFound, nil},
	}

	var config c.Config
	config.Environment = st.MakeEnvConfigs(st.EnvMain, st.EnvWithTTL)

	DoTest(config, constructor, func(p TestParams) {
		for _, spec := range specs {
			s := spec
			t.Run(s.name, func(t *testing.T) {
				if s.expectedStatus == http.StatusOK {
					etag := ""

					t.Run("success", func(t *testing.T) {
						result, body := st.DoRequest(s.request(), p.Handler)

						if assert.Equal(t, s.expectedStatus, result.StatusCode) {
							st.AssertNonStreamingHeaders(t, result.Header)
							if s.bodyMatcher != nil {
								s.bodyMatcher(t, body)
							}
							etag := result.Header.Get("Etag")
							assert.NotEqual(t, "", etag)
						}
					})

					t.Run("success - environment has TTL", func(t *testing.T) {
						s1 := s
						s1.credential = sdkKeyWithTTL
						result, _ := st.DoRequest(s1.request(), p.Handler)

						if assert.Equal(t, s.expectedStatus, result.StatusCode) {
							assert.NotEqual(t, "", result.Header.Get("Expires"))
						}
					})

					if etag != "" {
						t.Run("query with same ETag is cached", func(t *testing.T) {
							r := s.request()
							r.Header.Set("If-None-Match", etag)
							result, _ := st.DoRequest(r, p.Handler)

							assert.Equal(t, http.StatusNotModified, result.StatusCode)
						})

						t.Run("query with different ETag is cached", func(t *testing.T) {
							r := s.request()
							r.Header.Set("If-None-Match", "different-from-"+etag)
							result, _ := st.DoRequest(r, p.Handler)

							assert.Equal(t, http.StatusOK, result.StatusCode)
						})
					}
				}
			})
		}
	})
}
