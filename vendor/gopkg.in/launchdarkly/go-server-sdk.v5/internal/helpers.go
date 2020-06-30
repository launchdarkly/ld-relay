package internal

import (
	"fmt"
	"net/http"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
	ldevents "gopkg.in/launchdarkly/go-sdk-events.v1"
	"gopkg.in/launchdarkly/go-server-sdk-evaluation.v1/ldmodel"
	"gopkg.in/launchdarkly/go-server-sdk.v5/interfaces"
	"gopkg.in/launchdarkly/go-server-sdk.v5/sharedtest"
)

// This interface is implemented only by the SDK's own ClientContext implementation.
type hasDiagnosticsManager interface {
	GetDiagnosticsManager() *ldevents.DiagnosticsManager
}

type allData struct {
	Flags    map[string]*ldmodel.FeatureFlag `json:"flags"`
	Segments map[string]*ldmodel.Segment     `json:"segments"`
}

type httpStatusError struct {
	Message string
	Code    int
}

func (e httpStatusError) Error() string {
	return e.Message
}

// Tests whether an HTTP error status represents a condition that might resolve on its own if we retry,
// or at least should not make us permanently stop sending requests.
func isHTTPErrorRecoverable(statusCode int) bool {
	if statusCode >= 400 && statusCode < 500 {
		switch statusCode {
		case 400: // bad request
			return true
		case 408: // request timeout
			return true
		case 429: // too many requests
			return true
		default:
			return false // all other 4xx errors are unrecoverable
		}
	}
	return true
}

func httpErrorDescription(statusCode int) string {
	message := ""
	if statusCode == 401 || statusCode == 403 {
		message = " (invalid SDK key)"
	}
	return fmt.Sprintf("HTTP error %d%s", statusCode, message)
}

// Logs an HTTP error or network error at the appropriate level and determines whether it is recoverable
// (as defined by isHTTPErrorRecoverable).
func checkIfErrorIsRecoverableAndLog(
	loggers ldlog.Loggers,
	errorDesc, errorContext string,
	statusCode int,
	recoverableMessage string,
) bool {
	if statusCode > 0 && !isHTTPErrorRecoverable(statusCode) {
		loggers.Errorf("Error %s (giving up permanently): %s", errorContext, errorDesc)
		return false
	}
	loggers.Warnf("Error %s (%s): %s", errorContext, recoverableMessage, errorDesc)
	return true
}

func checkForHTTPError(statusCode int, url string) error {
	if statusCode == http.StatusUnauthorized {
		return httpStatusError{
			Message: fmt.Sprintf("Invalid SDK key when accessing URL: %s. Verify that your SDK key is correct.", url),
			Code:    statusCode}
	}

	if statusCode == http.StatusNotFound {
		return httpStatusError{
			Message: fmt.Sprintf("Resource not found when accessing URL: %s. Verify that this resource exists.", url),
			Code:    statusCode}
	}

	if statusCode/100 != 2 {
		return httpStatusError{
			Message: fmt.Sprintf("Unexpected response code: %d when accessing URL: %s", statusCode, url),
			Code:    statusCode}
	}
	return nil
}

// makeAllStoreData returns a data set that can be used to initialize a data store
func makeAllStoreData(
	flags map[string]*ldmodel.FeatureFlag,
	segments map[string]*ldmodel.Segment,
) []interfaces.StoreCollection {
	flagsColl := make([]interfaces.StoreKeyedItemDescriptor, 0, len(flags))
	for key, flag := range flags {
		flagsColl = append(flagsColl, interfaces.StoreKeyedItemDescriptor{
			Key:  key,
			Item: sharedtest.FlagDescriptor(*flag),
		})
	}
	segmentsColl := make([]interfaces.StoreKeyedItemDescriptor, 0, len(segments))
	for key, segment := range segments {
		segmentsColl = append(segmentsColl, interfaces.StoreKeyedItemDescriptor{
			Key:  key,
			Item: sharedtest.SegmentDescriptor(*segment),
		})
	}
	return []interfaces.StoreCollection{
		interfaces.StoreCollection{Kind: interfaces.DataKindFeatures(), Items: flagsColl},
		interfaces.StoreCollection{Kind: interfaces.DataKindSegments(), Items: segmentsColl},
	}
}
