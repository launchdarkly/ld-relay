package main

// Diagnostic-only: when LD_DEBUG_PPROF_ADDR is set (e.g. ":6060"), expose net/http/pprof
// on that address so heap/goroutine profiles can be captured under load. Off by default;
// not part of the concurrency feature. Safe to delete.

import (
	"net/http"
	_ "net/http/pprof"
	"os"
)

func init() {
	if addr := os.Getenv("LD_DEBUG_PPROF_ADDR"); addr != "" {
		go func() {
			_ = http.ListenAndServe(addr, nil) //nolint:gosec // debug-only listener
		}()
	}
}
