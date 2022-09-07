//go:build integrationtests
// +build integrationtests

package integrationtests

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/launchdarkly/ld-relay/v6/integrationtests/docker"
	"github.com/launchdarkly/ld-relay/v6/integrationtests/oshelpers"
	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

const (
	relayDockerImageName   = "launchdarkly/ld-relay"
	relayPrivateGitRepoURL = "git@github.com:launchdarkly/ld-relay-private.git"
	tempCheckoutName       = "relay"
)

// Create or copy a local Docker container for running Relay. The relayTagOrSHA parameter can be:
//
// 1. an empty string, meaning we should build from the current working copy, creating a new image name.
// 2. a version tag, meaning we should use the published Docker image with that tag.
// 3. a Git commit SHA in the private Relay repository.
//
// The function returns the name of the container.
func getRelayDockerImage(relayTagOrSHA string, loggers ldlog.Loggers) (*docker.Image, error) {
	if relayTagOrSHA == "" {
		loggers.Info("Building Relay Docker image from current working copy")
		dir, err := getGitRepoBaseDir()
		if err != nil {
			return nil, err
		}
		return docker.NewImageBuilder(dir).Build()
	}

	if matched, _ := regexp.MatchString("[0-9]+\\.[0-9]+\\.[0-9]+.*", relayTagOrSHA); matched {
		// Try to get a published image tagged with this version.
		loggers.Infof("Using published Relay Docker image for version %s", relayTagOrSHA)
		tag := fmt.Sprintf("%s:%s", relayDockerImageName, relayTagOrSHA)
		return docker.PullImage(tag)
	}

	// Assume it is the SHA of a Git commit - try to check it out in a temporary directory
	loggers.Infof("Building Relay Docker image from private tag or branch: %s", relayTagOrSHA)
	path, err := os.MkdirTemp("", "relay-integration-test-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(path)
	if err := oshelpers.Command("git", "clone", relayPrivateGitRepoURL, tempCheckoutName).
		WorkingDir(oshelpers.DirPath(path)).Run(); err != nil {
		return nil, err
	}
	checkoutDir := oshelpers.DirPath(path + string(os.PathSeparator) + tempCheckoutName)
	return docker.NewImageBuilder(checkoutDir).Build()
}

func getGitRepoBaseDir() (oshelpers.DirPath, error) {
	out, err := oshelpers.Command("git", "rev-parse", "--show-toplevel").RunAndGetOutput()
	return oshelpers.DirPath(strings.TrimSpace(string(out))), err
}
