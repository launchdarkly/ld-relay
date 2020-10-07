package relayenv

import "net/http/httputil"

// JSClientContext contains additional environment properties that are only relevant if this
// environment supports JavaScript clients (i.e. we know its environment ID).
type JSClientContext struct {
	// Origins is the configured list of allowed origins for CORS requests.
	Origins []string

	// Proxy is a ReverseProxy that we create for requests that are to be directly proxied to a
	// LaunchDarkly endpoint. Despite its name, the Relay Proxy does not normally use direct
	// proxying, but in the case of the goals resource for JS clients it is the simplest way.
	Proxy *httputil.ReverseProxy
}

// AllowedOrigins implements the internal interface for getting CORS allowed origins.
func (c JSClientContext) AllowedOrigins() []string {
	return c.Origins
}
