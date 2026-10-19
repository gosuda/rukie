package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	writerParams = "_txlock=immediate&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on" +
		"&_busy_timeout=5000&_dqs=0&_pragma=temp_store(MEMORY)&_pragma=mmap_size(0)" +
		"&_pragma=cache_size(-32768)&_pragma=journal_size_limit(67108864)&_pragma=trusted_schema(OFF)"
	readerParams = "mode=ro&_query_only=on&_synchronous=FULL&_foreign_keys=on" +
		"&_busy_timeout=5000&_dqs=0&_pragma=temp_store(MEMORY)&_pragma=mmap_size(0)" +
		"&_pragma=cache_size(-16384)&_pragma=trusted_schema(OFF)"
	backupParams = "mode=ro&_synchronous=FULL&_foreign_keys=on" +
		"&_busy_timeout=5000&_dqs=0&_pragma=temp_store(MEMORY)&_pragma=mmap_size(0)" +
		"&_pragma=cache_size(-16384)&_pragma=trusted_schema(OFF)"
)

const sqlDQSProbe = `SELECT "rukie_dqs_probe"`

type role int

const (
	roleWriter role = iota + 1
	roleReader
)

type pragmaCheck struct {
	query string
	want  string
}

var writerChecks = []pragmaCheck{
	{"PRAGMA foreign_keys", "1"},
	{"PRAGMA synchronous", "2"},
	{"PRAGMA busy_timeout", "5000"},
	{"PRAGMA temp_store", "2"},
	{"PRAGMA cache_size", "-32768"},
	{"PRAGMA mmap_size", "0"},
	{"PRAGMA trusted_schema", "0"},
	{"PRAGMA journal_size_limit", "67108864"},
	{"PRAGMA journal_mode", "wal"},
	{"PRAGMA query_only", "0"},
}

var readerChecks = []pragmaCheck{
	{"PRAGMA foreign_keys", "1"},
	{"PRAGMA synchronous", "2"},
	{"PRAGMA busy_timeout", "5000"},
	{"PRAGMA temp_store", "2"},
	{"PRAGMA cache_size", "-16384"},
	{"PRAGMA mmap_size", "0"},
	{"PRAGMA trusted_schema", "0"},
	{"PRAGMA query_only", "1"},
}

func fileDSN(path, params string) string {
	u := url.URL{Scheme: "file", Path: path}
	return u.String() + "?" + params
}

func openWriter(ctx context.Context, path string) (*sql.DB, *sql.Conn, error) {
	db, err := sql.Open("sqlite", fileDSN(path, writerParams))
	if err != nil {
		return nil, nil, fmt.Errorf("openWriter: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("openWriter: %w", err)
	}
	if err = verifyConn(ctx, conn, roleWriter); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, nil, fmt.Errorf("openWriter: %w", err)
	}
	if err = checkSQLiteVersion(ctx, conn); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, nil, fmt.Errorf("openWriter: %w", err)
	}
	return db, conn, nil
}

func openReaderPool(ctx context.Context, path string, n int) (*sql.DB, chan *sql.Conn, error) {
	db, err := sql.Open("sqlite", fileDSN(path, readerParams))
	if err != nil {
		return nil, nil, fmt.Errorf("openReaderPool: %w", err)
	}
	db.SetMaxOpenConns(n)
	db.SetMaxIdleConns(n)
	pool := make(chan *sql.Conn, n)
	for range n {
		conn, cerr := db.Conn(ctx)
		if cerr == nil {
			cerr = verifyConn(ctx, conn, roleReader)
			if cerr != nil {
				_ = conn.Close()
			}
		}
		if cerr != nil {
			drainClose(pool)
			_ = db.Close()
			return nil, nil, fmt.Errorf("openReaderPool: %w", cerr)
		}
		pool <- conn
	}
	return db, pool, nil
}

func drainClose(pool chan *sql.Conn) {
	for {
		select {
		case conn := <-pool:
			if conn != nil {
				_ = conn.Close()
			}
		default:
			return
		}
	}
}

func verifyConn(ctx context.Context, conn *sql.Conn, r role) error {
	checks := readerChecks
	if r == roleWriter {
		checks = writerChecks
	}
	for _, check := range checks {
		var got any
		if err := conn.QueryRowContext(ctx, check.query).Scan(&got); err != nil {
			return fmt.Errorf("verifyConn: %s: %w", check.query, err)
		}
		if text := pragmaText(got); text != check.want {
			return fmt.Errorf("verifyConn: %s is %q, want %q", check.query, text, check.want)
		}
	}
	return verifyDQS(ctx, conn)
}

func verifyDQS(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, sqlDQSProbe)
	if err == nil {
		_ = rows.Close()
		return fmt.Errorf("verifyConn: double-quoted identifier was accepted; _dqs=0 not applied")
	}
	if !strings.Contains(err.Error(), "no such column") {
		return fmt.Errorf("verifyConn: dqs probe: %w", err)
	}
	return nil
}

func pragmaText(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}

func isBadConn(err error) bool {
	return errors.Is(err, driver.ErrBadConn)
}

func discardSQLConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}
