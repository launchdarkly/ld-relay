//nolint:golint,stylecheck
package oshelpers

import (
	"io"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/launchdarkly/go-sdk-common.v2/ldlog"
)

type DirPath string

type CommandWrapper struct {
	command      string
	args         []string
	workDir      DirPath
	silent       bool
	outputWriter io.Writer
}

var Loggers ldlog.Loggers

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

func (c *CommandWrapper) OutputWriter(outputWriter io.Writer) *CommandWrapper {
	c.outputWriter = outputWriter
	return c
}

func (c *CommandWrapper) Run() error {
	cmd := c.initCommand()
	outputWriter := c.outputWriter
	if outputWriter == nil {
		outputWriter = newWriterToLogger(Loggers.ForLevel(ldlog.Info), ">>> ")
	}
	errLogger := newWriterToLogger(Loggers.ForLevel(ldlog.Info), "stderr >>> ")
	cmd.Stdout = outputWriter
	cmd.Stderr = errLogger
	err := cmd.Run()
	if c, ok := outputWriter.(io.Closer); ok {
		c.Close()
	}
	errLogger.Flush()
	return err
}

func (c *CommandWrapper) RunAndGetOutput() (string, error) {
	cmd := c.initCommand()
	errLogger := newWriterToLogger(Loggers.ForLevel(ldlog.Info), "stderr >>> ")
	cmd.Stderr = errLogger
	bytes, err := cmd.Output()
	if err == nil && !c.silent {
		Loggers.Infof(">>> %s", string(bytes))
	}
	errLogger.Flush()
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
		Loggers.Infof("Running (in %s): %s %s", path, c.command, strings.Join(c.args, " "))
	}
	cmd := exec.Command(c.command, c.args...) //nolint:gosec
	cmd.Dir = path
	return cmd
}
