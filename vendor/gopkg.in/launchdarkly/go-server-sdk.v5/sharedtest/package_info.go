// Package sharedtest contains types and functions used by SDK unit tests in multiple packages.
//
// Application code should not rely on anything in this package; it is not supported as part of the SDK.
// The one exception is that external implementations of PersistentDataStore can and should be tested by
// using PersistentDataStoreTestSuite.
//
// Note that this package is not allowed to reference the "internal" package, because the tests in that
// package use sharedtest helpers so it would be a circular reference.
package sharedtest
