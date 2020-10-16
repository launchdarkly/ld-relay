//nolint:golint,stylecheck
package docker

import (
	"fmt"
	"io"
	"strings"

	"github.com/launchdarkly/ld-relay/v6/integrationtests/oshelpers"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldvalue"

	"github.com/pborman/uuid"
)

// A programmatic interface to Docker commands via the shell. We're using this here instead of
// https://godoc.org/github.com/docker/docker/client for several reasons:
// - It's conceptually a bit simpler
// - The command output automatically gets echoed to the test log
// - Eliminates a transitive dependency when Relay is used as a module

type Image struct {
	name        string
	customBuild bool
}

type ImageBuilder struct {
	workDir oshelpers.DirPath
}

type Container struct {
	id   string
	name string
}

type ContainerBuilder struct {
	imageName string
	name      string
	params    []string
}

type Network struct {
	name string
	host string
}

func NewImageBuilder(workDir oshelpers.DirPath) *ImageBuilder {
	return &ImageBuilder{workDir: workDir}
}

func (ib *ImageBuilder) Build() (*Image, error) {
	name := uuid.New()
	if err := oshelpers.Command("docker", "build", "-t", name, ".").WorkingDir(ib.workDir).Run(); err != nil {
		return nil, err
	}
	return &Image{name: name, customBuild: true}, nil
}

func PullImage(name string) (*Image, error) {
	if err := oshelpers.Command("docker", "pull", name).Run(); err != nil {
		return nil, err
	}
	return &Image{name: name, customBuild: false}, nil
}

func (i *Image) IsCustomBuild() bool {
	return i.customBuild
}

func (i *Image) Delete() error {
	return oshelpers.Command("docker", "image", "rm", i.name).Run()
}

func (i *Image) NewContainerBuilder() *ContainerBuilder {
	return &ContainerBuilder{imageName: i.name}
}

func (cb *ContainerBuilder) Name(name string) *ContainerBuilder {
	cb.name = name
	cb.params = append(cb.params, "--name")
	cb.params = append(cb.params, name)
	return cb
}

func (cb *ContainerBuilder) EnvVar(name, value string) *ContainerBuilder {
	cb.params = append(cb.params, "-e")
	cb.params = append(cb.params, fmt.Sprintf("%s=%s", name, value))
	return cb
}

func (cb *ContainerBuilder) PublishPort(externalPort, internalPort int) *ContainerBuilder {
	cb.params = append(cb.params, "-p")
	cb.params = append(cb.params, fmt.Sprintf("%d:%d", externalPort, internalPort))
	return cb
}

func (cb *ContainerBuilder) Network(network *Network) *ContainerBuilder {
	if network != nil {
		cb.params = append(cb.params, "--network")
		cb.params = append(cb.params, network.name)
	}
	return cb
}

func (cb *ContainerBuilder) Build() (*Container, error) {
	args := []string{"create"}
	args = append(args, cb.params...)
	args = append(args, cb.imageName)
	out, err := oshelpers.Command("docker", args...).RunAndGetOutput()
	if err != nil {
		return nil, err
	}
	containerID := strings.TrimSpace(out)
	return &Container{
		id:   containerID,
		name: cb.name,
	}, nil
}

func (c *Container) GetID() string {
	return c.id
}

func (c *Container) GetName() string {
	return c.name
}

func (c *Container) Start() error {
	return oshelpers.Command("docker", "start", c.id).Run()
}

func (c *Container) Stop() error {
	return oshelpers.Command("docker", "stop", c.id).Run()
}

func (c *Container) Delete() error {
	return oshelpers.Command("docker", "rm", c.id).Run()
}

func (c *Container) FollowLogs(outputWriter io.Writer) error {
	return oshelpers.Command("docker", "logs", "--follow", c.id).OutputWriter(outputWriter).Run()
	// docker logs continues to run, piping the container's output to stdout, until the container is killed
}

func (c *Container) CommandInContainer(commandLine ...string) *oshelpers.CommandWrapper {
	args := []string{"exec", c.id}
	args = append(args, commandLine...)
	return oshelpers.Command("docker", args...)
}

func NewNetwork() (*Network, error) {
	name := "network-" + uuid.New()
	if err := oshelpers.Command("docker", "network", "create", name).Run(); err != nil {
		return nil, err
	}
	// The template expression after -f extracts result['IPAM']['Config'][0]['Gateway']
	out, err := oshelpers.Command("docker", "network", "inspect", name, "-f", "{{(index .IPAM.Config 0).Gateway}}").
		ShowOutput(false).RunAndGetOutput()
	if err != nil {
		return nil, err
	}
	host := strings.TrimSpace(out)
	return &Network{name: name, host: host}, nil
}

func (n *Network) GetName() string {
	return n.name
}

func (n *Network) Delete() error {
	return oshelpers.Command("docker", "network", "rm", n.name).Run()
}

func (n *Network) GetContainerIDs() ([]string, error) {
	out, err := oshelpers.Command("docker", "network", "inspect", n.name).RunAndGetOutput()
	if err != nil {
		return nil, err
	}
	return ldvalue.Parse([]byte(out)).GetByIndex(0).GetByKey("Containers").Keys(), nil
}
