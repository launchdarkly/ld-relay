# go-ntlm-proxy-auth

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![GoDoc](https://godoc.org/github.com/Codehardt/go-ntlm-proxy-auth?status.svg)](https://godoc.org/github.com/Codehardt/go-ntlm-proxy-auth)

With this package, you can connect to http/https servers protected by an NTLM proxy in Golang.

## Example

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
        Dial: dialer.Dial,
        DialContext: ntlmDialContext,
        // TLSClientConfig: ...
    },
}
```