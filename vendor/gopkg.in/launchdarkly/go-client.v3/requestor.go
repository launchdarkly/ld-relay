package ldclient

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/facebookgo/httpcontrol"
	"github.com/gregjones/httpcache"
)

const (
	LatestFlagsPath    = "/sdk/latest-flags"
	LatestSegmentsPath = "/sdk/latest-segments"
	LatestAllPath      = "/sdk/latest-all"
)

type requestor struct {
	sdkKey     string
	httpClient *http.Client
	config     Config
}

func newRequestor(sdkKey string, config Config) *requestor {
	baseTransport := httpcontrol.Transport{
		RequestTimeout: config.Timeout,
		DialTimeout:    config.Timeout,
		DialKeepAlive:  1 * time.Minute,
		MaxTries:       3,
	}

	cachingTransport := &httpcache.Transport{
		Cache:               httpcache.NewMemoryCache(),
		MarkCachedResponses: true,
		Transport:           &baseTransport,
	}

	httpClient := cachingTransport.Client()

	httpRequestor := requestor{
		sdkKey:     sdkKey,
		httpClient: httpClient,
		config:     config,
	}

	return &httpRequestor
}

func (r *requestor) requestAll() (allData, bool, error) {
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
		return allData{}, false, jsonErr
	}
	return data, cached, nil
}

func (r *requestor) requestFlag(key string) (*FeatureFlag, error) {
	var feature FeatureFlag
	resource := LatestFlagsPath + "/" + key
	body, _, err := r.makeRequest(resource)
	if err != nil {
		return nil, err
	}

	jsonErr := json.Unmarshal(body, &feature)

	if jsonErr != nil {
		return nil, jsonErr
	}
	return &feature, nil
}

func (r *requestor) requestSegment(key string) (*Segment, error) {
	var segment Segment
	resource := LatestSegmentsPath + "/" + key
	body, _, err := r.makeRequest(resource)
	if err != nil {
		return nil, err
	}

	jsonErr := json.Unmarshal(body, &segment)

	if jsonErr != nil {
		return nil, jsonErr
	}
	return &segment, nil
}

func (r *requestor) makeRequest(resource string) ([]byte, bool, error) {
	req, reqErr := http.NewRequest("GET", r.config.BaseUri+resource, nil)
	url := req.URL.String()
	if reqErr != nil {
		return nil, false, reqErr
	}

	req.Header.Add("Authorization", r.sdkKey)
	req.Header.Add("User-Agent", r.config.UserAgent)

	res, resErr := r.httpClient.Do(req)

	defer func() {
		if res != nil && res.Body != nil {
			ioutil.ReadAll(res.Body)
			res.Body.Close()
		}
	}()

	if resErr != nil {
		return nil, false, resErr
	}

	err := checkStatusCode(res.StatusCode, url)
	if err != nil {
		return nil, false, err
	}

	cached := res.Header.Get(httpcache.XFromCache) != ""

	body, ioErr := ioutil.ReadAll(res.Body)

	if ioErr != nil {
		return nil, false, err
	}
	return body, cached, nil
}
