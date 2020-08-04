// Package core contains the Relay Proxy core implementation and internal APIs.
//
// The basic Relay Proxy distribution and Relay Proxy Enterprise share much of the same behavior.
// As much as possible, that behavior is implemented in the core package and its subpackages.
// Symbols are exported from core only if they will need to be directly accessed by the other
// Relay projects in order for them to customize the core behavior; otherwise, they should be
// either unexported or in core's internal packages.
package core
