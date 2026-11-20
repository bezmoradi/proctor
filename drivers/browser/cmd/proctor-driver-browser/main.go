package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bezmoradi/proctor/protocol"
	"proctor/drivers/browser"
)

func main() {
	d := browser.New()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		d.Shutdown()
		os.Exit(1)
	}()
	err := protocol.Serve(d)
	d.Shutdown()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
