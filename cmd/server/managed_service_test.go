package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeServiceBackend struct {
	mu            sync.Mutex
	state         serviceState
	starts, stops int
	startErr      error
	stopHook      func(context.Context) error
}

func (b *fakeServiceBackend) query() (serviceState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, nil
}
func (b *fakeServiceBackend) start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	b.starts++
	if b.startErr != nil {
		return b.startErr
	}
	b.state = serviceRunning
	return nil
}
func (b *fakeServiceBackend) stop(ctx context.Context) error {
	if b.stopHook != nil {
		if err := b.stopHook(ctx); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stops++
	b.state = serviceStopped
	return nil
}
func managedFixture(t *testing.T, backend *fakeServiceBackend) (*managedService, <-chan struct{}) {
	t.Helper()
	updates := make(chan struct{}, 4)
	s := newManagedService(t.Context(), backend, func() { updates <- struct{}{} })
	t.Cleanup(s.close)
	return s, updates
}
func awaitManagedOperation(t *testing.T, s *managedService, updates <-chan struct{}) {
	t.Helper()
	select {
	case <-updates:
	case <-time.After(5 * time.Second):
		t.Fatal("management operation timed out")
	}
	s.update()
}
func TestServiceManagerAttachAndExitLeaveServiceRunning(t *testing.T) {
	b := &fakeServiceBackend{state: serviceRunning}
	s, _ := managedFixture(t, b)
	if s.currentState() != serviceRunning || !s.canStop() || s.canStart() {
		t.Fatal("manager did not attach to existing state")
	}
	s.close()
	if b.starts != 0 || b.stops != 0 || b.state != serviceRunning {
		t.Fatal("opening or closing the tray changed the service lifetime")
	}
}
func TestServiceManagerStopStartRestartAndExternalChanges(t *testing.T) {
	b := &fakeServiceBackend{state: serviceRunning}
	s, updates := managedFixture(t, b)
	s.stop(true)
	if s.canStart() || s.canStop() {
		t.Fatal("concurrent operations must be disabled")
	}
	s.start() // A second click must not issue another request.
	awaitManagedOperation(t, s, updates)
	if b.starts != 1 || b.stops != 1 || s.currentState() != serviceRunning {
		t.Fatal("restart did not stop then start the installed service")
	}
	s.stop(false)
	awaitManagedOperation(t, s, updates)
	if !s.canStart() || s.currentState() != serviceStopped {
		t.Fatal("stop did not update the tray")
	}
	s.start()
	awaitManagedOperation(t, s, updates)
	b.mu.Lock()
	b.state = serviceStopped
	b.mu.Unlock()
	s.update()
	if s.currentState() != serviceStopped {
		t.Fatal("external service changes were not observed")
	}
}
func TestClosingServiceManagerCompletesRequestedRestart(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	b := &fakeServiceBackend{state: serviceRunning, stopHook: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	s, _ := managedFixture(t, b)
	s.stop(true)
	<-entered
	closed := make(chan struct{})
	go func() { s.close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("tray abandoned the pending restart")
	default:
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("tray did not finish closing")
	}
	if b.starts != 1 || b.stops != 1 || b.state != serviceRunning {
		t.Fatal("exiting during restart left the service stopped")
	}
}

func TestServiceManagerShowsStartFailureAndCanRetry(t *testing.T) {
	failure := errors.New("invalid service configuration")
	b := &fakeServiceBackend{state: serviceStopped, startErr: failure}
	s, updates := managedFixture(t, b)
	s.start()
	awaitManagedOperation(t, s, updates)
	if !errors.Is(s.currentError(), failure) || !s.canStart() {
		t.Fatal("startup failure must allow correction and retry")
	}
	b.startErr = nil
	s.start()
	awaitManagedOperation(t, s, updates)
	if s.currentError() != nil || s.currentState() != serviceRunning {
		t.Fatal("retry did not recover")
	}
}
