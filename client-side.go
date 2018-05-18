package main

import (
	"context"
	"io/ioutil"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/gregjones/httpcache"
)

type ClientSideContext struct {
	allowedOrigins []string
	clientContext
}

func (c *ClientSideContext) AllowedOrigins() []string {
	return c.allowedOrigins
}

type ClientSideMux struct {
	contextByKey map[string]*ClientSideContext
	baseUri      string
}

func (m ClientSideMux) selectClientByUrlParam(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		envId := mux.Vars(req)["envId"]
		envInfo := m.contextByKey[envId]
		if envInfo == nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("ld-relay is not configured for environment id " + envId))
			return
		}

		req = req.WithContext(context.WithValue(req.Context(), "context", envInfo))
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

	defer res.Body.Close()

	w.WriteHeader(res.StatusCode)
	bodyBytes, _ := ioutil.ReadAll(res.Body)
	w.Write(bodyBytes)
}

func allowMethodOptionsHandler(method string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", method)
		w.WriteHeader(http.StatusOK)
	})
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
	w.Header().Set("Access-Control-Allow-Methods", "GET, REPORT")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length")
}
