# LaunchDarkly Relay Proxy - Building and Running in Windows

[(back to README)](../README.md)

To run the Relay Proxy as a Windows service, you will first need to build it with the Go compiler:

```shell
go build cmd/ld-relay
```

This will create the executable `ld-relay.exe`.

Then, to register it as a service:

```shell
sc create ld-relay DisplayName="LaunchDarkly Relay Proxy" start="auto" binPath="C:\path\to\ld-relay.exe -config C:\path\to\ld-relay.conf"
```
