package main

import (
	"context"
	"sync"
	"time"
)

type serviceBackend interface {
	query() (serviceState, error)
	start(context.Context) error
	stop(context.Context) error
}

// This controller owns only management requests, never the service lifetime.
// The tray thread owns its state; workers return completed operations by channel.
type managedService struct {
	backend  serviceBackend
	ctx      context.Context
	cancel   context.CancelFunc
	notify   func()
	worker   sync.WaitGroup
	done     chan error
	state    serviceState
	busy     bool
	lastErr  error
	queryErr error
}

func newManagedService(ctx context.Context, backend serviceBackend, notify func()) *managedService {
	ctx, cancel := context.WithCancel(ctx)
	s := &managedService{backend: backend, ctx: ctx, cancel: cancel, notify: notify, done: make(chan error, 1)}
	s.update()
	return s
}

func (s *managedService) currentState() serviceState { return s.state }
func (s *managedService) currentError() error {
	if s.queryErr != nil {
		return s.queryErr
	}
	return s.lastErr
}
func (s *managedService) canStart() bool {
	return !s.busy && s.ctx.Err() == nil && s.state == serviceStopped
}
func (s *managedService) canStop() bool {
	return !s.busy && s.ctx.Err() == nil && s.state == serviceRunning
}
func (s *managedService) start() {
	if s.canStart() {
		s.perform(serviceStarting, s.backend.start)
	}
}
func (s *managedService) stop(restart bool) {
	if !s.canStop() {
		return
	}
	state := serviceStopping
	if restart {
		state = serviceRestarting
	}
	s.perform(state, func(ctx context.Context) error {
		if err := s.backend.stop(ctx); err != nil {
			return err
		}
		if restart && ctx.Err() == nil {
			return s.backend.start(ctx)
		}
		return ctx.Err()
	})
}
func (s *managedService) perform(state serviceState, action func(context.Context) error) {
	s.busy, s.state, s.lastErr = true, state, nil
	s.worker.Go(func() {
		ctx, cancel := context.WithTimeout(s.ctx, 45*time.Second)
		defer cancel()
		s.done <- action(ctx)
		s.notify()
	})
}
func (s *managedService) update() {
	select {
	case s.lastErr = <-s.done:
		s.busy = false
	default:
	}
	if !s.busy {
		s.state, s.queryErr = s.backend.query()
		if s.queryErr != nil {
			s.state = serviceUnavailable
		}
	}
}
func (s *managedService) close() {
	// Finish an already requested restart before exiting the tray. Cancelling
	// between Stop and Start would otherwise strand the service in Stopped.
	s.worker.Wait()
	s.cancel()
}
