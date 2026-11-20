package server

import (
	"context"
	"fmt"
	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/handlers/auth"
	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/handlers/health"
	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/handlers/middleware"
	"github.com/bezmoradi/proctor/examples/sample/internal/adapters/observability"
	"github.com/bezmoradi/proctor/examples/sample/internal/core/shared"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func Serve(ctx context.Context, app http.Handler, logger shared.Logger, checks ...health.Check) error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	otelShutdown, err := observability.Setup(context.Background(), logger)
	if err != nil {
		logger.Error("otel setup failed; continuing without telemetry", "error", err)
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = otelShutdown(flushCtx)
	}()

	authConfig, err := auth.LoadConfig()
	if err != nil {
		return fmt.Errorf("auth config: %w", err)
	}

	allowedOrigins := middleware.AllowedOrigins()
	crossOrigin, err := middleware.CrossOrigin(allowedOrigins)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", health.Live)
	mux.HandleFunc("/readyz", health.Ready(logger, checks...))

	mux.Handle("/login", otelhttp.NewHandler(middleware.Chain(
		auth.LoginHandler(authConfig, logger),
		middleware.Recover(logger),
		middleware.RequestLogger(logger),
		middleware.CORS(allowedOrigins),
		crossOrigin,
		middleware.MaxBytes(middleware.MaxBodyBytes),
		middleware.RequireJSON,
	), "login"))
	mux.Handle("/", otelhttp.NewHandler(middleware.Chain(
		app,
		middleware.Recover(logger),
		middleware.RequestLogger(logger),
		middleware.CORS(allowedOrigins),
		crossOrigin,
		middleware.MaxBytes(middleware.MaxBodyBytes),
		auth.Middleware(authConfig),
		middleware.RequireJSON,
		middleware.Timeout(10*time.Second),
	), "http.server", otelhttp.WithSpanNameFormatter(spanName)))

	server := &http.Server{
		Addr:         fmt.Sprintf(":%v", port),
		Handler:      middleware.SecurityHeaders(mux),
		IdleTimeout:  time.Minute,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	logger.Info(fmt.Sprintf("The API is accessible at http://localhost:%s/api/v1/users", port))

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

func spanName(_ string, r *http.Request) string {
	return r.Method + " " + routeTemplate(r.URL.Path)
}

func routeTemplate(path string) string {
	const users = "/api/v1/users"

	switch {
	case path == users || path == users+"/":
		return users
	case strings.HasPrefix(path, users+"/"):
		return users + "/{id}"
	default:
		return "/"
	}
}
