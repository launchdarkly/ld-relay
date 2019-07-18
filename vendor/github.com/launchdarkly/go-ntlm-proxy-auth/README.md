# go-ntlm-proxy-auth

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![GoDoc](https://godoc.org/github.com/launchdarkly/go-ntlm-proxy-auth?status.svg)](https://godoc.org/github.com/launchdarkly/go-ntlm-proxy-auth)

With this package, you can connect to http/https servers protected by an NTLM proxy in Golang.

This is a fork of https://github.com/Codehardt/go-ntlm-proxy-auth which adds support for HTTPS proxy URLs. It also uses the fork https://github.com/launchdarkly/go-ntlmssp instead of `github.com/Azure/go-ntlmssp`.

## Example: NewNTLMProxyDialContext

```golang
// create a dialer
dialer := &net.Dialer{
    Timeout:   30 * time.Second,
    KeepAlive: 30 * time.Second,
}

// wrap dial context with NTLM
ntlmDialContext := ntlm.NewNTLMProxyDialContext(dialer, proxyURL, "user", "password", "domain", nil)

// create a http(s) client
client := &http.Client{
    Transport: &http.Transport{
        Proxy: nil, // !!! IMPORTANT, do not set proxy here !!!
        DialContext: ntlmDialContext,
    },
}
```
## Example: WrapDialContext (deprecated - does not support HTTPS proxy URL)

```golang
// create a dialer
dialer := &net.Dialer{
    Timeout:   30 * time.Second,
    KeepAlive: 30 * time.Second,
}

// wrap dial context with NTLM
ntlmDialContext := ntlm.WrapDialContext(dialer.DialContext, "proxyAddr", "user", "password", "domain")

// create a http(s) client
client := &http.Client{
    Transport: &http.Transport{
        Proxy: nil, // !!! IMPORTANT, do not set proxy here !!!
        DialContext: ntlmDialContext,
    },
}
```