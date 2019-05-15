package ldclient

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"time"
)

// HttpStatusError describes an http error
type HttpStatusError struct {
	Message string
	Code    int
}

// Error returns a the error message for an http status error
func (e HttpStatusError) Error() string {
	return e.Message
}

// ParseTime converts any of the following into a pointer to a time.Time value:
//   RFC3339/ISO8601 timestamp (example: 2016-04-16T17:09:12.759-07:00)
//   Unix epoch milliseconds as string
//   Unix milliseconds as number
// Passing in a time.Time value will return a pointer to the input value.
// Unparsable inputs will return nil
// More info on RFC3339: http://stackoverflow.com/questions/522251/whats-the-difference-between-iso-8601-and-rfc-3339-date-formats
func ParseTime(input interface{}) *time.Time {
	if input == nil {
		return nil
	}

	// First check if we can easily detect the type as a time.Time or timestamp as string
	switch typedInput := input.(type) {
	case time.Time:
		return &typedInput
	case string:
		value, err := time.Parse(time.RFC3339Nano, typedInput)
		if err == nil {
			utcValue := value.UTC()
			return &utcValue
		}
	}

	// Is it a number or can it be parsed as a number?
	parsedNumberPtr := ParseFloat64(input)
	if parsedNumberPtr != nil {
		value := unixMillisToUtcTime(*parsedNumberPtr)
		return &value
	}
	return nil
}

// ParseFloat64 parses a numeric value as float64 from a string or another numeric type.
// Returns nil pointer if input is nil or unparsable.
func ParseFloat64(input interface{}) *float64 {
	if input == nil {
		return nil
	}

	switch typedInput := input.(type) {
	case float64:
		return &typedInput
	default:
		float64Type := reflect.TypeOf(float64(0))
		v := reflect.ValueOf(input)
		v = reflect.Indirect(v)
		if v.Type().ConvertibleTo(float64Type) {
			floatValue := v.Convert(float64Type)
			f64 := floatValue.Float()
			return &f64
		}
	}
	return nil
}

// unixMillisToUtcTime converts a Unix epoch milliseconds float64 value to the equivalent time.Time value with UTC location
func unixMillisToUtcTime(unixMillis float64) time.Time {
	return time.Unix(0, int64(unixMillis)*int64(time.Millisecond)).UTC()
}

// ToJsonRawMessage converts input to a *json.RawMessage if possible.
func ToJsonRawMessage(input interface{}) (json.RawMessage, error) {
	if input == nil {
		return nil, nil
	}
	switch typedInput := input.(type) {
	//already json, so just return casted input value
	case json.RawMessage:
		return typedInput, nil
	case []byte:
		inputJsonRawMessage := json.RawMessage(typedInput)
		return inputJsonRawMessage, nil
	default:
		inputJsonBytes, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("Could not marshal: %+v to json", input)
		}
		inputJsonRawMessage := json.RawMessage(inputJsonBytes)
		return inputJsonRawMessage, nil
	}
}

func checkForHttpError(statusCode int, url string) error {
	if statusCode == http.StatusUnauthorized {
		return HttpStatusError{
			Message: fmt.Sprintf("Invalid SDK key when accessing URL: %s. Verify that your SDK key is correct.", url),
			Code:    statusCode}
	}

	if statusCode == http.StatusNotFound {
		return HttpStatusError{
			Message: fmt.Sprintf("Resource not found when accessing URL: %s. Verify that this resource exists.", url),
			Code:    statusCode}
	}

	if statusCode/100 != 2 {
		return HttpStatusError{
			Message: fmt.Sprintf("Unexpected response code: %d when accessing URL: %s", statusCode, url),
			Code:    statusCode}
	}
	return nil
}

// MakeAllVersionedDataMap returns a map of version objects grouped by namespace that can be used to initialize a feature store
func MakeAllVersionedDataMap(
	features map[string]*FeatureFlag,
	segments map[string]*Segment) map[VersionedDataKind]map[string]VersionedData {

	allData := make(map[VersionedDataKind]map[string]VersionedData)
	allData[Features] = make(map[string]VersionedData)
	allData[Segments] = make(map[string]VersionedData)
	for k, v := range features {
		allData[Features][k] = v
	}
	for k, v := range segments {
		allData[Segments][k] = v
	}
	return allData
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

func httpErrorMessage(statusCode int, context string, recoverableMessage string) string {
	statusDesc := ""
	if statusCode == 401 {
		statusDesc = " (invalid SDK key)"
	}
	resultMessage := recoverableMessage
	if !isHTTPErrorRecoverable(statusCode) {
		resultMessage = "giving up permanently"
	}
	return fmt.Sprintf("Received HTTP error %d%s for %s - %s",
		statusCode, statusDesc, context, resultMessage)
}
