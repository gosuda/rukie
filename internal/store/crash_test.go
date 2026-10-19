package store

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"encoding/json/jsontext"
)

const (
	crashChildEnv = "RUKIE_STORE_CRASH_CHILD"
	crashDBEnv    = "RUKIE_STORE_CRASH_DB"
	crashWriters  = 8
)

var errPlanned = errors.New("planned failure after inserts")

func crashWrite(tag string, fail bool) func(*WriteTx) error {
	return func(tx *WriteTx) error {
		if err := insertProbe(tx, tag, "crash"); err != nil {
			return err
		}
		for n := range 3 {
			ev := Event{
				Kind:       "probe.row",
				Body:       jsontext.Value(fmt.Sprintf(`{"tag":%q,"n":%d}`, tag, n)),
				OccurredAt: time.Now(),
			}
			if _, err := tx.Outbox().Emit(ev); err != nil {
				return err
			}
		}
		if fail {
			return errPlanned
		}
		return nil
	}
}

func TestCrashChild(t *testing.T) {
	if os.Getenv(crashChildEnv) != "1" {
		t.Skip("crash child only; run by TestCrashMidBatch")
	}
	s, err := open(t.Context(), Options{Path: os.Getenv(crashDBEnv)}, fixtureFS(t))
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	var out sync.Mutex
	var wg sync.WaitGroup
	for g := range crashWriters {
		wg.Go(func() {
			for j := 0; ; j++ {
				tag := tagFor(fmt.Sprintf("g%d", g), j)
				err := s.Write(context.Background(), crashWrite(tag, j%5 == 4))
				word := "ACK"
				if err != nil {
					word = "NACK"
				}
				out.Lock()
				_, _ = fmt.Fprintf(os.Stdout, "%s %s\n", word, tag)
				out.Unlock()
			}
		})
	}
	wg.Wait()
}

func runCrashChild(t *testing.T, path string, killAfter int) (acked, nacked map[string]bool) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "/proc/self/exe", "-test.run=^TestCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), crashChildEnv+"=1", crashDBEnv+"="+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	acked, nacked = map[string]bool{}, map[string]bool{}
	scanner := bufio.NewScanner(stdout)
	for len(acked) < killAfter && scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "ACK":
			acked[fields[1]] = true
		case "NACK":
			nacked[fields[1]] = true
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if len(acked) < killAfter {
		t.Fatalf("child produced %d acks before EOF, want %d; stderr: %s", len(acked), killAfter, stderr.String())
	}
	return acked, nacked
}

func TestCrashMidBatch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("crash child re-executes /proc/self/exe; the store requires Linux OFD locks")
	}
	iterations := 40
	if testing.Short() {
		iterations = 8
	}
	for it := range iterations {
		path := testPath(t)
		killAfter := 5 + (it*37)%196
		acked, nacked := runCrashChild(t, path, killAfter)
		checkCrashInvariants(t, path, acked, nacked, it)
	}
}

func checkCrashInvariants(t *testing.T, path string, acked, nacked map[string]bool, it int) {
	t.Helper()
	s := openWith(t, path, fixtureFS(t), Options{})
	if got := readInt(t, s, `SELECT count(*) FROM pragma_integrity_check WHERE integrity_check <> 'ok'`); got != 0 {
		t.Fatalf("iteration %d: I1 integrity_check failed", it)
	}
	for tag := range acked {
		if n := readInt(t, s, `SELECT count(*) FROM probe_rows WHERE tag = ?`, tag); n != 1 {
			t.Fatalf("iteration %d: I2 acked %s has %d probe rows", it, tag, n)
		}
		if n := readInt(t, s, `SELECT count(*) FROM outbox WHERE json_extract(body, '$.tag') = ?`, tag); n != 3 {
			t.Fatalf("iteration %d: I2 acked %s has %d outbox rows", it, tag, n)
		}
	}
	for tag := range nacked {
		probe := readInt(t, s, `SELECT count(*) FROM probe_rows WHERE tag = ?`, tag)
		events := readInt(t, s, `SELECT count(*) FROM outbox WHERE json_extract(body, '$.tag') = ?`, tag)
		if probe != 0 || events != 0 {
			t.Fatalf("iteration %d: I3 nacked %s persisted (%d probe, %d outbox)", it, tag, probe, events)
		}
	}
	partial := readInt(t, s, `SELECT count(*) FROM (
		SELECT json_extract(body, '$.tag') AS tag, count(*) AS n FROM outbox GROUP BY tag
	) WHERE n <> 3`)
	orphan := readInt(t, s, `SELECT count(*) FROM outbox WHERE json_extract(body, '$.tag') NOT IN (SELECT tag FROM probe_rows)`)
	if partial != 0 || orphan != 0 {
		t.Fatalf("iteration %d: I4 partial requests=%d orphan events=%d", it, partial, orphan)
	}
	var maxBefore int64
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		return tx.tx.QueryRowContext(tx.ctx, `SELECT COALESCE(max(seq), 0) FROM outbox`).Scan(&maxBefore)
	}); err != nil {
		t.Fatalf("iteration %d: max seq: %v", it, err)
	}
	var emitted OutboxRow
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		var err error
		emitted, err = tx.Outbox().Emit(Event{Kind: "probe.check", Body: jsontext.Value(`{"tag":"check"}`), OccurredAt: time.Now()})
		return err
	}); err != nil {
		t.Fatalf("iteration %d: post-crash emit: %v", it, err)
	}
	if emitted.Seq <= maxBefore {
		t.Fatalf("iteration %d: I5 seq reused: %d <= %d", it, emitted.Seq, maxBefore)
	}
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		_, err := tx.tx.ExecContext(tx.ctx, `INSERT INTO probe_rows_fts (probe_rows_fts) VALUES ('integrity-check')`)
		return err
	}); err != nil {
		t.Fatalf("iteration %d: I6 fts integrity-check: %v", it, err)
	}
	if got := readInt(t, s, `SELECT count(*) FROM schema_migrations`); got != 2 {
		t.Fatalf("iteration %d: I7 schema_migrations has %d rows after reopen, want 2", it, got)
	}
}
