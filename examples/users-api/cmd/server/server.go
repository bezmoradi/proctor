package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
	"users-api/internal/adapters/handlers/health"
	"users-api/internal/adapters/handlers/middleware"
	"users-api/internal/core/shared"
)

// Serve mounts the application handler behind the shared middleware chain, next
// to the health probes, and runs the server until ctx ends, then drains it. checks
// are the dependencies /readyz probes. It returns an error if the server can't
// start or stops unexpectedly.
func Serve(ctx context.Context, app http.Handler, logger shared.Logger, checks ...health.Check) error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Browsers may call the app cross-origin only from CORS_ALLOWED_ORIGINS; the
	// same list is trusted by the cross-origin (CSRF) protection.
	allowedOrigins := middleware.AllowedOrigins()
	crossOrigin, err := middleware.CrossOrigin(allowedOrigins)
	if err != nil {
		return err
	}

	// Mount liveness/readiness at the transport layer so they work regardless of
	// the HTTP framework, then delegate the application routes to it.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health.Live)
	mux.HandleFunc("/readyz", health.Ready(logger, checks...))
	mux.Handle("/", middleware.Chain(
		app,
		middleware.Recover(logger),
		middleware.RequestLogger(logger),
		middleware.CORS(allowedOrigins),
		crossOrigin,
		middleware.MaxBytes(middleware.MaxBodyBytes),
		middleware.RequireJSON,
		middleware.Timeout(10*time.Second),
	))

	server := &http.Server{
		Addr:         fmt.Sprintf(":%v", port),
		Handler:      middleware.SecurityHeaders(mux),
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	logger.Info(fmt.Sprintf("The API is accessible at http://localhost:%s/api/v1/users", port))
	// ListenAndServe only returns before Shutdown on a failure, such as the port
	// being taken.
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}
