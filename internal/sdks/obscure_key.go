package sdks

import "regexp"

var (
	hexDigitRegex    = regexp.MustCompile(`[a-fA-F\d]`)
	alphaPrefixRegex = regexp.MustCompile(`^[a-z][a-z][a-z]-`)
)

// ObscureKey returns an obfuscated version of an SDK key or mobile key.
func ObscureKey(key string) string {
	if alphaPrefixRegex.MatchString(key) {
		return key[0:4] + ObscureKey(key[4:])
	}
	if len(key) > 4 {
		return hexDigitRegex.ReplaceAllString(key[:len(key)-5], "*") + key[len(key)-5:]
	}
	return key
}
