package main

import (
	"fmt"
	"os"

	"proctor/drivers/sqs"
	"proctor/protocol"
)

func main() {
	d := sqs.New()
	err := protocol.Serve(d)
	d.Shutdown()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
