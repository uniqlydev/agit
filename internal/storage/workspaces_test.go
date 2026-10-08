package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uniqlydev/agit/internal/transaction"
	"github.com/uniqlydev/agit/internal/workspace"
)

func TestMigrationV2PreservesV1AndSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), DatabaseName)
	store, err := Initialize(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tx, err := (transaction.Service{Store: store}).Begin(ctx, "existing ' objective", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	var version, foreignKeys int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("version %d %v", version, err)
	}
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign keys %d %v", foreignKeys, err)
	}
	records, err := store.List(ctx, false)
	if err != nil || len(records) != 1 || records[0] != tx {
		t.Fatalf("%v %v", records, err)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "agit-v1-backup-*.db"))
	if len(backups) != 1 {
		t.Fatalf("%v", backups)
	}
	backup, err := Open(ctx, backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := backup.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("backup version %d %v", version, err)
	}
	original, err := backup.List(ctx, false)
	if err != nil || len(original) != 1 || original[0] != tx {
		t.Fatal("snapshot did not preserve transactions")
	}
	id, _ := store.RepositoryID(ctx)
	if err := store.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	again, _ := store.RepositoryID(ctx)
	if id != again {
		t.Fatal("upgrade changed ID")
	}
}
func TestMigrationV2Rollback(t *testing.T) {
	ctx := context.Background()
	store, err := Initialize(ctx, filepath.Join(t.TempDir(), DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`CREATE TABLE workspaces (developer_data TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Upgrade(ctx); err == nil {
		t.Fatal("conflicting table overwritten")
	}
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("%d %v", version, err)
	}
	if _, err := store.db.Exec(`INSERT INTO workspaces VALUES ('preserved')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`SELECT id FROM repository_identity`); err == nil {
		t.Fatal("partial schema survived rollback")
	}
}
func TestWorkspaceForeignKeyUniquenessAndCAS(t *testing.T) {
	ctx := context.Background()
	store, err := Initialize(ctx, filepath.Join(t.TempDir(), DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	id, _ := workspace.NewID()
	txID, _ := workspace.NewID()
	token, _ := workspace.NewID()
	now := time.Now().UTC()
	w := workspace.Workspace{ID: id, TransactionID: txID, Name: "backend", Branch: "agit/" + txID + "/" + id + "-backend", Path: "workspaces/" + id + "/tree", BaseCommit: "0123456789012345678901234567890123456789", State: workspace.Creating, Operation: "create", OperationToken: token, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertWorkspace(ctx, w); err == nil {
		t.Fatal("missing transaction foreign key accepted")
	}
	tx := transaction.Transaction{ID: txID, Objective: "objective", State: transaction.StateActive, Branch: "main", CreatedAt: now}
	if err := store.Create(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	duplicate := w
	duplicate.ID, _ = workspace.NewID()
	duplicate.OperationToken, _ = workspace.NewID()
	duplicate.Path = "workspaces/" + duplicate.ID + "/tree"
	duplicate.Branch = "agit/" + txID + "/" + duplicate.ID + "-backend"
	if err := store.InsertWorkspace(ctx, duplicate); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := store.db.Exec(`DELETE FROM transactions WHERE id=?`, txID); err == nil {
		t.Fatal("transaction with workspace deleted")
	}
	w.State = workspace.Ready
	if err := store.UpdateWorkspace(ctx, w, workspace.Creating); err != nil {
		t.Fatal(err)
	}
	w.State = workspace.Error
	if err := store.UpdateWorkspace(ctx, w, workspace.Creating); err == nil {
		t.Fatal("stale CAS accepted")
	}
	tampered := w
	tampered.Path = "../../developer"
	if err := store.UpdateWorkspace(ctx, tampered, workspace.Ready); err == nil {
		t.Fatal("immutable identity changed")
	}
}
func TestCandidateInspectionIsReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), DatabaseName)
	store, err := Initialize(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Inspect(ctx, path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("inspection changed database")
	}
}
