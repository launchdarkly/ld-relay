// +build go1.12

package internal

// TLS 1.3 is supported in Go 1.12 and above

import "crypto/tls"

func ParseTLSVersion(name string) (uint16, bool) {
	switch name {
	case "1.0":
		return tls.VersionTLS10, true
	case "1.1":
		return tls.VersionTLS11, true
	case "1.2":
		return tls.VersionTLS12, true
	case "1.3":
		return tls.VersionTLS13, true
	default:
		return 0, false
	}
}
