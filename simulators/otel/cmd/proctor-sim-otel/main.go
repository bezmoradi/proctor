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
	"syscall"
	"time"

	"proctor/simulators/otel"
)

var Version = "0.0.0-dev"

func main() {
	addr := flag.String("http", "127.0.0.1:4318", "address for OTLP/HTTP (protobuf and JSON) and the read API")
	flag.Parse()
	logger := log.New(os.Stderr, "proctor-sim-otel: ", log.LstdFlags|log.LUTC)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:           otel.New(Version, logger),
		Protocols:         &protocols,
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
	logger.Printf("version %s listening on http://%s for OTLP/HTTP (POST %s, %s and %s; metrics are counted and discarded); keeps at most %d spans, %d log records and %d bytes of records; a request may hold %d bytes after decompression",
		Version, ln.Addr(), otel.TracesPath, otel.LogsPath, otel.MetricsPath, otel.MaxSpans, otel.MaxLogs, otel.MaxBytes, otel.MaxRequest)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
