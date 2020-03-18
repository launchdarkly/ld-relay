// Package ldvalue provides an abstraction of the LaunchDarkly SDK's general value type. LaunchDarkly
// supports the standard JSON data types of null, boolean, number, string, array, and object (map), for
// any feature flag variation or custom user attribute. The ldvalue.Value type can contain any of these.
//
// For backward compatibility, some SDK methods and types still represent these values with the general
// Go type "interface{}". Whenever there is an alternative that uses ldvalue.Value, that is preferred;
// in the next major version release of the SDK, the uses of interface{} will be removed. There are two
// reasons. First, interface{} can contain values that have no JSON encoding. Second, interface{} could
// be an array, slice, or map, all of which are passed by reference and could be modified in one place
// causing unexpected effects in another place. Value is guaranteed to be immutable and to contain only
// JSON-compatible types as long as you do not use the UnsafeUseArbitraryValue() and
// UnsafeArbitraryValue() methods (which will be removed in the future).
//
// This package also provides the helper type OptionalString, a safer alternative to using string pointers.
package ldvalue
