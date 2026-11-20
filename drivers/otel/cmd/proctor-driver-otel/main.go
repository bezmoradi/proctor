package main

import (
	"fmt"
	"os"

	"github.com/bezmoradi/proctor/protocol"
	"proctor/drivers/otel"
)

func main() {
	if err := protocol.Serve(otel.New()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
