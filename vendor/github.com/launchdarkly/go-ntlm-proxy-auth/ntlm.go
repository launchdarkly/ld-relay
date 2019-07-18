package ntlm

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strings"

	ntlmssp "github.com/launchdarkly/go-ntlmssp"
)

// DialContext is the DialContext function that should be wrapped with a
// NTLM Authentication.
//
// Example for DialContext:
//
// dialContext := (&net.Dialer{KeepAlive: 30*time.Second, Timeout: 30*time.Second}).DialContext
type DialContext func(ctx context.Context, network, addr string) (net.Conn, error)

// NewNTLMProxyDialContext provides a DialContext function that includes transparent NTLM proxy authentication.
// Unlike WrapDialContext, it describes the proxy location with a full URL, whose scheme can be HTTP or HTTPS.
func NewNTLMProxyDialContext(dialer *net.Dialer, proxyURL url.URL, proxyUsername, proxyPassword, proxyDomain string, tlsConfig *tls.Config) DialContext {
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialProxy := func() (net.Conn, error) {
			debugf("ntlm> Will connect to proxy at " + proxyURL.Host)
			if proxyURL.Scheme == "https" {
				return tls.DialWithDialer(dialer, "tcp", proxyURL.Host, tlsConfig)
			}
			return dialer.DialContext(ctx, network, proxyURL.Host)
		}
		return dialAndNegotiate(addr, proxyUsername, proxyPassword, proxyDomain, dialProxy)
	}
}

// WrapDialContext wraps a DialContext with an NTLM Authentication to a proxy. Note that this does not support
// using HTTPS to connect to the proxy; use NewNTLMProxyDialContext if that is required.
func WrapDialContext(dialContext DialContext, proxyAddress, proxyUsername, proxyPassword, proxyDomain string) DialContext {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialAndNegotiate(addr, proxyUsername, proxyPassword, proxyDomain, func() (net.Conn, error) {
			return dialContext(ctx, network, proxyAddress)
		})
	}
}

func dialAndNegotiate(addr, proxyUsername, proxyPassword, proxyDomain string, baseDial func() (net.Conn, error)) (net.Conn, error) {
	conn, err := baseDial()
	if err != nil {
		debugf("ntlm> Could not call dial context with proxy: %s", err)
		return conn, err
	}
	// NTLM Step 1: Send Negotiate Message
	negotiateMessage, err := ntlmssp.NewNegotiateMessage(proxyDomain, "")
	if err != nil {
		debugf("ntlm> Could not negotiate domain '%s': %s", proxyDomain, err)
		return conn, err
	}
	debugf("ntlm> NTLM negotiate message: '%s'", base64.StdEncoding.EncodeToString(negotiateMessage))
	header := make(http.Header)
	header.Set("Proxy-Authorization", fmt.Sprintf("NTLM %s", base64.StdEncoding.EncodeToString(negotiateMessage)))
	header.Set("Proxy-Connection", "Keep-Alive")
	connect := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: header,
	}
	if err := connect.Write(conn); err != nil {
		debugf("ntlm> Could not write negotiate message to proxy: %s", err)
		return conn, err
	}
	debugf("ntlm> Successfully sent negotiate message to proxy")
	// NTLM Step 2: Receive Challenge Message
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connect)
	if err != nil {
		debugf("ntlm> Could not read response from proxy: %s", err)
		return conn, err
	}
	_, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		debugf("ntlm> Could not read response body from proxy: %s", err)
		return conn, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		debugf("ntlm> Expected %d as return status, got: %d", http.StatusProxyAuthRequired, resp.StatusCode)
		return conn, errors.New(http.StatusText(resp.StatusCode))
	}
	challenge := strings.Split(resp.Header.Get("Proxy-Authenticate"), " ")
	if len(challenge) < 2 {
		debugf("ntlm> The proxy did not return an NTLM challenge, got: '%s'", resp.Header.Get("Proxy-Authenticate"))
		return conn, errors.New("no NTLM challenge received")
	}
	debugf("ntlm> NTLM challenge: '%s'", challenge[1])
	challengeMessage, err := base64.StdEncoding.DecodeString(challenge[1])
	if err != nil {
		debugf("ntlm> Could not base64 decode the NTLM challenge: %s", err)
		return conn, err
	}
	// NTLM Step 3: Send Authorization Message
	debugf("ntlm> Processing NTLM challenge with username '%s' and password with length %d", proxyUsername, len(proxyPassword))
	authenticateMessage, err := ntlmssp.ProcessChallenge(challengeMessage, proxyUsername, proxyPassword)
	if err != nil {
		debugf("ntlm> Could not process the NTLM challenge: %s", err)
		return conn, err
	}
	debugf("ntlm> NTLM authorization: '%s'", base64.StdEncoding.EncodeToString(authenticateMessage))
	header.Set("Proxy-Authorization", fmt.Sprintf("NTLM %s", base64.StdEncoding.EncodeToString(authenticateMessage)))
	connect = &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: header,
	}
	if err := connect.Write(conn); err != nil {
		debugf("ntlm> Could not write authorization to proxy: %s", err)
		return conn, err
	}
	resp, err = http.ReadResponse(br, connect)
	if err != nil {
		debugf("ntlm> Could not read response from proxy: %s", err)
		return conn, err
	}
	if resp.StatusCode != http.StatusOK {
		debugf("ntlm> Expected %d as return status, got: %d", http.StatusOK, resp.StatusCode)
		return conn, errors.New(http.StatusText(resp.StatusCode))
	}
	// Succussfully authorized with NTLM
	debugf("ntlm> Successfully injected NTLM to connection")
	return conn, nil
}
