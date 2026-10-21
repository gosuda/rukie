package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestWriteRefusesDuringBackup(t *testing.T) {
	s := openTest(t, Options{Barrier: func() (func(), bool) { return func() {}, false }})
	ran := false
	err := s.Write(t.Context(), func(tx *WriteTx) error {
		ran = true
		return nil
	})
	if !errors.Is(err, ErrBackupInProgress) {
		t.Fatalf("err = %v", err)
	}
	if ran {
		t.Fatalf("write ran despite refused barrier")
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error { return nil }); err != nil {
		t.Fatalf("read during barrier refusal: %v", err)
	}
}

func TestWriteHoldsBarrierAcrossCommit(t *testing.T) {
	var held atomic.Int64
	var released atomic.Int64
	s := openTest(t, Options{Barrier: func() (func(), bool) {
		held.Add(1)
		return func() { released.Add(1) }, true
	}})
	err := s.Write(t.Context(), func(tx *WriteTx) error {
		if held.Load() != 1 {
			return errors.New("barrier not held during write")
		}
		if released.Load() != 0 {
			return errors.New("barrier released before commit")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if held.Load() != 1 || released.Load() != 1 {
		t.Fatalf("held=%d released=%d", held.Load(), released.Load())
	}
}

func TestWriteReleasesBarrierOnSubmitCancel(t *testing.T) {
	var released atomic.Int64
	s := openTest(t, Options{Barrier: func() (func(), bool) {
		return func() { released.Add(1) }, true
	}})
	ctx, stop := context.WithCancel(t.Context())
	stop()
	ran := false
	err := s.Write(ctx, func(tx *WriteTx) error {
		ran = true
		return nil
	})
	if err == nil {
		t.Fatalf("cancelled write succeeded")
	}
	if ran {
		t.Fatalf("cancelled write ran")
	}
	if released.Load() != 1 {
		t.Fatalf("released=%d", released.Load())
	}
}
