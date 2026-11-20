package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	simhttp "proctor/simulators/http"
)

var Version = "0.0.0-dev"

func main() {
	addr := flag.String("addr", "127.0.0.1:18194", "address to listen on")
	routesFile := flag.String("routes", "", "the route file (required)")
	certFile := flag.String("cert", "", "TLS: your own PEM certificate (with --key)")
	keyFile := flag.String("key", "", "TLS: your own PEM private key (with --cert)")
	tlsNames := flag.String("tls-name", "", "TLS: comma-separated host names and IP addresses for a certificate made at start (with --ca-out)")
	caOut := flag.String("ca-out", "", "TLS: where to write the CA certificate that signs the --tls-name certificate")
	flag.Parse()
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "proctor-sim-http: "+format+"\n", args...)
		os.Exit(2)
	}
	if flag.NArg() > 0 {
		fail("unexpected argument %q", flag.Arg(0))
	}
	if *routesFile == "" {
		fail("--routes FILE is required")
	}
	switch {
	case (*certFile == "") != (*keyFile == ""):
		fail("--cert and --key go together")
	case (*tlsNames == "") != (*caOut == ""):
		fail("--tls-name and --ca-out go together")
	case *certFile != "" && *tlsNames != "":
		fail("--cert/--key and --tls-name/--ca-out are two ways to serve TLS; give one")
	}
	routes, err := simhttp.Load(*routesFile)
	if err != nil {
		fail("%s: %v", *routesFile, err)
	}
	logger := log.New(os.Stderr, "proctor-sim-http: ", log.LstdFlags|log.LUTC)

	var tlsConfig *tls.Config
	switch {
	case *certFile != "":
		cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
		if err != nil {
			fail("--cert/--key: %v", err)
		}
		tlsConfig = simhttp.ServerTLS(cert)
	case *tlsNames != "":
		dns, ips, err := simhttp.SplitNames(*tlsNames)
		if err != nil {
			fail("%v", err)
		}
		cert, caPEM, err := simhttp.NewCA(dns, ips)
		if err != nil {
			fail("making the TLS certificate: %v", err)
		}
		if err := simhttp.WriteCA(*caOut, caPEM); err != nil {
			fail("%v", err)
		}
		tlsConfig = simhttp.ServerTLS(cert)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{
		Handler:           simhttp.New(routes, Version, logger),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shut); err != nil {
			_ = srv.Close()
		}
	}()
	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
		ln = tls.NewListener(ln, tlsConfig)
	}
	logger.Printf("version %s listening on %s://%s (%d routes from %s, sha256 %s)", Version, scheme, ln.Addr(), len(routes.List), *routesFile, routes.SHA256)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	<-done
	logger.Printf("stopped")
}
