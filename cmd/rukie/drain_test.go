package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestHalveDeadlineHalvesRemainingOnce(t *testing.T) {
	_, hd := newHalveDeadline(context.Background(), 10*time.Second, errors.New("drain timed out"))
	defer hd.stop()
	start := time.Now()
	before := hd.deadline
	hd.halve()
	half := hd.deadline
	if !half.Before(before) {
		t.Fatal("halve did not shorten the deadline")
	}
	want := start.Add(5 * time.Second)
	if half.Sub(want) > 2*time.Second || want.Sub(half) > 2*time.Second {
		t.Fatalf("halved deadline = %v, want about %v (half the remaining 10s)", half, want)
	}
	hd.halve()
	if !hd.deadline.Equal(half) {
		t.Fatal("second halve moved the deadline; halving applies once")
	}
}

func TestHalveRegistryHalvesLateDeadlines(t *testing.T) {
	reg := &halveRegistry{}
	reg.halveAll()
	var calls int
	reg.add(func() { calls++ })
	if calls != 1 {
		t.Fatalf("late-registered halve ran %d times, want 1: a signal between stop and drain is not lost", calls)
	}
	reg.add(func() { calls++ })
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	reg.halveAll()
	if calls != 2 {
		t.Fatalf("calls = %d after a repeat halveAll, want 2 (once)", calls)
	}
}

func TestShutdownSecondSignalHalvesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, stop := context.WithCancel(t.Context())
		halves := &halveRegistry{}
		var calls int
		halves.add(func() { calls++ })
		serveDone := make(chan struct{})
		sigCh := make(chan os.Signal, 2)
		go watchShutdownSignals(sigCh, stop, halves, serveDone)

		sigCh <- syscall.SIGTERM
		synctest.Wait()
		if ctx.Err() == nil {
			t.Fatal("first signal did not stop serving")
		}

		supDone := make(chan error)
		waiterGot := make(chan error, 1)
		go func() {
			select {
			case err := <-supDone:
				waiterGot <- err
			case <-serveDone:
				waiterGot <- errors.New("serve returned while the supervisor was still draining")
			}
		}()

		sigCh <- syscall.SIGINT
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("halve ran %d times after the second signal, want 1", calls)
		}
		sigCh <- syscall.SIGINT
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("halve ran %d times after the third signal, want 1 (once only)", calls)
		}
		select {
		case err := <-waiterGot:
			t.Fatalf("waiter released on a second signal with %v; it must keep waiting for the child", err)
		default:
		}
		close(serveDone)
		synctest.Wait()
	})
}
