package slog

import (
	"log/slog"
	"os"

	"github.com/bezmoradi/proctor/examples/sample/internal/core/shared"

	"go.opentelemetry.io/contrib/bridges/otelslog"
)

const scope = "github.com/bezmoradi/proctor/examples/sample"

type logger struct {
	logger *slog.Logger
}

func New() shared.Logger {
	return logger{
		logger: slog.New(slog.NewMultiHandler(
			slog.NewTextHandler(os.Stdout, nil),
			otelslog.NewHandler(scope),
		)),
	}
}

func (l logger) Info(message string, args ...any) {
	l.logger.Info(message, args...)
}

func (l logger) Warn(message string, args ...any) {
	l.logger.Warn(message, args...)
}

func (l logger) Error(message string, args ...any) {
	l.logger.Error(message, args...)
}

func (l logger) Fatal(err error) {
	l.logger.Error(err.Error())
	os.Exit(1)
}
