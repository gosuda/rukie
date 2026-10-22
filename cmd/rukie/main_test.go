package main

import (
	"context"
	"testing"

	"github.com/gosuda/rukie/internal/config"
)

type readySupervisorStub struct{}

func (readySupervisorStub) WaitReady(context.Context) error { return nil }

func TestWaitSupervisorReadyDoesNotWaitForExitAfterReady(t *testing.T) {
	exited := make(chan struct{})
	waitCalled := make(chan struct{}, 1)
	cfg := config.Defaults()
	if err := waitSupervisorReady(t.Context(), readySupervisorStub{}, exited, func() error {
		waitCalled <- struct{}{}
		return nil
	}, cfg); err != nil {
		t.Fatalf("waitSupervisorReady: %v", err)
	}
	select {
	case <-waitCalled:
		t.Fatal("waitSupervisorReady waited for supervisor exit after readiness")
	default:
	}
}
