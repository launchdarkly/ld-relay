// Package basictypes contains types and constants that are used by multiple Relay packages
// and do not have any testable logic of their own.
//
// Putting such things here, instead of in one of the packages that use them, allows us to
// reference them in shared test code as well without causing import cycles if those other
// packages also use the shared test code. It also provides a convenient way to see basic
// characteristics of Relay's internal model.
package basictypes
