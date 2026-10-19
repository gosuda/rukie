package store

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"
)

func TestConnPragmas(t *testing.T) {
	s := openTest(t, Options{ReadConns: 2})
	readers := s.readerCount
	held := make([]*sql.Conn, 0, readers)
	for range readers {
		conn, err := s.acquireReader(t.Context())
		if err != nil {
			t.Fatalf("acquireReader: %v", err)
		}
		if err = verifyConn(t.Context(), conn, roleReader); err != nil {
			t.Fatalf("verifyConn reader: %v", err)
		}
		if _, err = conn.ExecContext(t.Context(), `SELECT "rukie_quoted"`); err == nil {
			t.Fatal("double-quoted identifier was accepted on a reader")
		}
		held = append(held, conn)
	}
	if got := s.Stats().ReadersInUse; got != readers {
		t.Fatalf("ReadersInUse = %d, want %d", got, readers)
	}
	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.acquireReader(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reader beyond pool size returned %v, want DeadlineExceeded", err)
	}
	for _, conn := range held {
		s.releaseReader(conn, false)
	}
}

func TestOFDLockingEnabled(t *testing.T) {
	openTest(t, Options{})
	if runtime.GOOS == "linux" && !sqlite.OFDLockingEnabled() {
		t.Fatal("OFD locks are not enabled after Open on Linux")
	}
}

func TestVersionAtLeast(t *testing.T) {
	minimum := [3]int{3, 53, 4}
	cases := []struct {
		version string
		want    bool
	}{
		{"3.53.4", true},
		{"3.53.5", true},
		{"3.54.0", true},
		{"4.0.0", true},
		{"3.53.3", false},
		{"3.52.9", false},
		{"3.9.0", false},
	}
	for _, tc := range cases {
		got, err := versionAtLeast(tc.version, minimum)
		if err != nil || got != tc.want {
			t.Errorf("versionAtLeast(%q) = %v, %v; want %v", tc.version, got, err, tc.want)
		}
	}
	if _, err := versionAtLeast("3.53", minimum); err == nil {
		t.Error("two-part version accepted")
	}
}

func TestSQLiteVersionIsCurrent(t *testing.T) {
	s := openTest(t, Options{})
	var v string
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		return tx.tx.QueryRowContext(tx.ctx, sqlSQLiteVersion).Scan(&v)
	}); err != nil {
		t.Fatalf("sqlite_version: %v", err)
	}
	ok, err := versionAtLeast(v, [3]int{3, 53, 4})
	if err != nil || !ok {
		t.Fatalf("bundled SQLite %s is below 3.53.4 (%v)", v, err)
	}
}

func TestCloseDeadlineIsRetryable(t *testing.T) {
	path := testPath(t)
	s := openWith(t, path, fixtureFS(t), Options{})
	started := make(chan struct{})
	gate := make(chan struct{})
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- s.Write(t.Context(), func(*WriteTx) error {
			close(started)
			<-gate
			return nil
		})
	}()
	<-started
	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := s.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close past its deadline returned %v, want DeadlineExceeded", err)
	}
	if _, err := open(t.Context(), Options{Path: path}, fixtureFS(t)); !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("path released while the store is still open: %v", err)
	}
	close(gate)
	if err := <-gateDone; err != nil {
		t.Fatalf("gate write: %v", err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
	reopened, err := open(t.Context(), Options{Path: path}, fixtureFS(t))
	if err != nil {
		t.Fatalf("open after completed Close: %v", err)
	}
	if err = reopened.Close(t.Context()); err != nil {
		t.Fatalf("Close reopened: %v", err)
	}
}

func TestSecondCloseHonorsItsDeadline(t *testing.T) {
	s := openTest(t, Options{})
	started := make(chan struct{})
	gate := make(chan struct{})
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- s.Write(t.Context(), func(*WriteTx) error {
			close(started)
			<-gate
			return nil
		})
	}()
	<-started
	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Close(t.Context()) }()
	waitFor(t, "first Close to begin", func() bool { return s.writer.closingNow() })
	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if err := s.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Close returned %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("second Close took %v past a 50ms deadline", elapsed)
	}
	close(gate)
	if err := <-gateDone; err != nil {
		t.Fatalf("gate write: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first Close: %v", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	s := openTest(t, Options{})
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := s.Read(t.Context(), func(*ReadTx) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Read after Close returned %v, want ErrClosed", err)
	}
	if err := s.Write(t.Context(), func(*WriteTx) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Write after Close returned %v, want ErrClosed", err)
	}
}
