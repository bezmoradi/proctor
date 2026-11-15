package main

import (
	"fmt"
	"os"

	"proctor/drivers/ws"
	"proctor/protocol"
)

func main() {
	d := ws.New()
	err := protocol.Serve(d)
	d.Shutdown()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
