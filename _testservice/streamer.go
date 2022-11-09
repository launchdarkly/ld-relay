package main

import (
	"net/http"

	"github.com/launchdarkly/go-server-sdk/v6/testhelpers/ldservices"
)

func streamerEndpointHandler() http.Handler {
	initialData := ldservices.NewServerSDKData()
	handler, _ := ldservices.ServerSideStreamingServiceHandler(initialData.ToPutEvent())
	return handler
}
