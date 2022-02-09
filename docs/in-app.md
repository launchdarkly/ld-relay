# LaunchDarkly Relay Proxy - Building within an application

[(Back to README)](../README.md)

If you need to customize the Relay Proxy's behavior and runtime environment in ways that the usual configuration settings don't support, you can incorporate the Relay Proxy code into your own application and let it provide service endpoints on a server that you configure.

Building the Relay Proxy code requires Go 1.16 or later.

Here is an example of how you might run the Relay Proxy endpoints inside your web server beneath a path called `/relay`, using [Gorilla](https://github.com/gorilla/mux) to set up the service routes.

```go
import (
    "github.com/gorilla/mux"
    "github.com/launchdarkly/ld-relay/v6/config"
    "github.com/launchdarkly/ld-relay/v6/relay"
    "gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

func createRelayConfig() config.Config {
    var cfg config.Config
    if err := config.LoadConfigFile(&cfg, "path/to/my.config", ldlog.NewDefaultLoggers()); err != nil {
        log.Fatalf("Error loading config file: %s", err)
    }
    return cfg
}

r, err := relay.NewRelay(createRelayConfig(), ldlog.NewDefaultLoggers(), nil)
if err != nil {
    log.Fatalf("Error creating relay: %s", err)
}

router := mux.NewRouter()
router.PathPrefix("/relay").Handler(http.StripPrefix("/relay", r))
```

The above example uses a configuration file. You can also pass in a `config.Config` struct that you have filled in directly. Note that some of the fields use types from `github.com/launchdarkly/go-configtypes` to enforce validation rules.

```go
import (
    "github.com/launchdarkly/ld-relay/v6/config"
    configtypes "github.com/launchdarkly/go-configtypes"
)

func createRelayConfig() config.Config {
    var cfg config.Config
    cfg.Main.Port, _ = configtypes.NewOptIntGreaterThanZero(5000)
    cfg.Environment = map[string]*config.EnvConfig{
        "Spree Project Production": &config.EnvConfig{
            SDKKey: config.SDKKey("SPREE_PROD_API_KEY"),
        },
    }
    return cfg
}
```

Alternatively, you can parse the configuration from a string that is in the same format as the configuration file, using the same `gcfg` package that ld-relay uses:

```go
import (
    "github.com/launchdarkly/ld-relay/v6/config"
    "gopkg.in/gcfg.v1"
)

var configString = `
[main]
port = 5000

[environment "Spree Project Production"]
sdkKey = "SPREE_PROD_API_KEY"
`

func createRelayConfig() config.Config {
    var cfg config.Config
    if err := gcfg.ReadStringInto(&cfg, configString); err != nil {
        log.Fatalf("Error loading config file: %s", err)
    }
    return cfg
}
```

If you want to shut down all Relay Proxy components, connections, goroutines, and port listeners while your application is still running, call the `Relay`'s `Close()` method. You are allowed to start a new `Relay` instance after doing this. (In fact, you can always start a new `Relay` instance even if one already exists, as long as they're not using the same port. However, there's normally no reason to do this.)
