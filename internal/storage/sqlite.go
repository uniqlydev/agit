package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/uniqlydev/agit/internal/transaction"
	_ "modernc.org/sqlite"
)

const DatabaseName = "agit.db"

type Store struct {
	db   *sql.DB
	path string
}

func Open(ctx context.Context, path string) (*Store, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("AGIT database missing or not a regular file; run agit init")
	}
	return connect(ctx, path, "rw")
}
func Initialize(ctx context.Context, path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("create AGIT database: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return connect(ctx, path, "rw")
}
func openDB(path, mode string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	query := uri.Query()
	query.Set("mode", mode)
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "synchronous(FULL)")
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
func connect(ctx context.Context, path, mode string) (*Store, error) {
	db, err := openDB(path, mode)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 2 {
		return fmt.Errorf("unsupported database schema version %d", version)
	}
	if version == 0 {
		_, err = tx.ExecContext(ctx, `CREATE TABLE transactions (
   id TEXT PRIMARY KEY,
   objective TEXT NOT NULL CHECK(length(trim(objective)) > 0),
   state TEXT NOT NULL CHECK(state IN ('active', 'completed', 'aborted')),
   branch TEXT NOT NULL,
   head_commit TEXT NOT NULL,
   created_at TEXT NOT NULL
  ); PRAGMA user_version = 1;`)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Create(ctx context.Context, t transaction.Transaction) error {
	if err := transaction.ValidateObjective(t.Objective); err != nil {
		return err
	}
	if err := t.State.Validate(); err != nil {
		return err
	}
	if t.ID == "" || t.CreatedAt.IsZero() {
		return fmt.Errorf("transaction ID and creation time are required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO transactions(id, objective, state, branch, head_commit, created_at) VALUES(?, ?, ?, ?, ?, ?)`, t.ID, t.Objective, t.State, t.Branch, t.HeadCommit, t.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("persist transaction: %w", err)
	}
	return nil
}
func (s *Store) List(ctx context.Context, activeOnly bool) ([]transaction.Transaction, error) {
	query := `SELECT id, objective, state, branch, head_commit, created_at FROM transactions`
	var args []any
	if activeOnly {
		query += " WHERE state = ?"
		args = append(args, transaction.StateActive)
	}
	query += " ORDER BY created_at, id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []transaction.Transaction{}
	for rows.Next() {
		var t transaction.Transaction
		var created string
		if err := rows.Scan(&t.ID, &t.Objective, &t.State, &t.Branch, &t.HeadCommit, &created); err != nil {
			return nil, err
		}
		if err := t.State.Validate(); err != nil {
			return nil, err
		}
		t.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}
