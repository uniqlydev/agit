package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/uniqlydev/agit/internal/transaction"
)

func TestInitializationMigrationAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database with spaces.db")
	store, err := Initialize(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("version %d: %v", version, err)
	}
	created, err := (transaction.Service{Store: store}).Begin(ctx, "  objective ' ? ;  ", "main", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Initialize(ctx, path); err == nil {
		t.Fatal("reinitialized existing database")
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	txs, err := store.List(ctx, true)
	if err != nil || len(txs) != 1 {
		t.Fatalf("%v %v", txs, err)
	}
	if txs[0] != created {
		t.Fatalf("persisted %+v != %+v", txs[0], created)
	}
	if err := store.Create(ctx, created); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	created.ID = "invalid-state"
	created.State = "unknown"
	if err := store.Create(ctx, created); err == nil {
		t.Fatal("invalid state accepted")
	}
}
func TestMissingDatabase(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("created missing database")
	}
}
func TestMigrationFromEmptyAndFutureVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("future schema accepted")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 99 {
		t.Fatalf("future schema changed: %d %v", version, err)
	}
}
func TestMigrationRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "conflict.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE transactions (developer_data TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("conflicting table accepted")
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 0 {
		t.Fatalf("rollback: %d %v", version, err)
	}
	if _, err := db.Exec("INSERT INTO transactions VALUES ('preserved')"); err != nil {
		t.Fatal("developer table lost:", err)
	}
}
