package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bezmoradi/proctor/protocol"
	"proctor/drivers/stub"
)

func main() {
	signal.Notify(make(chan os.Signal, 1), os.Interrupt, syscall.SIGTERM)
	d := stub.New()
	err := protocol.Serve(d)
	d.Shutdown()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
