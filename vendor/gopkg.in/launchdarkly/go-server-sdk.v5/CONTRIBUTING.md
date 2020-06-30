# Contributing to the LaunchDarkly Server-side SDK for Go
 
LaunchDarkly has published an [SDK contributor's guide](https://docs.launchdarkly.com/docs/sdk-contributors-guide) that provides a detailed explanation of how our SDKs work. See below for additional information on how to contribute to this SDK.
 
## Submitting bug reports and feature requests

The LaunchDarkly SDK team monitors the [issue tracker](https://github.com/launchdarkly/go-server-sdk/issues) in the SDK repository. Bug reports and feature requests specific to this SDK should be filed in this issue tracker. The SDK team will respond to all newly filed issues within two business days.
 
## Submitting pull requests
 
We encourage pull requests and other contributions from the community. Before submitting pull requests, ensure that all temporary or unintended code is removed. Don't worry about adding reviewers to the pull request; the LaunchDarkly SDK team will add themselves. The SDK team will acknowledge all pull requests within two business days.
 
## Build instructions
 
### Prerequisites
 
The SDK should be built against Go 1.13 or newer.

Note that the base import path is `gopkg.in/launchdarkly/go-server-sdk.v5`, not `github.com/launchdarkly/go-server-sdk`; all references in this code to other packages within the repository must use that same base import path. This ensures that the package can be referenced not only as a Go module, but also by projects that use older tools like `dep` and `govendor`, because the 5.x release of the Go SDK supports either module or non-module usage. Future releases of this package, and of the Go SDK, may drop support for non-module usage.

### Building

To build the SDK without running any tests:
```
make
```

If you wish to clean your working directory between builds, you can clean it by running:
```
make clean
```

To run the linter:
```
make lint
```

If you add any new dependencies to the SDK, use `dep ensure` to ensure that they are copied to `vendor/`.

### Testing
 
To build the SDK and run all unit tests:
```
make test
```

By default, the full unit test suite includes live tests of the integrations for Consul, DynamoDB, and Redis. Those tests expect you to have instances of all of those databases running locally. To skip them, set the environment variable `LD_SKIP_DATABASE_TESTS=1` before running the tests. If you do want to run them, the simplest way to provide the databases is with Docker:

```bash
docker run -p 8500:8500 consul
docker run -p 8000:8000 amazon/dynamodb-local
docker run -p 6379:6379 redis
```

## Best practices for performance

The Go SDK can be used in high-traffic application/service code where performance is critical. There are a number of coding principles to keep in mind for maximizing performance. The benchmarks that are run in CI are helpful in measuring the impact of code changes in this regard.

### Avoid heap allocations

Go's memory model uses a mix of stack and heap allocations, with the compiler transparently choosing the most appropriate strategy based on various type and scope rules. It is always preferable, when possible, to keep ephemeral values on the stack rather than on the heap to avoid creating extra work for the garbage collector.

- The most obvious rule is that anything explicitly allocated by reference (`x := &SomeType{}`), or returned by reference (`return &x`), will be allocated on the heap. Avoid this unless the object has mutable state that must be shared.
- Casting a value type to an interface causes it to be allocated on the heap, since an interface is really a combination of a type identifier and a hidden pointer.
- A closure that references any variables outside of its scope (including the method receiver, if it is inside a method) causes an object to be allocated on the heap containing the values or addresses of those variables.
- Treating a method as an anonymous function (`myFunc := someReceiver.SomeMethod`) is equivalent to a closure.

Allocations are counted in the benchmark output: "5 allocs/op" means that a total of 5 heap objects were allocated during each run of the benchmark. This does not mean that the objects were retained, only that they were allocated at some point.

For a much (MUCH) more detailed breakdown of this behavior, you may use the option `GODEBUG=allocfreetrace=1` while running a unit test or benchmark. This provides the type and code location of literally every heap allocation during the run. The output is extremely verbose, so it is recommended that you:

1. use the Makefile helper `benchmark-allocs` (see below) to reduce the number of benchmark runs and avoid capturing allocations from the Go tools themselves;
2. search the stacktrace output to find the method you are actually testing (such as `BoolVariation`) rather than the benchmark function name, so you are not looking at actions that are just part of the benchmark setup;
3. consider writing a smaller temporary benchmark specifically for this purpose, since most of the existing benchmarks will iterate over a series of parameters.

```bash
BENCHMARK=BenchmarkMySampleOperation make benchmark-allocs
```

### Avoid `defer` in simple cases

It's common to use `defer` to guarantee cleanup of some kind of temporary state when a method exits, such as releasing a lock. As convenient as this feature is, it should be avoided in high-traffic code paths _if it is safe to do so_, due to its small but consistent [runtime overhead](https://medium.com/i0exception/runtime-overhead-of-using-defer-in-go-7140d5c40e32).

It is safe to avoid `defer` if:

1. there is only one possible return point from the function, _and_:
2. there is no possibility of a panic at any point where a premature exit would leave things in an unwanted state.

Therefore, this optimization should be used only in small methods where the flow is simple and it is possible to prove that no panic can occur within the critical path.

Less preferable:

```go
func (t *thing) getNumber() int {
    t.lock.Lock()
    defer t.lock.Unlock()
    if t.isTwo {
        return 2
    }
    return 1
}
```

More preferable:

```go
func (t *thing) getNumber() int {
    t.lock.Lock()
    answer := 1
    if t.isTwo {
        answer = 2
    }
    t.lock.Unlock()
    return answer
}
```

Note that in this example, a panic _is_ possible on the first line if `t` is nil; but since the lock would not get locked in that scenario, things would still be left in a safe state even in that case.

### Minimize overhead when logging at debug level

The `Debug` logging level can be a useful diagnostic tool, and it is OK to log very verbosely at this level. However, to avoid slowing things down in the usual case where this log level is disabled, keep in mind:

1. Avoid string concatenation; use `Printf`-style placeholders instead.
2. Before calling `loggers.Debug` or `loggers.Debugf`, check `loggers.IsDebugEnabled()`. If it returns false, you should skip the `Debug` or `Debugf` call, since otherwise it can incur some overhead from converting the parameters of that call to `interface{}` values.
