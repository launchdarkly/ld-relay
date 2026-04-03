package main

import (
	"net/http"

	"github.com/launchdarkly/go-server-sdk/v7/subsystems"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservices"
	"github.com/launchdarkly/go-server-sdk/v7/testhelpers/ldservicesv2"
)

func streamerEndpointHandler() http.Handler {
	initialData := ldservicesv2.NewServerSDKData()
	protocol := ldservicesv2.NewStreamingProtocol().
		WithIntent(subsystems.ServerIntent{Payload: subsystems.Payload{
			ID:     "fake-id",
			Target: 0,
			Code:   subsystems.IntentTransferFull,
			Reason: "payload-missing",
		}}).
		WithPutObjects(initialData.ToPutObjects()).
		WithTransferred("state", 1)
	handler, _ := ldservices.ServerSideStreamingV2ServiceProtocolHandler(protocol)
	return handler
}
