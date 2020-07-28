# LaunchDarkly Relay Proxy - Building Within an Application

[(back to README)](../README.md)

If you need to customize the Relay Proxy's behavior and runtime environment in ways that the usual configuration settings don't support, you can incorporate the Relay Proxy code into your own application and let it provide service endpoints on a server that you configure.

Here is an example of how you might run the Relay Proxy endpoints inside your web server beneath a path called `/relay`, using [Gorilla](https://github.com/gorilla/mux) to set up the service routes.

```go
import (
    relay "github.com/launchdarkly/ld-relay/v6"
    "github.com/launchdarkly/ld-relay/v6/config"
)

func createRelayConfig() config.Config {
    cfg := config.DefaultConfig
    if err := relay.LoadConfigFile(&cfg, "path/to/my.config"); err != nil {
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
    cfg := config.DefaultConfig
    cfg.Main.Port = 5000
    cfg.Environment = map[string]*config.EnvConfig{
        "Spree Project Production": &config.EnvConfig{
            SDKKey: "SPREE_PROD_API_KEY",
        },
    }
    return cfg
}
```

Or, you can parse the configuration from a string that is in the same format as the configuration file, using the same `gcfg` package that ld-relay uses:

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
    return cfg
}
```
