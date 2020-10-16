//nolint:golint,stylecheck
package oshelpers

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type DirPath string

type CommandWrapper struct {
	command      string
	args         []string
	workDir      DirPath
	silent       bool
	outputWriter io.Writer
}

func Command(command string, args ...string) *CommandWrapper {
	return &CommandWrapper{command: command, args: args, outputWriter: os.Stdout}
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
	cmd.Stdout = c.outputWriter
	err := cmd.Run()
	if w, ok := cmd.Stderr.(*LineParsingWriter); ok {
		w.Flush()
	}
	if w, ok := c.outputWriter.(*LineParsingWriter); ok {
		w.Flush()
	}
	return err
}

func (c *CommandWrapper) RunAndGetOutput() (string, error) {
	cmd := c.initCommand()
	bytes, err := cmd.Output()
	if w, ok := cmd.Stderr.(*LineParsingWriter); ok {
		w.Flush()
	}
	if err == nil && !c.silent {
		_, _ = c.outputWriter.Write([]byte(fmt.Sprintf(">>> %s", string(bytes))))
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
		_, _ = c.outputWriter.Write([]byte(fmt.Sprintf("Running (in %s): %s %s\n", path, c.command, strings.Join(c.args, " "))))
	}
	cmd := exec.Command(c.command, c.args...) //nolint:gosec
	cmd.Dir = path
	cmd.Stderr = NewLineParsingWriter(func(line string) { fmt.Fprintf(c.outputWriter, "stderr >>> %s\n", line) })
	return cmd
}
