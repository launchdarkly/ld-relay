package main

import (
	"fmt"
	"net"
	"net/http"
)

func runService(port int, handler http.Handler) error {
	err := startService(port, handler)
	if err != nil {
		return err
	}
	select {} // sleep until we're killed
}

func startService(port int, handler http.Handler) error {
	// Doing Listen and Serve separately instead of ListenAndServe allows us to not exit till we know it's really listening.
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	return nil
}
