// Package core contains Relay Proxy core implementation components and internal APIs.
//
// The principle behind the organization of this code is as follows:
//
// 1. Anything that needs to be referenced from the top-level Relay application code should be in
// internal/core or one of its subpackages - unless it is entirely related to an "enterprise"
// feature (such as autoconfig), in which case it can be in internal/ but outside of core.
//
// 2. Anything that needs to be referenced only from within the core code, and not from any higher
// level, should be in a subpackage of internal/core/internal so that it cannot be imported from
// anywhere outside of the internal/core subtree.
//
// This is meant to encourage as much encapsulation as possible, and also to facilitate separating
// out the "enterprise" code from the rest of the Relay distribution if that ever becomes desirable.
package core
