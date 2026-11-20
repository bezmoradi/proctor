package main

import (
	"fmt"
	"os"

	"github.com/bezmoradi/proctor/protocol"
	"proctor/drivers/postgres"
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
