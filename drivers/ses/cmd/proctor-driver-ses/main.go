package main

import (
	"fmt"
	"os"

	"github.com/bezmoradi/proctor/protocol"
	"proctor/drivers/ses"
)

func main() {
	if err := protocol.Serve(ses.New()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
