package main

import (
	"net/http"

	"github.com/launchdarkly/go-test-helpers/v3/ldservices"
)

func streamerEndpointHandler() http.Handler {
	initialData := ldservices.NewServerSDKData()
	handler, _ := ldservices.ServerSideStreamingServiceHandler(initialData.ToPutEvent())
	return handler
}
