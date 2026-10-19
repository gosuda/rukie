package store

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *Store) acquireReader(ctx context.Context) (*sql.Conn, error) {
	select {
	case <-s.readClosing:
		return nil, ErrClosed
	default:
	}
	var conn *sql.Conn
	select {
	case conn = <-s.readers:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.readClosing:
		return nil, ErrClosed
	}
	if conn != nil {
		return conn, nil
	}
	return s.openLazyReader(ctx)
}

func (s *Store) openLazyReader(ctx context.Context) (*sql.Conn, error) {
	conn, err := s.rdb.Conn(ctx)
	if err == nil {
		if verr := verifyConn(ctx, conn, roleReader); verr != nil {
			_ = conn.Close()
			err = verr
		} else {
			return conn, nil
		}
	}
	s.readers <- nil
	return nil, fmt.Errorf("acquireReader: %w", err)
}

func (s *Store) releaseReader(conn *sql.Conn, broken bool) {
	if broken {
		discardSQLConn(conn)
		conn = nil
	}
	select {
	case <-s.readClosing:
		if conn != nil {
			_ = conn.Close()
		}
		s.readers <- nil
	default:
		s.readers <- conn
	}
}
