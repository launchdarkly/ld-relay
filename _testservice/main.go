package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
)

func runServiceCommand(commandName string, portStr string) error {
	var handler http.Handler
	switch commandName {
	case "streamer":
		handler = streamerEndpointHandler()
	default:
		return errors.New("unknown service name")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return errors.New("invalid port")
	}
	return runService(port, handler)
}

func maybeError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func main() {
	switch {
	case len(os.Args) == 4 && os.Args[1] == "exec":
		maybeError(runServiceCommand(os.Args[2], os.Args[3]))
		return
	case len(os.Args) >= 3 && os.Args[1] == "start":
		maybeError(startProcess(os.Args[2], os.Args[3:]))
		return
	case len(os.Args) == 3 && os.Args[1] == "stop":
		maybeError(stopProcess(os.Args[2]))
		return
	}

	fmt.Fprintln(os.Stderr, "see readme for usage")
	os.Exit(1)
}
