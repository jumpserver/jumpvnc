package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTrayServiceRestartsAfterCleanup(t *testing.T) {
	started := make(chan context.Context, 2)
	cleanup := make(chan struct{})
	finishCleanup := sync.OnceFunc(func() { close(cleanup) })
	updates := make(chan struct{}, 4)
	service := newTrayService(t.Context(), func(ctx context.Context, ready func()) error {
		started <- ctx
		ready()
		<-ctx.Done()
		<-cleanup
		return ctx.Err()
	}, func() { updates <- struct{}{} })
	t.Cleanup(service.close)
	t.Cleanup(finishCleanup)

	service.start()
	awaitServiceState(t, service, updates, serviceRunning)
	first := <-started
	service.stop(true)
	service.start()
	service.update()
	if service.state != serviceRestarting || first.Err() == nil {
		t.Fatal("restart must cancel the previous run and wait for cleanup")
	}
	select {
	case <-started:
		t.Fatal("a second run started before cleanup completed")
	default:
	}
	finishCleanup()
	awaitServiceState(t, service, updates, serviceRunning)
	second := <-started
	if second == first || second.Err() != nil {
		t.Fatal("restart did not create a new active run")
	}
	service.stop(false)
	awaitServiceState(t, service, updates, serviceStopped)
	if second.Err() == nil || service.lastErr != nil {
		t.Fatalf("stop should cancel the run without recording an error, got %v", service.lastErr)
	}
}

func TestTrayServiceCanStartAfterFailure(t *testing.T) {
	failure := errors.New("invalid configuration")
	first := true
	updates := make(chan struct{}, 4)
	service := newTrayService(t.Context(), func(ctx context.Context, ready func()) error {
		if first {
			first = false
			return failure
		}
		ready()
		<-ctx.Done()
		return ctx.Err()
	}, func() { updates <- struct{}{} })
	t.Cleanup(service.close)

	service.start()
	awaitServiceState(t, service, updates, serviceStopped)
	if !errors.Is(service.lastErr, failure) {
		t.Fatalf("startup error = %v, want %v", service.lastErr, failure)
	}
	service.start()
	awaitServiceState(t, service, updates, serviceRunning)
	if service.lastErr != nil {
		t.Fatalf("startup error remained after a successful start: %v", service.lastErr)
	}
}

func awaitServiceState(t *testing.T, service *trayService, updates <-chan struct{}, want serviceState) {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		service.update()
		if service.state == want {
			return
		}
		select {
		case <-updates:
		case <-timeout.C:
			t.Fatalf("service state = %v, want %v", service.state, want)
		}
	}
}
