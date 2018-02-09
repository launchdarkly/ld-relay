package main

import (
	"context"
	"io/ioutil"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/gregjones/httpcache"
)

type ClientSideInfo struct {
	allowedOrigins []string
	client         FlagReader
}

type ClientSideMux struct {
	infoByKey map[string]ClientSideInfo
	baseUri   string
}

func (m ClientSideMux) selectClientByUrlParam(next func(w http.ResponseWriter, req *http.Request)) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		envId := mux.Vars(req)["envId"]
		envInfo := m.infoByKey[envId]
		if envInfo.client == nil {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("ld-relay is not configured for environment id " + envId))
			return
		}

		ctx := clientContextImpl{client: envInfo.client}
		req = req.WithContext(context.WithValue(req.Context(), "context", ctx))
		next(w, req)
	}
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
		return
	}

	w.Header().Set("Content-Type", res.Header["Content-Type"][0])

	defer res.Body.Close()

	w.WriteHeader(res.StatusCode)
	bodyBytes, _ := ioutil.ReadAll(res.Body)
	w.Write(bodyBytes)
}

func (m ClientSideMux) optionsHandler(method string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", method)
		w.WriteHeader(http.StatusOK)
	})
}

func (m ClientSideMux) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		envId := mux.Vars(r)["envId"]
		domains := m.infoByKey[envId].allowedOrigins
		corsMiddlewareByEnvAllowedDomains(domains, r).ServeHTTP(w, r)
		next.ServeHTTP(w, r)
	})
}

func corsMiddlewareByEnvAllowedDomains(domains []string, r *http.Request) http.Handler {
	if len(domains) != 0 {
		return corsDomainsMiddleware(domains)
	}
	return corsHeadersMiddleware()
}

func corsHeadersMiddleware() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := defaultAllowedOrigin
		if r.Header.Get("Origin") != "" {
			origin = r.Header.Get("Origin")
		}
		setCorsHeaders(w, origin)
	})
}

func corsDomainsMiddleware(domains []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, d := range domains {
			if r.Header.Get("Origin") == d {
				setCorsHeaders(w, d)
				return
			}
		}
		// Not a valid origin, set allowed origin to any allowed origin
		setCorsHeaders(w, domains[0])
	})
}

func setCorsHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Credentials", "false")
	w.Header().Set("Access-Control-Max-Age", "300")
	w.Header().Set("Access-Control-Allow-Methods", "GET, REPORT")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length")
}
