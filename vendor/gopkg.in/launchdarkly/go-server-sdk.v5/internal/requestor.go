package internal

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/gregjones/httpcache"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
)

// SDK endpoints
const (
	LatestFlagsPath    = "/sdk/latest-flags"
	LatestSegmentsPath = "/sdk/latest-segments"
	LatestAllPath      = "/sdk/latest-all"
)

// requestor is the interface implemented by requestorImpl, used for testing purposes
type requestor interface {
	requestAll() (data allData, cached bool, err error)
	requestResource(kind interfaces.StoreDataKind, key string) (interfaces.StoreItemDescriptor, error)
}

// requestorImpl is the internal implementation of getting flag/segment data from the LD polling endpoints.
type requestorImpl struct {
	httpClient *http.Client
	baseURI    string
	headers    http.Header
	loggers    ldlog.Loggers
}

type malformedJSONError struct {
	innerError error
}

func (e malformedJSONError) Error() string {
	return e.innerError.Error()
}

func newRequestorImpl(
	context interfaces.ClientContext,
	httpClient *http.Client,
	baseURI string,
	withCache bool,
) requestor {
	httpClientToUse := httpClient
	if httpClientToUse == nil {
		httpClientToUse = context.GetHTTP().CreateHTTPClient()
	}
	if withCache {
		modifiedClient := *httpClientToUse
		modifiedClient.Transport = &httpcache.Transport{
			Cache:               httpcache.NewMemoryCache(),
			MarkCachedResponses: true,
			Transport:           httpClientToUse.Transport,
		}
		httpClientToUse = &modifiedClient
	}

	return &requestorImpl{
		httpClient: httpClientToUse,
		baseURI:    baseURI,
		headers:    context.GetHTTP().GetDefaultHeaders(),
		loggers:    context.GetLogging().GetLoggers(),
	}
}

func (r *requestorImpl) requestAll() (allData, bool, error) {
	if r.loggers.IsDebugEnabled() {
		r.loggers.Debug("Polling LaunchDarkly for feature flag updates")
	}

	var data allData
	body, cached, err := r.makeRequest(LatestAllPath)
	if err != nil {
		return allData{}, false, err
	}
	if cached {
		return allData{}, true, nil
	}
	jsonErr := json.Unmarshal(body, &data)

	if jsonErr != nil {
		return allData{}, false, malformedJSONError{jsonErr}
	}
	return data, cached, nil
}

func (r *requestorImpl) requestResource(
	kind interfaces.StoreDataKind,
	key string,
) (interfaces.StoreItemDescriptor, error) {
	var resource string
	switch kind.GetName() {
	case "segments":
		resource = LatestSegmentsPath + "/" + key
	case "features":
		resource = LatestFlagsPath + "/" + key
	default:
		return interfaces.StoreItemDescriptor{}, fmt.Errorf("unexpected item type: %s", kind)
	}
	body, _, err := r.makeRequest(resource)
	if err != nil {
		return interfaces.StoreItemDescriptor{}, err
	}
	item, err := kind.Deserialize(body)
	if err != nil {
		return item, malformedJSONError{err}
	}
	return item, nil
}

func (r *requestorImpl) makeRequest(resource string) ([]byte, bool, error) {
	req, reqErr := http.NewRequest("GET", r.baseURI+resource, nil)
	if reqErr != nil {
		return nil, false, reqErr
	}
	url := req.URL.String()

	for k, vv := range r.headers {
		req.Header[k] = vv
	}

	res, resErr := r.httpClient.Do(req)

	if resErr != nil {
		return nil, false, resErr
	}

	defer func() {
		_, _ = ioutil.ReadAll(res.Body)
		_ = res.Body.Close()
	}()

	if err := checkForHTTPError(res.StatusCode, url); err != nil {
		return nil, false, err
	}

	cached := res.Header.Get(httpcache.XFromCache) != ""

	body, ioErr := ioutil.ReadAll(res.Body)

	if ioErr != nil {
		return nil, false, ioErr // COVERAGE: there is no way to simulate this condition in unit tests
	}
	return body, cached, nil
}
