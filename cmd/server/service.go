package main

import (
	"context"
	"sync"
)

type serviceState int

const (
	serviceStopped serviceState = iota
	serviceStarting
	serviceRunning
	serviceStopping
	serviceRestarting
	serviceUnavailable
)

type trayController interface {
	canStart() bool
	canStop() bool
	start()
	stop(restart bool)
	update()
	close()
	currentState() serviceState
	currentError() error
}

func (s *trayService) currentState() serviceState { return s.state }
func (s *trayService) currentError() error        { return s.lastErr }

type serviceEvent struct {
	ready bool
	err   error
}

// The tray thread owns the service state. Workers only publish events.
type trayService struct {
	ctx    context.Context
	run    func(context.Context, func()) error
	notify func()
	events chan serviceEvent
	worker sync.WaitGroup

	state   serviceState
	cancel  context.CancelFunc
	lastErr error
}

func newTrayService(ctx context.Context, run func(context.Context, func()) error, notify func()) *trayService {
	// A run emits at most one ready event and one completion event. Both must
	// fit even if the tray thread is waiting for shutdown.
	return &trayService{ctx: ctx, run: run, notify: notify, events: make(chan serviceEvent, 2)}
}

func (s *trayService) canStart() bool {
	return s.state == serviceStopped && s.ctx.Err() == nil
}

func (s *trayService) canStop() bool {
	return s.state == serviceRunning || s.state == serviceStarting
}

func (s *trayService) start() {
	if !s.canStart() {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.state = serviceStarting
	s.cancel = cancel
	s.lastErr = nil
	s.worker.Go(func() {
		err := s.run(ctx, func() { s.publish(serviceEvent{ready: true}) })
		s.publish(serviceEvent{err: err})
	})
}

func (s *trayService) stop(restart bool) {
	if !s.canStop() {
		return
	}
	s.state = serviceStopping
	if restart {
		s.state = serviceRestarting
	}
	s.cancel()
}

func (s *trayService) publish(event serviceEvent) {
	s.events <- event
	s.notify()
}

func (s *trayService) update() {
	for {
		select {
		case event := <-s.events:
			if event.ready {
				if s.state == serviceStarting {
					s.state = serviceRunning
				}
				continue
			}
			previous := s.state
			s.cancel()
			s.cancel = nil
			s.state = serviceStopped
			switch previous {
			case serviceRestarting:
				s.start()
			case serviceStarting, serviceRunning:
				if s.ctx.Err() == nil {
					s.lastErr = event.err
				}
			}
		default:
			return
		}
	}
}

func (s *trayService) close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.worker.Wait()
}
