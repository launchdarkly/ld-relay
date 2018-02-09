package ldclient

import (
	"encoding/json"
	"github.com/facebookgo/httpcontrol"
	"github.com/gregjones/httpcache"
	"io/ioutil"
	"net/http"
	"time"
)

const (
	LatestFlagsPath = "/sdk/latest-flags"
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

	requestor := requestor{
		sdkKey:     sdkKey,
		httpClient: httpClient,
		config:     config,
	}

	return &requestor
}

func (r *requestor) requestAllFlags() (map[string]*FeatureFlag, bool, error) {
	var features map[string]*FeatureFlag
	body, cached, err := r.makeRequest(LatestFlagsPath)
	if err != nil {
		return nil, false, err
	}
	if cached {
		return nil, true, nil
	}
	jsonErr := json.Unmarshal(body, &features)

	if jsonErr != nil {
		return nil, false, jsonErr
	}
	return features, cached, nil
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

func (r *requestor) makeRequest(resource string) ([]byte, bool, error) {
	req, reqErr := http.NewRequest("GET", r.config.BaseUri+resource, nil)
	url := req.URL.String()
	if reqErr != nil {
		return nil, false, reqErr
	}

	req.Header.Add("Authorization", r.sdkKey)
	req.Header.Add("User-Agent", "GoClient/"+Version)

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

	body, err := ioutil.ReadAll(res.Body)

	if err != nil {
		return nil, false, err
	}
	return body, cached, err
}
