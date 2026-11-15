package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"proctor/simulators/ses"
)

type list []string

func (l *list) String() string { return strings.Join(*l, ",") }

func (l *list) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18193", "address to listen on")
	var identities list
	flag.Var(&identities, "identity", "verified sender identity, a domain or an address (repeatable or comma-separated); none means every sender is accepted")
	flag.Parse()
	logger := log.New(os.Stderr, "proctor-sim-ses: ", log.LstdFlags|log.LUTC)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	srv := &http.Server{
		Handler:           ses.New(identities, logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	logger.Printf("listening on http://%s (verified identities: %s)", ln.Addr(), describe(identities))
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func describe(ids list) string {
	if len(ids) == 0 {
		return "any sender"
	}
	return ids.String()
}
