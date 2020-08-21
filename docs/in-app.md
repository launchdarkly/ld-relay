# LaunchDarkly Relay Proxy - Building within an application

[(Back to README)](../README.md)

If you need to customize the Relay Proxy's behavior and runtime environment in ways that the usual configuration settings don't support, you can incorporate the Relay Proxy code into your own application and let it provide service endpoints on a server that you configure.

You will need to import two packages, one from this repository and one from the `ld-relay-config` repository:

```go
import (
    config "github.com/launchdarkly/ld-relay-config"
    "github.com/launchdarkly/ld-relay/v6/relay"
)
```

The `ld-relay-config` package contains the types and functions for setting up the Relay Proxy configuration; the `relay` package contains the constructor for the Relay Proxy itself.

Here is an example of how you might run the Relay Proxy endpoints inside your web server beneath a path called `/relay`, using [Gorilla](https://github.com/gorilla/mux) to set up the service routes.

```go
import (
    config "github.com/launchdarkly/ld-relay-config"
    "github.com/launchdarkly/ld-relay/v6/relay"
)

func createRelayConfig() config.Config {
    var cfg config.Config
    if err := config.LoadConfigFile(&cfg, "path/to/my.config"); err != nil {
        log.Fatalf("Error loading config file: %s", err)
    }
    return cfg
}

r, err := relay.NewRelay(createRelayConfig, nil)
if err != nil {
    log.Fatalf("Error creating relay: %s", err)
}

router := mux.NewRouter()
router.PathPrefix("/relay").Handler(r)
```

The above example uses a configuration file. You can also pass in a `config.Config` struct that you have filled in directly:

```go
func createRelayConfig() config.Config {
    var cfg config.Config
    cfg.Main.Port = 5000
    cfg.Environment = map[string]*config.EnvConfig{
        "Spree Project Production": &config.EnvConfig{
            SDKKey: "SPREE_PROD_API_KEY",
        },
    }
    return cfg
}
```

Alternatively, you can parse the configuration from a string that is in the same format as the configuration file, using the same `gcfg` package that ld-relay uses:

```go
import "github.com/launchdarkly/gcfg"

configString := `
[main]
port = 5000

[environment "Spree Project Production"]
sdkKey = "SPREE_PROD_API_KEY"
`

func createRelayConfig() config.Config {
    cfg := config.DefaultConfig
    if err := gcfg.ReadStringInto(&cfg, configString); err != nil {
        log.Fatalf("Error loading config file: %s", err)
    }
    if err := config.ValidateConfig(&cfg); err != nil {
        log.Fatalf("Invalid configuration: %s", err)
    }
    return cfg
}
```
