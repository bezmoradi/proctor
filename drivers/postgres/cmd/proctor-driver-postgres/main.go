package main

import (
	"fmt"
	"os"

	"proctor/drivers/postgres"
	"proctor/protocol"
)

func main() {
	d := postgres.New()
	err := protocol.Serve(d)
	d.Shutdown()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
