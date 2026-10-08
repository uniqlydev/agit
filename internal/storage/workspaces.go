package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/uniqlydev/agit/internal/safeio"
	"github.com/uniqlydev/agit/internal/workspace"
)

const workspaceColumns = `id, transaction_id, name, branch, path, base_commit, state, created_at, updated_at, operation_token, operation, admin_path, last_error, removed_at`

// Upgrade adopts a v1 database without changing the original v1 migration.
// Project callers hold the repository lock; low-level M0 callers can remain v1.
func (s *Store) Upgrade(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 2 {
		return nil
	}
	if version != 1 {
		return fmt.Errorf("cannot adopt schema version %d", version)
	}
	// VACUUM INTO creates a consistent SQLite snapshot, including committed WAL data.
	backup, err := os.CreateTemp(filepath.Dir(s.path), "agit-v1-backup-*.db")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
		return fmt.Errorf("pre-migration backup %s: %w", backupPath, err)
	}
	f, err := safeio.OpenFile(backupPath, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := safeio.SyncDir(filepath.Dir(s.path)); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err := workspace.NewID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE repository_identity (
 singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
 id TEXT NOT NULL UNIQUE CHECK(length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*')
 );
 CREATE TABLE workspaces (
 id TEXT PRIMARY KEY NOT NULL CHECK(length(id) = 32 AND id NOT GLOB '*[^0-9a-f]*'),
 transaction_id TEXT NOT NULL REFERENCES transactions(id) ON DELETE RESTRICT,
 name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 48 AND name NOT GLOB '*[^a-z0-9-]*' AND substr(name,1,1) GLOB '[a-z]' AND substr(name,-1,1) != '-'),
 branch TEXT NOT NULL UNIQUE,
 path TEXT NOT NULL UNIQUE,
 base_commit TEXT NOT NULL CHECK(length(base_commit) IN (40,64)),
 state TEXT NOT NULL CHECK(state IN ('creating','ready','error','removing','removed')),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 operation_token TEXT NOT NULL UNIQUE CHECK(length(operation_token) = 32 AND operation_token NOT GLOB '*[^0-9a-f]*'),
 operation TEXT NOT NULL CHECK(operation IN ('create','remove')),
 admin_path TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '',
 removed_at TEXT NOT NULL DEFAULT '',
 UNIQUE(transaction_id, name)
 );`)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO repository_identity(singleton, id) VALUES(1, ?)", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 2"); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RepositoryID(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM repository_identity WHERE singleton = 1").Scan(&id)
	if err == nil && !workspace.ValidID(id) {
		return "", fmt.Errorf("invalid repository identity")
	}
	return id, err
}
func (s *Store) InsertWorkspace(ctx context.Context, w workspace.Workspace) error {
	if !workspace.ValidID(w.ID) || !workspace.ValidID(w.TransactionID) || !workspace.ValidID(w.OperationToken) || w.State != workspace.Creating || w.Operation != "create" {
		return fmt.Errorf("invalid creation intent")
	}
	if err := workspace.ValidateName(w.Name); err != nil {
		return err
	}
	if w.Path != filepath.ToSlash(filepath.Join("workspaces", w.ID, "tree")) || w.Branch != "agit/"+w.TransactionID+"/"+w.ID+"-"+w.Name {
		return fmt.Errorf("invalid workspace path/branch")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspaces(`+workspaceColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, w.ID, w.TransactionID, w.Name, w.Branch, w.Path, w.BaseCommit, w.State, w.CreatedAt.UTC().Format(time.RFC3339Nano), w.UpdatedAt.UTC().Format(time.RFC3339Nano), w.OperationToken, w.Operation, w.AdminPath, w.LastError, "")
	return err
}
func (s *Store) UpdateWorkspace(ctx context.Context, w workspace.Workspace, from workspace.State) error {
	if err := workspace.ValidateTransition(from, w.State); err != nil && !(from == workspace.Error && w.State == workspace.Error) {
		return err
	}
	removed := ""
	if !w.RemovedAt.IsZero() {
		removed = w.RemovedAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE workspaces SET state=?, updated_at=?, operation=?, admin_path=?, last_error=?, removed_at=? WHERE id=? AND state=? AND transaction_id=? AND name=? AND branch=? AND path=? AND base_commit=? AND operation_token=? AND (admin_path = '' OR admin_path = ?)`, w.State, w.UpdatedAt.UTC().Format(time.RFC3339Nano), w.Operation, w.AdminPath, w.LastError, removed, w.ID, from, w.TransactionID, w.Name, w.Branch, w.Path, w.BaseCommit, w.OperationToken, w.AdminPath)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("workspace state or identity changed concurrently")
	}
	return nil
}
func (s *Store) ListWorkspaces(ctx context.Context, txID string) ([]workspace.Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+workspaceColumns+` FROM workspaces WHERE transaction_id=? ORDER BY created_at, id`, txID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []workspace.Workspace
	for rows.Next() {
		var w workspace.Workspace
		var created, updated, removed string
		if err := rows.Scan(&w.ID, &w.TransactionID, &w.Name, &w.Branch, &w.Path, &w.BaseCommit, &w.State, &created, &updated, &w.OperationToken, &w.Operation, &w.AdminPath, &w.LastError, &removed); err != nil {
			return nil, err
		}
		if w.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, err
		}
		if w.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
			return nil, err
		}
		if removed != "" {
			if w.RemovedAt, err = time.Parse(time.RFC3339Nano, removed); err != nil {
				return nil, err
			}
		}
		result = append(result, w)
	}
	return result, rows.Err()
}

// Inspect validates a legacy candidate without migrations or writable connections.
func Inspect(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("database is not regular: %s", path)
	}
	db, err := openDB(path, "ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 1 && version != 2 {
		return fmt.Errorf("unsupported candidate schema %d", version)
	}
	rows, err := db.QueryContext(ctx, `SELECT id, objective, state, branch, head_commit, created_at FROM transactions LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}
