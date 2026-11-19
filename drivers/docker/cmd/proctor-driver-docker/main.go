package main

import (
	"fmt"
	"os"

	"proctor/drivers/docker"
	"proctor/protocol"
)

func main() {
	if err := protocol.Serve(docker.New()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
