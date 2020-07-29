// Package middleware contains helpers for adding standard behavior like authentication and metrics
// to REST endpoints.
//
// This package exports its symbols from core, rather than being in core/internal, because the Relay
// and Relay Enterprise projects will need to use the standard middleware whenever they create their
// own endpoints.
package middleware
