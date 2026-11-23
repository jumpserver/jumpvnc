//go:build !windows

package main

import (
	"context"
	"log/slog"
)

func runAgent(ctx context.Context, name string, _ bool, log *slog.Logger) error {
	return run(ctx, name, log, nil)
}
