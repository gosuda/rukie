package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type ftsCandidate struct {
	name   string
	create string
}

const (
	sqlFTSCreatePrimary = `CREATE VIRTUAL TABLE temp.fts_probe USING fts5(body, content='fts_probe_src', content_rowid='id', tokenize='trigram case_sensitive 0')`
	sqlFTSCreateBare    = `CREATE VIRTUAL TABLE temp.fts_probe USING fts5(body, content='fts_probe_src', content_rowid='id', tokenize='trigram')`
)

var ftsCandidates = []ftsCandidate{
	{name: "trigram case_sensitive 0", create: sqlFTSCreatePrimary},
	{name: "trigram", create: sqlFTSCreateBare},
}

var (
	ftsProbeSetup = []string{
		`CREATE TABLE temp.fts_probe_src(id INTEGER PRIMARY KEY, body TEXT NOT NULL)`,
	}
	ftsProbeWrite = []string{
		`INSERT INTO temp.fts_probe_src(id, body) VALUES (1, 'rukie probe')`,
		`INSERT INTO fts_probe(rowid, body) VALUES (1, 'rukie probe')`,
		`INSERT INTO fts_probe(fts_probe) VALUES ('rebuild')`,
		`INSERT INTO fts_probe(fts_probe) VALUES ('optimize')`,
		`INSERT INTO fts_probe(fts_probe) VALUES ('integrity-check')`,
	}
	ftsProbeChecks = []struct {
		query string
		want  int64
	}{
		{`SELECT count(*) FROM fts_probe WHERE fts_probe MATCH 'probe'`, 1},
		{`SELECT count(*) FROM fts_probe WHERE fts_probe MATCH 'PROBE'`, 1},
		{`SELECT json_valid('{}')`, 1},
	}
)

const (
	sqlFTSDropProbe  = `DROP TABLE IF EXISTS temp.fts_probe`
	sqlFTSDropSource = `DROP TABLE IF EXISTS temp.fts_probe_src`
)

func selectFTS(ctx context.Context, conn *sql.Conn, candidates []ftsCandidate) (string, error) {
	var errs []error
	for _, c := range candidates {
		err := probeFTS5(ctx, conn, c.create)
		if err == nil {
			return c.name, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", c.name, err))
	}
	return "", fmt.Errorf("selectFTS: %w: %w", ErrFTS5Unavailable, errors.Join(errs...))
}

func probeFTS5(ctx context.Context, conn *sql.Conn, create string) (err error) {
	defer func() {
		err = errors.Join(err, dropFTSProbe(ctx, conn))
	}()
	if err = execAll(ctx, conn, ftsProbeSetup); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, create); err != nil {
		return err
	}
	if err = execAll(ctx, conn, ftsProbeWrite); err != nil {
		return err
	}
	for _, check := range ftsProbeChecks {
		var got int64
		if err = conn.QueryRowContext(ctx, check.query).Scan(&got); err != nil {
			return err
		}
		if got != check.want {
			return fmt.Errorf("probe %q returned %d, want %d", check.query, got, check.want)
		}
	}
	return nil
}

func dropFTSProbe(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, sqlFTSDropProbe); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, sqlFTSDropSource)
	return err
}

func execAll(ctx context.Context, conn *sql.Conn, stmts []string) error {
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func checkAppID(ctx context.Context, conn *sql.Conn) error {
	var id int64
	if err := conn.QueryRowContext(ctx, sqlAppID).Scan(&id); err != nil {
		return fmt.Errorf("checkAppID: %w", err)
	}
	if id != 0 && id != appID {
		return fmt.Errorf("checkAppID: %d: %w", id, ErrAppIDMismatch)
	}
	return nil
}

func checkSQLiteVersion(ctx context.Context, conn *sql.Conn) error {
	var version string
	if err := conn.QueryRowContext(ctx, sqlSQLiteVersion).Scan(&version); err != nil {
		return fmt.Errorf("checkSQLiteVersion: %w", err)
	}
	ok, err := versionAtLeast(version, [3]int{3, 53, 4})
	if err != nil {
		return fmt.Errorf("checkSQLiteVersion: %w", err)
	}
	if !ok {
		return fmt.Errorf("checkSQLiteVersion: %s: %w", version, ErrSQLiteTooOld)
	}
	return nil
}
