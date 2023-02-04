package relay

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"

	"github.com/launchdarkly/ld-relay/v7/internal/basictypes"
	"github.com/launchdarkly/ld-relay/v7/internal/browser"
	"github.com/launchdarkly/ld-relay/v7/internal/events"
	"github.com/launchdarkly/ld-relay/v7/internal/middleware"
	"github.com/launchdarkly/ld-relay/v7/internal/util"

	ldevents "github.com/launchdarkly/go-sdk-events/v2"
)

func getEventsImage(w http.ResponseWriter, req *http.Request) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())

	if clientCtx.Env.GetEventDispatcher() == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(util.ErrorJSONMsg("Event proxy is not enabled for this environment"))
		return
	}
	handler := clientCtx.Env.GetEventDispatcher().GetHandler(basictypes.JSClientSDK, ldevents.AnalyticsEventDataKind)
	if handler == nil { // COVERAGE: abnormal condition that can't be caused in unit tests
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(util.ErrorJSONMsg("Event proxy for browser clients is not enabled for this environment"))
		return
	}

	d := req.URL.Query().Get("d")
	if d != "" {
		go func() {
			nullW := httptest.NewRecorder()
			eventData, _ := base64.StdEncoding.DecodeString(d)
			eventsReq, _ := http.NewRequest("POST", "", bytes.NewBuffer(eventData))
			eventsReq.Header.Add("Content-Type", "application/json")
			eventsReq.Header.Add("X-LaunchDarkly-User-Agent", eventsReq.Header.Get("X-LaunchDarkly-User-Agent"))
			eventsReq.Header.Add(events.EventSchemaHeader, strconv.Itoa(events.SummaryEventsSchemaVersion))
			handler(nullW, eventsReq)
		}()
	}

	w.Header().Set("Content-Type", "image/gif")
	_, _ = w.Write(browser.Transparent1PixelImageData)
}

func getGoals(w http.ResponseWriter, req *http.Request) {
	clientCtx := middleware.GetEnvContextInfo(req.Context())
	clientCtx.Env.GetJSClientContext().Proxy.ServeHTTP(w, req)
}
