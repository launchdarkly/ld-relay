package relay

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"

	"github.com/launchdarkly/ld-relay/v6/core"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/util"
)

const transparent1PixelImgBase64 = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7="

var transparent1PixelImg []byte

func init() {
	transparent1PixelImg, _ = base64.StdEncoding.DecodeString(transparent1PixelImgBase64)
}

func getEventsImage(w http.ResponseWriter, req *http.Request) {
	clientCtx := core.GetEnvContextInfo(req.Context())

	if clientCtx.Env.GetEventDispatcher() == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(util.ErrorJsonMsg("Event proxy is not enabled for this environment"))
		return
	}
	handler := clientCtx.Env.GetEventDispatcher().GetHandler(events.JavaScriptSDKEventsEndpoint)
	if handler == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(util.ErrorJsonMsg("Event proxy for browser clients is not enabled for this environment"))
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
	w.Write(transparent1PixelImg)
}

func getGoals(w http.ResponseWriter, req *http.Request) {
	clientCtx := core.GetEnvContextInfo(req.Context())
	clientCtx.Env.GetJSClientContext().Proxy.ServeHTTP(w, req)
}
