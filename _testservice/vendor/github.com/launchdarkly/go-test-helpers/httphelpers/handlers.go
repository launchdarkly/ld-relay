package httphelpers

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
)

// HTTPRequestInfo represents a request captured by NewRecordingHTTPHandler.
type HTTPRequestInfo struct {
	Request *http.Request
	Body    []byte // body has to be captured separately by server because you can't read it after the response is sent
}

func getRequestBody(request *http.Request) []byte {
	if request.Body == nil {
		return nil
	}
	body, _ := ioutil.ReadAll(request.Body)
	return body
}

// DelegatingHandler is a struct that behaves as an http.Handler by delegating to the handler it wraps.
// Use this if you want to change the handler's behavior dynamically during a test.
//
//     dh := &httphelpers.DelegatingHandler{httphelpers.HandlerWithStatus(200)}
//     server := httptest.NewServer(dh) // the server will return 200
//     dh.Handler = httphelpers.HandlerWithStatus(401) // now the server will return 401
type DelegatingHandler struct {
	Handler http.Handler
}

func (d *DelegatingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.Handler.ServeHTTP(w, r)
}

// HandlerForMethod is a simple alternative to using an HTTP router. It delegates to the specified handler
// if the method matches; otherwise to the default handler, or a 405 if that is nil.
func HandlerForMethod(method string, handlerForMethod http.Handler, defaultHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == method {
			handlerForMethod.ServeHTTP(w, r)
		} else {
			if defaultHandler != nil {
				defaultHandler.ServeHTTP(w, r)
			} else {
				w.WriteHeader(405)
			}
		}
	})
}

// HandlerForPath is a simple alternative to using an HTTP router. It delegates to the specified handler
// if the path matches; otherwise to the default handler, or a 404 if that is nil.
func HandlerForPath(path string, handlerForPath http.Handler, defaultHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == path {
			handlerForPath.ServeHTTP(w, r)
		} else {
			if defaultHandler != nil {
				defaultHandler.ServeHTTP(w, r)
			} else {
				w.WriteHeader(404)
			}
		}
	})
}

// HandlerForPathRegex is a simple alternative to using an HTTP router. It delegates to the specified handler
// if the path matches; otherwise to the default handler, or a 404 if that is nil.
func HandlerForPathRegex(pathRegex string, handlerForPath http.Handler, defaultHandler http.Handler) http.Handler {
	pr := regexp.MustCompile(pathRegex)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pr.MatchString(r.URL.Path) {
			handlerForPath.ServeHTTP(w, r)
		} else {
			if defaultHandler != nil {
				defaultHandler.ServeHTTP(w, r)
			} else {
				w.WriteHeader(404)
			}
		}
	})
}

// HandlerWithJSONResponse creates an HTTP handler that returns a 200 status and the JSON encoding of
// the specified object.
func HandlerWithJSONResponse(contentToEncode interface{}, additionalHeaders http.Header) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bytes, err := json.Marshal(contentToEncode)
		if err != nil {
			log.Printf("error encoding JSON response: %s", err)
			w.WriteHeader(500)
		} else {
			w.Header().Set("Content-Type", "application/json")
			for k, vv := range additionalHeaders {
				w.Header()[k] = vv
			}
			w.WriteHeader(200)
			_, _ = w.Write(bytes)
		}
	})
}

// HandlerWithResponse creates an HTTP handler that always returns the same status code, headers, and body.
func HandlerWithResponse(status int, headers http.Header, body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, vv := range headers {
			w.Header()[k] = vv
		}
		w.WriteHeader(status)
		if body != nil {
			_, _ = w.Write(body)
		}
	})
}

// HandlerWithStatus creates an HTTP handler that always returns the same status code.
func HandlerWithStatus(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	})
}

// RecordingHandler wraps any HTTP handler in another handler that pushes received requests onto a channel.
//
//     handler, requestsCh := httphelpers.RecordingHandler(httphelpers.HandlerWithStatus(200))
//     httphelpers.WithServer(handler, func(server *http.TestServer) {
//         doSomethingThatMakesARequest(server.URL) // request will receive a 200 status
//         r := <-requestsCh
//         verifyRequestPropertiesWereCorrect(r.Request, r.Body)
//     })
func RecordingHandler(delegateToHandler http.Handler) (http.Handler, <-chan HTTPRequestInfo) {
	requestsCh := make(chan HTTPRequestInfo, 100)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsCh <- HTTPRequestInfo{r, getRequestBody(r)}
		delegateToHandler.ServeHTTP(w, r)
	})
	return handler, requestsCh
}

// SequentialHandler creates an HTTP handler that delegates to one handler per request, in the order given.
// If there are more requests than parameters, all subsequent requests go to the last handler.
//
// In this example, the first HTTP request will get a 503, and all subsequent requests will get a 200.
//
//     handler := httphelpers.SequentialHandler(
//         httphelpers.HandlerWithStatus(503),
//         httphelpers.HandlerWithStatus(200)
//     )
func SequentialHandler(firstHandler http.Handler, remainingHandlers ...http.Handler) http.Handler {
	allHandlers := append([]http.Handler{firstHandler}, remainingHandlers...)
	requestCounter := 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler := allHandlers[requestCounter]
		if requestCounter < len(allHandlers)-1 {
			requestCounter++
		}
		handler.ServeHTTP(w, r)
	})
}

// PanicHandler creates an HTTP handler that will panic.
//
// This is not useful inside an actual HTTP server, but when used in conjunction with NewHTTPClientFromHandler,
// it allows you to simulate an I/O error.
//
//     handler := PanicHandler(&net.AddrError{})
//     client := NewClientFromHandler(handler)
//     // All requests made with this client will return an AddrError (or some other error that wraps that error)
func PanicHandler(err error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(err)
	})
}
