//go:build tools

// Add tools used by Relay Proxy (such as linters, release tools; anything that uses a Go module and should be
// tracked in go.mod) here.
package main

import (
	_ "github.com/goreleaser/goreleaser"
)
