
This file describes the testing tool in _testservice. It is only of interest to developers of the Relay Proxy.

This small application is used in CI testing of the Relay Proxy, for simple end-to-end integration tests that only require the LaunchDarkly streaming endpoint to be available on a very basic level, allowing these tests to be self-contained in CI rather than having to connect to the actual LaunchDarkly service.

To avoid having to manage background processes directly in a test script, this tool launches a background instance of itself when it is started and provides an option to shut down that process.

`testservice start streamer <PORT>`: Starts the simulated stream endpoint in a background process, listening on the specified port. Does not exit until the listener has started.

`testservice stop streamer`: Stops the background process if it is running.

The PID of the background process is written to ./.testservice-streamer.pid.
