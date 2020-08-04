package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"

	"net/http/httputil"

	"github.com/launchdarkly/ld-relay/v6/config"
	"github.com/launchdarkly/ld-relay/v6/internal/cors"
	"github.com/launchdarkly/ld-relay/v6/internal/events"
	"github.com/launchdarkly/ld-relay/v6/internal/relayenv"
	"github.com/launchdarkly/ld-relay/v6/internal/util"
)

type contextKeyType string

const contextKey contextKeyType = "context"

type clientSideContext struct {
	relayenv.EnvContext
	allowedOrigins []string
	proxy          *httputil.ReverseProxy
}

func (c *clientSideContext) AllowedOrigins() []string {
	return c.allowedOrigins
}

type clientSideMux struct {
	contextByKey map[config.SDKCredential]*clientSideContext
}

func (m clientSideMux) selectClientByUrlParam(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		envId, err := jsClientSdk.getSDKCredential(req)
		if err != nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("URL did not contain an environment ID"))
			return
		}
		clientCtx := m.contextByKey[envId]
		if clientCtx == nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(fmt.Sprintf("ld-relay is not configured for environment id %s", envId)))
			return
		}

		if clientCtx.GetClient() == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("client was not initialized"))
			return
		}

		reqContext := context.WithValue(req.Context(), contextKey, clientCtx)
		// Even though the clientCtx also serves as a CORSContext, we attach it separately here just to keep
		// the CORS implementation less reliant on other unrelated implementation details
		reqContext = cors.WithCORSContext(reqContext, clientCtx)
		req = req.WithContext(reqContext)
		next.ServeHTTP(w, req)
	})
}

func (m clientSideMux) getGoals(w http.ResponseWriter, req *http.Request) {
	clientCtx := getClientContext(req).(*clientSideContext)
	clientCtx.proxy.ServeHTTP(w, req)
}

const transparent1PixelImgBase64 = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7="

var transparent1PixelImg []byte

func init() {
	transparent1PixelImg, _ = base64.StdEncoding.DecodeString(transparent1PixelImgBase64)
}

func getEventsImage(w http.ResponseWriter, req *http.Request) {
	clientCtx := getClientContext(req)

	if clientCtx.GetHandlers().EventDispatcher == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(util.ErrorJsonMsg("Event proxy is not enabled for this environment"))
		return
	}
	handler := clientCtx.GetHandlers().EventDispatcher.GetHandler(events.JavaScriptSDKEventsEndpoint)
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
