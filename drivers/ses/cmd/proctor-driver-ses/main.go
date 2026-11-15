package main

import (
	"fmt"
	"os"

	"proctor/drivers/ses"
	"proctor/protocol"
)

func main() {
	if err := protocol.Serve(ses.New()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
