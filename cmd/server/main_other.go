//go:build !windows

package main

import (
	"context"
	"errors"
	"log/slog"
)

func runAgent(ctx context.Context, name string, _ bool, log *slog.Logger) error {
	return run(ctx, name, log, nil, nil)
}

func runWindowsService(context.Context, string) error {
	return errors.New("service mode requires Windows")
}

func runServiceManager(context.Context, bool, *slog.Logger) error {
	return errors.New("service management requires Windows")
}
