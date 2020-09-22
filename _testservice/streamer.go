package main

import (
	"net/http"

	"github.com/launchdarkly/go-test-helpers/ldservices"
)

func streamerEndpointHandler() http.Handler {
	initialData := ldservices.NewServerSDKData()
	handler, _ := ldservices.ServerSideStreamingServiceHandler(initialData, nil)
	return handler
}
