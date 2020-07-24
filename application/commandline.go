package application

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// DefaultConfigPath is the default configuration file path.
const DefaultConfigPath = "/etc/ld-relay.conf"

// Options represents all options that can be set from the command line.
type Options struct {
	ConfigFile       string
	AllowMissingFile bool
	UseEnvironment   bool
}

func errConfigFileNotFound(filename string) error {
	return fmt.Errorf("configuration file %q does not exist", filename)
}

// DescribeConfigSource returns a human-readable phrase describing whether the configuration comes from a
// file, from variables, or both.
func (o Options) DescribeConfigSource() string {
	if o.ConfigFile == "" && o.UseEnvironment {
		return "configuration from environment variables"
	}
	desc := ""
	if o.ConfigFile != "" {
		desc = fmt.Sprintf("configuration file %s", o.ConfigFile)
	}
	if o.UseEnvironment {
		desc += " plus environment variables"
	}
	return desc
}

// ReadOptions reads and validates the command-line options.
//
// The configuration parameter behavior is as follows:
// 1. If you specify --config $FILEPATH, it loads that file. Failure to find it or parse it is a fatal error,
//    unless you also specify --allow-missing-file.
// 2. If you specify --from-env, it creates a configuration from environment variables as described in README.
// 3. If you specify both, the file is loaded first, then it applies changes from variables if any.
// 4. Omitting all options is equivalent to explicitly specifying --config /etc/ld-relay.conf.
func ReadOptions() (Options, error) {
	var o Options

	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.StringVar(&o.ConfigFile, "config", "", "configuration file location")
	fs.BoolVar(&o.AllowMissingFile, "allow-missing-file", false, "suppress error if config file is not found")
	fs.BoolVar(&o.UseEnvironment, "from-env", false, "read configuration from environment variables")
	err := fs.Parse(os.Args)
	if err != nil {
		return o, err
	}

	if o.ConfigFile == "" && !o.UseEnvironment {
		o.ConfigFile = DefaultConfigPath
	}

	if o.ConfigFile != "" {
		_, err := os.Stat(o.ConfigFile)
		fileExists := err == nil || !os.IsNotExist(err)
		if !fileExists {
			if !o.AllowMissingFile {
				return o, errConfigFileNotFound(o.ConfigFile)
			}
			o.ConfigFile = ""
		}
	}

	return o, nil
}

// DescribeRelayVersion returns the same version string unless it is a prerelease build, in
// which case it is reformatted to change "+xxx" into "(build xxx)".
func DescribeRelayVersion(version string) string {
	split := strings.Split(version, "+")
	if len(split) == 2 {
		return fmt.Sprintf("%s (build %s)", split[0], split[1])
	}
	return version
}
