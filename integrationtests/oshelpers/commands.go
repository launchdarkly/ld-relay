//nolint:golint,stylecheck
package oshelpers

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type DirPath string

type CommandWrapper struct {
	command string
	args    []string
	workDir DirPath
	silent  bool
}

func Command(command string, args ...string) *CommandWrapper {
	return &CommandWrapper{command: command, args: args}
}

func (c *CommandWrapper) WorkingDir(workDir DirPath) *CommandWrapper {
	c.workDir = workDir
	return c
}

func (c *CommandWrapper) ShowOutput(value bool) *CommandWrapper {
	c.silent = !value
	return c
}

func (c *CommandWrapper) Run() error {
	cmd := c.initCommand()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *CommandWrapper) RunAndGetOutput() (string, error) {
	cmd := c.initCommand()
	cmd.Stderr = os.Stderr
	bytes, err := cmd.Output()
	if err == nil && !c.silent {
		fmt.Print(string(bytes))
	}
	return string(bytes), err
}

func (c *CommandWrapper) initCommand() *exec.Cmd {
	path := string(c.workDir)
	if path == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			panic(err)
		}
	}
	if !c.silent {
		fmt.Printf("running (in %s): %s %s\n", path, c.command, strings.Join(c.args, " "))
	}
	cmd := exec.Command(c.command, c.args...) //nolint:gosec
	cmd.Dir = path
	return cmd
}
