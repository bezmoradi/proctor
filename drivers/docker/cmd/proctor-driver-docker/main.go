package main

import (
	"fmt"
	"os"

	"github.com/bezmoradi/proctor/protocol"
	"proctor/drivers/docker"
)

func main() {
	if err := protocol.Serve(docker.New()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
