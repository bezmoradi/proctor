package observability

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bezmoradi/proctor/examples/sample/internal/core/shared"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const serviceVersion = "0.1.0"

const errorLogInterval = time.Minute

func noop(context.Context) error { return nil }

func Setup(ctx context.Context, logger shared.Logger) (func(context.Context) error, error) {
	otel.SetErrorHandler(&errorLogger{logger: logger})

	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.instance.id", uuid.NewString())),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(attribute.String("service.version", serviceVersion)),
	)
	if err != nil {
		return noop, err
	}

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	traceExporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return noop, err
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)

	logExporter, err := otlploghttp.New(ctx)
	if err != nil {
		return tracerProvider.Shutdown, err
	}
	loggerProvider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		sdklog.WithResource(res),
	)
	otel.SetLoggerProvider(loggerProvider)

	shutdown := func(ctx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(ctx), loggerProvider.Shutdown(ctx))
	}

	metricExporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return shutdown, err
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	shutdown = func(ctx context.Context) error {
		return errors.Join(tracerProvider.Shutdown(ctx), loggerProvider.Shutdown(ctx), meterProvider.Shutdown(ctx))
	}

	if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
		return shutdown, err
	}

	return shutdown, nil
}

type errorLogger struct {
	logger     shared.Logger
	mu         sync.Mutex
	last       time.Time
	suppressed int
}

func (e *errorLogger) Handle(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if time.Since(e.last) < errorLogInterval {
		e.suppressed++
		return
	}
	e.logger.Error("opentelemetry error", "error", err, "suppressed", e.suppressed)
	e.last = time.Now()
	e.suppressed = 0
}
