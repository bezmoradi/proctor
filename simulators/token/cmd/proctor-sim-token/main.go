package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var Version = "0.0.0-dev"

const (
	exitOK      = 0
	exitRuntime = 1
	exitConfig  = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	logger := log.New(stderr, "proctor-sim-token: ", log.LstdFlags|log.LUTC)

	fs := flag.NewFlagSet("proctor-sim-token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "", "address to listen on, host:port (required)")
	claimsPath := fs.String("claims", "", "path to the claims file (required)")
	secretEnv := fs.String("internal-secret-env", "", "name of the environment variable holding the expected X-Internal-Secret")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitConfig
	}
	secretSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "internal-secret-env" {
			secretSet = true
		}
	})

	if err := checkArgs(fs, *listen, *claimsPath); err != nil {
		logger.Print(err)
		return exitConfig
	}

	s := &sim{logger: logger}
	if secretSet {
		if *secretEnv == "" {
			logger.Print("--internal-secret-env needs an environment variable name")
			return exitConfig
		}
		v := os.Getenv(*secretEnv)
		if v == "" {
			logger.Printf("environment variable %s is not set or is empty", *secretEnv)
			return exitConfig
		}
		s.secret = []byte(v)
	}

	claims, err := loadClaims(*claimsPath)
	if err != nil {
		logger.Print(err)
		return exitConfig
	}
	s.claims.Store(&claims)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		logger.Printf("listen: %v", err)
		return exitRuntime
	}

	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          logger,
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	secretMode := "not checked"
	if s.secret != nil {
		secretMode = "required from " + *secretEnv
	}
	logger.Printf("simulated platform-service token check version %s listening on %s, %d tokens loaded, internal secret %s", Version, ln.Addr(), len(claims), secretMode)

	for {
		select {
		case err := <-serveErr:
			logger.Printf("serve: %v", err)
			return exitRuntime
		case sig := <-sigs:
			if sig == syscall.SIGHUP {
				reloaded, err := loadClaims(*claimsPath)
				if err != nil {
					logger.Printf("reload failed, keeping the previous claims: %v", err)
					continue
				}
				s.claims.Store(&reloaded)
				logger.Printf("reloaded claims file, %d tokens loaded", len(reloaded))
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := srv.Shutdown(ctx)
			cancel()
			if err != nil {
				logger.Printf("shutdown: %v", err)
				return exitRuntime
			}
			logger.Printf("stopped on %s", sig)
			return exitOK
		}
	}
}

func checkArgs(fs *flag.FlagSet, listen, claimsPath string) error {
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if listen == "" {
		return errors.New("--listen is required")
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("--listen: %v", err)
	}
	if host == "" {
		return errors.New("--listen needs an explicit host, for example 127.0.0.1:18191")
	}
	if claimsPath == "" {
		return errors.New("--claims is required")
	}
	return nil
}
