package vnc

import (
	"context"
	"log/slog"
	"time"
)

// SuperviseConsole keeps the SSH service available while Windows has no desktop
// or the old capture process is being replaced. Each dial belongs to one worker.
func SuperviseConsole(parent context.Context, start func(context.Context) (Backend, error), log *slog.Logger) Backend {
	ctx, cancel := context.WithCancel(parent)
	type worker struct {
		backend  Backend
		lifetime context.Context
	}
	ready := make(chan worker)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for ctx.Err() == nil {
			backend, err := start(ctx)
			if err == nil {
				life, retire := context.WithCancel(ctx)
				current := worker{backend, life}
			serve:
				for {
					select {
					case ready <- current:
					case err = <-backend.Exited:
						break serve
					case <-ctx.Done():
						break serve
					}
				}
				retire()
				backend.Stop()
			}
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.Warn("vnc.console.retry", "error", err)
			} else {
				log.Info("vnc.console.restarting")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	return Backend{
		Stop: func() { cancel(); <-stopped },
		Dial: func(dialCtx context.Context) (Desktop, error) {
			select {
			case <-ctx.Done():
				return Desktop{}, ctx.Err()
			case <-dialCtx.Done():
				return Desktop{}, dialCtx.Err()
			case current := <-ready:
				desktop, err := current.backend.Dial(dialCtx)
				if err != nil {
					return Desktop{}, err
				}
				if err := current.lifetime.Err(); err != nil {
					desktop.Close()
					return Desktop{}, err
				}
				desktop.lifetime = current.lifetime
				return desktop, nil
			}
		},
	}
}
