package ruk

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/ruk/internal/fakedaemon"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

type poolAcquisition struct {
	pc  *conn
	s   *stream
	err error
}

func acquireAsync(ctx context.Context, client *Client) <-chan poolAcquisition {
	done := make(chan poolAcquisition, 1)
	go func() {
		pc, s, _, err := client.pool.acquire(ctx)
		done <- poolAcquisition{pc: pc, s: s, err: err}
	}()
	return done
}

func awaitAcquisition(t *testing.T, done <-chan poolAcquisition) poolAcquisition {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("connection acquisition did not finish")
		return poolAcquisition{}
	}
}

func holdHandshakes(t *testing.T) (fakedaemon.Config, <-chan struct{}, func()) {
	t.Helper()
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	var calls atomic.Int64
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	cfg := fakedaemon.Config{BeforeHandshake: func() {
		if calls.Add(1) > 1 {
			entered <- struct{}{}
			<-release
		}
	}}
	return cfg, entered, unblock
}

func awaitHandshake(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not receive the pending handshake")
	}
}

func TestPoolCountsPendingDials(t *testing.T) {
	cfg, entered, unblock := holdHandshakes(t)
	defer unblock()
	socket, _ := startFake(t, cfg)
	t.Cleanup(unblock)
	client := dialTest(t, socket, WithPool(2, 1))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	first, s, _, err := client.pool.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.release(s)
	secondDone := acquireAsync(ctx, client)
	awaitHandshake(t, entered)

	waitCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	thirdDone := acquireAsync(waitCtx, client)
	select {
	case <-entered:
		t.Error("a dial exceeded the two-connection limit")
		stop()
	case third := <-thirdDone:
		if !errors.Is(third.err, context.DeadlineExceeded) {
			t.Errorf("full pool returned %v, want deadline exceeded", third.err)
		}
		if third.s != nil {
			third.pc.release(third.s)
		}
	}

	first.release(s)
	reuseCtx, stopReuse := context.WithTimeout(ctx, time.Second)
	defer stopReuse()
	reused := awaitAcquisition(t, acquireAsync(reuseCtx, client))
	if reused.err != nil {
		t.Errorf("idle connection was blocked by a pending dial: %v", reused.err)
	} else {
		if reused.pc != first {
			t.Error("acquisition did not reuse the idle connection")
		}
		reused.pc.release(reused.s)
	}
	unblock()
	second := awaitAcquisition(t, secondDone)
	if second.err != nil {
		t.Fatalf("reserved dial failed: %v", second.err)
	}
	second.pc.release(second.s)
}

type waitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestPoolWakesOnConnectionFailure(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{})
	client := dialTest(t, socket, WithPool(1, 1))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	first, s, _, err := client.pool.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.release(s)
	waitCtx := &waitingContext{Context: ctx, waiting: make(chan struct{})}
	done := acquireAsync(waitCtx, client)
	select {
	case <-waitCtx.waiting:
	case got := <-done:
		t.Fatalf("full pool did not wait: %v", got.err)
	case <-time.After(time.Second):
		t.Fatal("call did not wait for a connection")
	}
	if err := first.nc.Close(); err != nil {
		t.Fatal(err)
	}
	next := awaitAcquisition(t, done)
	if next.err != nil {
		t.Fatalf("queued call did not recover from connection loss: %v", next.err)
	}
	if next.pc == first {
		t.Error("queued call reused a failed connection")
	}
	next.pc.release(next.s)
}

func TestPoolCloseCancelsPendingDial(t *testing.T) {
	cfg, entered, unblock := holdHandshakes(t)
	defer unblock()
	socket, _ := startFake(t, cfg)
	t.Cleanup(unblock)
	client := dialTest(t, socket, WithPool(2, 1))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pc, s, _, err := client.pool.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.release(s)
	pending := acquireAsync(ctx, client)
	awaitHandshake(t, entered)
	closed := make(chan struct{})
	go func() {
		_ = client.Close()
		close(closed)
	}()
	got := awaitAcquisition(t, pending)
	if !errors.Is(got.err, ErrClosed) {
		t.Errorf("pending dial returned %v, want client closed", got.err)
	}
	if got.s != nil {
		got.pc.release(got.s)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after canceling its pending dial")
	}
}

func TestCloseRejectsAllClientEntrypoints(t *testing.T) {
	socket, _ := startFake(t, fakedaemon.Config{})
	client := dialTest(t, socket)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	repo, err := client.OpenRepo(ctx, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ensureReplaySession(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		run  func(context.Context) error
		name string
	}{
		{run: func(ctx context.Context) error {
			_, err := client.OpenRepo(ctx, "/repo")
			return err
		}, name: "cached repo"},
		{run: func(ctx context.Context) error {
			_, err := Schema(ctx, client, repo, wire.SchemaArgs{})
			return err
		}, name: "typed call"},
		{run: func(ctx context.Context) error {
			_, _, err := RunCommand(ctx, client, Command{
				Open:   wire.ServiceOpen{Service: "upload-pack", Repository: "/repo", Advertise: true},
				Stdout: io.Discard,
			})
			return err
		}, name: "command"},
		{run: func(ctx context.Context) error {
			_, _, err := RunCommandStream(ctx, client, StreamCommand{
				Open:   wire.ServiceOpen{Service: "upload-pack", Repository: "/repo"},
				Stdin:  strings.NewReader("0000"),
				Stdout: io.Discard,
			})
			return err
		}, name: "stream command"},
		{run: func(ctx context.Context) error {
			_, err := Archive(ctx, client, repo, ArchiveArgs{Rev: ArchiveRefRev("refs/heads/main")}, io.Discard)
			return err
		}, name: "archive"},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			callCtx, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			if err := step.run(callCtx); !errors.Is(err, ErrClosed) {
				t.Errorf("call after Close returned %v, want client closed", err)
			}
		})
	}
}

func TestCloseCancelsDedicatedHandshake(t *testing.T) {
	cfg, entered, unblock := holdHandshakes(t)
	defer unblock()
	socket, _ := startFake(t, cfg)
	t.Cleanup(unblock)
	client := dialTest(t, socket)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := RunCommand(ctx, client, Command{
			Open:   wire.ServiceOpen{Service: "upload-pack", Repository: "/repo", Advertise: true},
			Stdout: io.Discard,
		})
		result <- err
	}()
	awaitHandshake(t, entered)
	closed := make(chan struct{})
	go func() {
		_ = client.Close()
		close(closed)
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("dedicated dial returned %v, want client closed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dedicated handshake survived Close")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after canceling the dedicated handshake")
	}
}

func TestCloseStopsDedicatedCommand(t *testing.T) {
	opened := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	socket, _ := startFake(t, fakedaemon.Config{ValidRepo: func(_ string) (string, string) {
		close(opened)
		<-release
		return "handle", ""
	}})
	t.Cleanup(unblock)
	client := dialTest(t, socket)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := RunCommand(ctx, client, Command{
			Open:   wire.ServiceOpen{Service: "upload-pack", Repository: "/repo", Advertise: true},
			Stdout: io.Discard,
		})
		result <- err
	}()
	select {
	case <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not receive the dedicated command")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("command returned %v, want client closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dedicated command survived Close")
	}
}
