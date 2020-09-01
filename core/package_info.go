// Package core contains Relay Proxy core implementation components and internal APIs.
//
// The basic Relay Proxy distribution and Relay Proxy Enterprise share much of the same behavior.
// As much as possible, that behavior is implemented in the core package and its subpackages.
// Symbols are exported from core only if they will need to be directly accessed by the other
// Relay projects in order for them to customize the core behavior; otherwise, they should be
// either unexported or in core's internal packages.
//
// Third-party application code should never reference exported symbols from this package or its
// subpackages directly. Such use is unsupported, and the behavior of the core code is subject to
// change.
package core
