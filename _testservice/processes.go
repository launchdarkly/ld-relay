package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
)

func pidFileName(serviceName string) string {
	return fmt.Sprintf(".testservice-%s.pid", serviceName)
}

// This function launches ourself again in a new child process, with an "exec" argument that tells it
// what to do, and stores the PID in a file.
func startProcess(serviceName string, serviceArgs []string) error {
	pidFile := pidFileName(serviceName)
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		return fmt.Errorf("%s already exists", pidFile)
	}

	args := append([]string{"exec", serviceName}, serviceArgs...)
	cmd := exec.Command(os.Args[0], args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid

	if err := ioutil.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		fmt.Fprintln(os.Stderr, "couldn't write PID file - killing process")
		_ = cmd.Process.Kill()
		return err
	}

	return nil
}

func stopProcess(serviceName string) error {
	pidFile := pidFileName(serviceName)
	data, err := ioutil.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("%s not found", pidFile)
	}
	if err := os.Remove(pidFile); err != nil {
		return err
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return fmt.Errorf("invalid PID in %s", pidFile)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
