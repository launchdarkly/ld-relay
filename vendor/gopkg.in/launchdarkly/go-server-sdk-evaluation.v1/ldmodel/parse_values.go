package ldmodel

import (
	"regexp"
	"time"

	"github.com/blang/semver"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"
)

func parseDateTime(value ldvalue.Value) (time.Time, bool) {
	switch value.Type() {
	case ldvalue.StringType:
		t, err := time.Parse(time.RFC3339Nano, value.StringValue())
		if err == nil {
			return t.UTC(), true
		}
	case ldvalue.NumberType:
		return unixMillisToUtcTime(value.Float64Value()), true
	}
	return time.Time{}, false
}

func unixMillisToUtcTime(unixMillis float64) time.Time {
	return time.Unix(0, int64(unixMillis)*int64(time.Millisecond)).UTC()
}

func parseRegexp(value ldvalue.Value) (*regexp.Regexp, bool) {
	if value.Type() == ldvalue.StringType {
		if r, err := regexp.Compile(value.StringValue()); err == nil {
			return r, true
		}
	}
	return nil, false
}

func parseSemVer(value ldvalue.Value) (semver.Version, bool) {
	if value.Type() == ldvalue.StringType {
		versionStr := value.StringValue()
		if sv, err := semver.Parse(versionStr); err == nil {
			return sv, true
		}
		// Failed to parse as-is; see if we can fix it by adding zeroes
		matchParts := versionNumericComponentsRegex.FindStringSubmatch(versionStr)
		if matchParts != nil {
			transformedVersionStr := matchParts[0]
			for i := 1; i < len(matchParts); i++ {
				if matchParts[i] == "" {
					transformedVersionStr += ".0"
				}
			}
			transformedVersionStr += versionStr[len(matchParts[0]):]
			if sv, err := semver.Parse(transformedVersionStr); err == nil {
				return sv, true
			}
		}
	}
	return semver.Version{}, false
}
