# LaunchDarkly Relay Proxy - Building and running in Windows

[(Back to README)](../README.md)

To run the Relay Proxy as a Windows service, you must first build it with the Go compiler:

```shell
go build
```

This creates the executable `ld-relay.exe`.

The Go version must be at least 1.17.

Then, to register it as a service, use:

```shell
sc create ld-relay DisplayName="LaunchDarkly Relay Proxy" start="auto" binPath="C:\path\to\ld-relay.exe -config C:\path\to\ld-relay.conf"
```
