package relay

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	c "github.com/launchdarkly/ld-relay/v6/config"
)

func TestRelayPHPPollingEndpoints(t *testing.T) {
	sdkKeyMain := testEnvMain.config.SDKKey
	sdkKeyWithTTL := testEnvWithTTL.config.SDKKey

	specs := []endpointTestParams{
		{"get flag", "GET", fmt.Sprintf("/sdk/flags/%s", flag1ServerSide.flag.Key), nil, sdkKeyMain,
			http.StatusOK, expectJSONEntity(flag1ServerSide.flag)},
		{"get unknown flag", "GET", "/sdk/flags/no-such-flag", nil, sdkKeyMain,
			http.StatusNotFound, nil},
		{"get all flags", "GET", "/sdk/flags", nil, sdkKeyMain,
			http.StatusOK, expectJSONEntity(flagsMap(allFlags))},
		{"get segment", "GET", fmt.Sprintf("/sdk/segments/%s", segment1.Key), nil, sdkKeyMain,
			http.StatusOK, expectJSONEntity(segment1)},
		{"get unknown segment", "GET", "/sdk/segments/no-such-segment", nil, sdkKeyMain,
			http.StatusNotFound, nil},
	}

	config := c.DefaultConfig
	config.Environment = makeEnvConfigs(testEnvMain, testEnvWithTTL)

	relayTest(config, func(p relayTestParams) {
		for _, s := range specs {
			t.Run(s.name, func(t *testing.T) {
				if s.expectedStatus == http.StatusOK {
					etag := ""

					t.Run("success", func(t *testing.T) {
						result, body := doRequest(s.request(), p.relay)

						if assert.Equal(t, s.expectedStatus, result.StatusCode) {
							assertNonStreamingHeaders(t, result.Header)
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
						result, _ := doRequest(s1.request(), p.relay)

						if assert.Equal(t, s.expectedStatus, result.StatusCode) {
							assert.NotEqual(t, "", result.Header.Get("Expires"))
						}
					})

					if etag != "" {
						t.Run("query with same ETag is cached", func(t *testing.T) {
							r := s.request()
							r.Header.Set("If-None-Match", etag)
							result, _ := doRequest(r, p.relay)

							assert.Equal(t, http.StatusNotModified, result.StatusCode)
						})

						t.Run("query with different ETag is cached", func(t *testing.T) {
							r := s.request()
							r.Header.Set("If-None-Match", "different-from-"+etag)
							result, _ := doRequest(r, p.relay)

							assert.Equal(t, http.StatusOK, result.StatusCode)
						})
					}
				}
			})
		}
	})
}
