package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"

	"net/http/httputil"

	"github.com/gorilla/mux"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/events"
	"gopkg.in/launchdarkly/ld-relay.v5/internal/util"
)

type contextKeyType string

const contextKey contextKeyType = "context"

type clientSideContext struct {
	clientContext
	allowedOrigins []string
	proxy          *httputil.ReverseProxy
}

func (c *clientSideContext) AllowedOrigins() []string {
	return c.allowedOrigins
}

type clientSideMux struct {
	contextByKey map[string]*clientSideContext
}

func (m clientSideMux) selectClientByUrlParam(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		envId := mux.Vars(req)["envId"]
		clientCtx := m.contextByKey[envId]
		if clientCtx == nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("ld-relay is not configured for environment id " + envId))
			return
		}

		if clientCtx.getClient() == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("client was not initialized"))
			return
		}

		req = req.WithContext(context.WithValue(req.Context(), contextKey, clientCtx))
		next.ServeHTTP(w, req)
	})
}

func (m clientSideMux) getGoals(w http.ResponseWriter, req *http.Request) {
	clientCtx := getClientContext(req).(*clientSideContext)
	clientCtx.proxy.ServeHTTP(w, req)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var domains []string
		if context, ok := r.Context().Value(contextKey).(corsContext); ok {
			domains = context.AllowedOrigins()
		}
		if len(domains) > 0 {
			for _, d := range domains {
				if r.Header.Get("Origin") == d {
					setCorsHeaders(w, d)
					return
				}
			}
			// Not a valid origin, set allowed origin to any allowed origin
			setCorsHeaders(w, domains[0])
		} else {
			origin := defaultAllowedOrigin
			if r.Header.Get("Origin") != "" {
				origin = r.Header.Get("Origin")
			}
			setCorsHeaders(w, origin)
		}
		next.ServeHTTP(w, r)
	})
}

var allowedHeadersList = []string{
	"Content-Type",
	"Content-Length",
	"Accept-Encoding",
	"X-LaunchDarkly-User-Agent",
	events.EventSchemaHeader,
}

var allowedHeaders = strings.Join(allowedHeadersList, ",")

func setCorsHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "false")
	w.Header().Set("Access-Control-Max-Age", "300")
	w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
	w.Header().Set("Access-Control-Expose-Headers", "Date")
}

const transparent1PixelImgBase64 = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7="

var transparent1PixelImg []byte

func init() {
	transparent1PixelImg, _ = base64.StdEncoding.DecodeString(transparent1PixelImgBase64)
}

func getEventsImage(w http.ResponseWriter, req *http.Request) {
	clientCtx := getClientContext(req)

	if clientCtx.getHandlers().eventDispatcher == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(util.ErrorJsonMsg("Event proxy is not enabled for this environment"))
		return
	}
	handler := clientCtx.getHandlers().eventDispatcher.GetHandler(events.JavaScriptSDKEventsEndpoint)
	if handler == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(util.ErrorJsonMsg("Event proxy for browser clients is not enabled for this environment"))
		return
	}

	d := req.URL.Query().Get("d")
	if d != "" {
		go func() {
			nullW := httptest.NewRecorder()
			events, _ := base64.StdEncoding.DecodeString(d)
			eventsReq, _ := http.NewRequest("POST", "", bytes.NewBuffer(events))
			eventsReq.Header.Add("Content-Type", "application/json")
			eventsReq.Header.Add("X-LaunchDarkly-User-Agent", eventsReq.Header.Get("X-LaunchDarkly-User-Agent"))
			handler(nullW, eventsReq)
		}()
	}

	w.Header().Set("Content-Type", "image/gif")
	w.Write(transparent1PixelImg)
}
