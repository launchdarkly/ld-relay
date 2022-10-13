package docker

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/launchdarkly/ld-relay/v6/integrationtests/oshelpers"

	"github.com/launchdarkly/go-sdk-common/v3/ldvalue"

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
	imageName       string
	name            string
	params          []string
	containerParams []string
}

type Network struct {
	name string
	host string
}

var (
	defaultWriter = oshelpers.NewLogWriter(os.Stdout, "Docker") //nolint:gochecknoglobals
)

func command(cmd string, args ...string) *oshelpers.CommandWrapper { //nolint:unparam
	return oshelpers.Command(cmd, args...).OutputWriter(defaultWriter)
}

func NewImageBuilder(workDir oshelpers.DirPath) *ImageBuilder {
	return &ImageBuilder{workDir: workDir}
}

func (ib *ImageBuilder) Build() (*Image, error) {
	name := uuid.New()
	if err := command("docker", "build", "-t", name, ".").WorkingDir(ib.workDir).Run(); err != nil {
		return nil, err
	}
	return &Image{name: name, customBuild: true}, nil
}

func PullImage(name string) (*Image, error) {
	if err := command("docker", "pull", name).Run(); err != nil {
		return nil, err
	}
	return &Image{name: name, customBuild: false}, nil
}

func (i *Image) IsCustomBuild() bool {
	return i.customBuild
}

func (i *Image) Delete() error {
	return command("docker", "image", "rm", i.name).Run()
}

func (i *Image) NewContainerBuilder() *ContainerBuilder {
	return &ContainerBuilder{imageName: i.name}
}

func (cb *ContainerBuilder) Name(name string) *ContainerBuilder {
	cb.name = name
	return cb.args("--name", name)
}

func (cb *ContainerBuilder) EnvVar(name, value string) *ContainerBuilder {
	return cb.args("-e", fmt.Sprintf("%s=%s", name, value))
}

func (cb *ContainerBuilder) PublishPort(externalPort, internalPort int) *ContainerBuilder {
	return cb.args("-p", fmt.Sprintf("%d:%d", externalPort, internalPort))
}

func (cb *ContainerBuilder) Network(network *Network) *ContainerBuilder {
	if network != nil {
		return cb.args("--network", network.name)
	}
	return cb
}

func (cb *ContainerBuilder) SharedVolume(hostDir, containerDir string) *ContainerBuilder {
	return cb.args("-v", fmt.Sprintf("%s:%s", hostDir, containerDir))
}

func (cb *ContainerBuilder) ContainerParams(args ...string) *ContainerBuilder {
	cb.containerParams = append(cb.containerParams, args...)
	return cb
}

func (cb *ContainerBuilder) args(args ...string) *ContainerBuilder {
	cb.params = append(cb.params, args...)
	return cb
}

func (cb *ContainerBuilder) Build() (*Container, error) {
	args := []string{"create"}
	args = append(args, cb.params...)
	args = append(args, cb.imageName)
	args = append(args, cb.containerParams...)
	out, err := command("docker", args...).RunAndGetOutput()
	if err != nil {
		return nil, err
	}
	containerID := strings.TrimSpace(out)
	return &Container{
		id:   containerID,
		name: cb.name,
	}, nil
}

func (cb *ContainerBuilder) Run() error {
	args := []string{"run"}
	args = append(args, cb.params...)
	args = append(args, cb.imageName)
	args = append(args, cb.containerParams...)
	return command("docker", args...).OutputWriter(oshelpers.NewLogWriter(os.Stdout, cb.imageName)).Run()
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
	return command("docker", "stop", c.id).Run()
}

func (c *Container) Delete() error {
	return command("docker", "rm", c.id).Run()
}

func (c *Container) FollowLogs(outputWriter io.Writer) error {
	return command("docker", "logs", "--follow", c.id).OutputWriter(outputWriter).Run()
	// docker logs continues to run, piping the container's output to stdout, until the container is killed
}

func (c *Container) CommandInContainer(commandLine ...string) *oshelpers.CommandWrapper {
	args := []string{"exec", c.id}
	args = append(args, commandLine...)
	return command("docker", args...)
}

func NewNetwork() (*Network, error) {
	name := "network-" + uuid.New()
	if err := command("docker", "network", "create", name).Run(); err != nil {
		return nil, err
	}
	// The template expression after -f extracts result['IPAM']['Config'][0]['Gateway']
	out, err := command("docker", "network", "inspect", name, "-f", "{{(index .IPAM.Config 0).Gateway}}").
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
	return command("docker", "network", "rm", n.name).Run()
}

func (n *Network) GetContainerIDs() ([]string, error) {
	out, err := command("docker", "network", "inspect", n.name).ShowOutput(false).RunAndGetOutput()
	if err != nil {
		return nil, err
	}
	return ldvalue.Parse([]byte(out)).GetByIndex(0).GetByKey("Containers").Keys(nil), nil
}
