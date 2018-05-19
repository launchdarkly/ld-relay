package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io/ioutil"
	"net/http"
	"net/http/httptest"

	"github.com/gorilla/mux"
	"github.com/gregjones/httpcache"
)

type clientSideContext struct {
	allowedOrigins []string
	clientContext
}

func (c *clientSideContext) AllowedOrigins() []string {
	return c.allowedOrigins
}

type ClientSideMux struct {
	contextByKey map[string]*clientSideContext
	baseUri      string
}

func (m ClientSideMux) selectClientByUrlParam(next http.Handler) http.Handler {
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

		req = req.WithContext(context.WithValue(req.Context(), "context", clientCtx))
		next.ServeHTTP(w, req)
	})
}

func (m ClientSideMux) getGoals(w http.ResponseWriter, req *http.Request) {
	envId := mux.Vars(req)["envId"]

	ldReq, _ := http.NewRequest("GET", m.baseUri+"/sdk/goals/"+envId, nil)
	ldReq.Header.Set("Authorization", req.Header.Get("Authorization"))

	cachingTransport := httpcache.NewMemoryCacheTransport()
	httpClient := cachingTransport.Client()
	res, err := httpClient.Do(ldReq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write(ErrorJsonMsgf("Error fetching goals: %s", err))
		return
	}

	w.Header().Set("Content-Type", res.Header["Content-Type"][0])

	w.WriteHeader(res.StatusCode)
	bodyBytes, _ := ioutil.ReadAll(res.Body)
	w.Write(bodyBytes)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var domains []string
		if context, ok := r.Context().Value("context").(corsContext); ok {
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

func setCorsHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "false")
	w.Header().Set("Access-Control-Max-Age", "300")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-LaunchDarkly-User-Agent")
	w.Header().Set("Access-Control-Expose-Headers", "Date")
}

const transparent1PixelImgBase64 = "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7="

var transparent1PixelImg []byte

func init() {
	transparent1PixelImg, _ = base64.StdEncoding.DecodeString(transparent1PixelImgBase64)
}

func getEventsImage(w http.ResponseWriter, req *http.Request) {
	clientCtx := getClientContext(req)

	if clientCtx.getHandlers().eventsHandler == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(ErrorJsonMsg("Event proxy is not enabled for this environment"))
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
			clientCtx.getHandlers().eventsHandler.ServeHTTP(nullW, eventsReq)
		}()
	}

	w.Header().Set("Content-Type", "image/gif")
	w.Write(transparent1PixelImg)
}
