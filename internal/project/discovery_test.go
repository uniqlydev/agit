package project

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uniqlydev/agit/internal/git"
	"github.com/uniqlydev/agit/internal/storage"
	"github.com/uniqlydev/agit/internal/transaction"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func repoFixture(t *testing.T) *git.Repository {
	t.Helper()
	root := t.TempDir()
	gitTest(t, root, "init", "-b", "main")
	gitTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fixture")
	repo, err := git.Discover(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
func TestLegacyAdoptionFromLinkedWorktree(t *testing.T) {
	ctx := context.Background()
	repo := repoFixture(t)
	if err := initializeLegacy(ctx, repo.Root); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, filepath.Join(repo.Root, ".agit", storage.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	head, _ := repo.Head(ctx)
	tx, err := (transaction.Service{Store: store}).Begin(ctx, "existing objective", "main", head)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, repo.Root, "worktree", "add", "-b", "linked", linked, "HEAD")
	linkedRepo, err := git.Discover(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := Open(ctx, linkedRepo)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.MetadataDir != filepath.Join(repo.Root, ".agit") {
		t.Fatal("legacy database relocated")
	}
	existing, err := adopted.List(ctx, false)
	if err != nil || len(existing) != 1 || existing[0] != tx {
		t.Fatalf("%v %v", existing, err)
	}
	id := adopted.RepositoryID
	adopted.Close()
	reopened, err := Open(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.RepositoryID != id {
		t.Fatal("repository identity changed")
	}
	backups, err := filepath.Glob(filepath.Join(repo.Root, ".agit", "agit-v1-backup-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups: %v %v", backups, err)
	}
}
func TestConflictingLegacyMetadataPreserved(t *testing.T) {
	ctx := context.Background()
	repo := repoFixture(t)
	if err := initializeLegacy(ctx, repo.Root); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, repo.Root, "worktree", "add", "-b", "linked", linked, "HEAD")
	if err := initializeLegacy(ctx, linked); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(repo.Root, ".agit", storage.DatabaseName), filepath.Join(linked, ".agit", storage.DatabaseName)}
	before := [][]byte{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, data)
	}
	if _, err := Open(ctx, repo); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("%v", err)
	}
	for i, path := range paths {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before[i]) {
			t.Fatal("conflict modified database")
		}
	}
}
func TestConflictAfterLocatorPublication(t *testing.T) {
	ctx := context.Background()
	repo := repoFixture(t)
	if err := Initialize(ctx, repo.Root); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, repo.Root, "worktree", "add", "-b", "linked", linked, "HEAD")
	if err := initializeLegacy(ctx, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, repo); err == nil {
		t.Fatal("additional legacy database silently ignored")
	}
}
func TestInterruptedLocatorPublicationReusesIdentity(t *testing.T) {
	ctx := context.Background()
	repo := repoFixture(t)
	if err := initializeLegacy(ctx, repo.Root); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(ctx, filepath.Join(repo.Root, ".agit", storage.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upgrade(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := store.RepositoryID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	adopted, err := Open(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer adopted.Close()
	if adopted.RepositoryID != id {
		t.Fatal("adoption regenerated identity")
	}
}
func TestStaleLocatorBlocksReinitialization(t *testing.T) {
	ctx := context.Background()
	repo := repoFixture(t)
	if err := Initialize(ctx, repo.Root); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(repo.Root, ".agit")
	preserved := filepath.Join(t.TempDir(), "preserved")
	if err := os.Rename(metadata, preserved); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, repo); err == nil {
		t.Fatal("stale locator accepted")
	}
	if err := Initialize(ctx, repo.Root); err == nil {
		t.Fatal("reinitialized stale locator")
	}
	if _, err := os.Stat(metadata); !os.IsNotExist(err) {
		t.Fatal("replaced missing metadata")
	}
}
func TestLocatorIdentityMismatch(t *testing.T) {
	ctx := context.Background()
	repo := repoFixture(t)
	if err := Initialize(ctx, repo.Root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo.CommonDir, "agit-control", "metadata.json")
	if err := os.WriteFile(path, []byte(`{"Version":1,"RepositoryID":"00000000000000000000000000000000","MetadataPath":"../.agit"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, repo); err == nil {
		t.Fatal("wrong repository identity accepted")
	}
}
func TestMetadataSymlinksRejected(t *testing.T) {
	for _, kind := range []string{"directory", "database", "locator", "lock"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			repo := repoFixture(t)
			if err := Initialize(ctx, repo.Root); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(repo.Root, ".agit")
			switch kind {
			case "database":
				path = filepath.Join(path, storage.DatabaseName)
			case "locator":
				path = filepath.Join(repo.CommonDir, "agit-control", "metadata.json")
			case "lock":
				path = filepath.Join(repo.CommonDir, "agit-control.lock")
			}
			target := filepath.Join(t.TempDir(), "preserved")
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(ctx, repo); err == nil {
				t.Fatalf("%s symlink accepted", kind)
			}
		})
	}
}
func TestRepositoryLockCancellation(t *testing.T) {
	repo := repoFixture(t)
	release, err := acquire(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acquire(ctx, repo); err == nil {
		t.Fatal("second repository lock acquired")
	}
}
func TestUnknownControlFilesNotOverwritten(t *testing.T) {
	for _, kind := range []string{"lock", "directory"} {
		t.Run(kind, func(t *testing.T) {
			repo := repoFixture(t)
			path := filepath.Join(repo.CommonDir, "agit-control.lock")
			if kind == "lock" {
				if err := os.WriteFile(path, []byte("developer file"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				path = filepath.Join(repo.CommonDir, "agit-control")
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := Initialize(context.Background(), repo.Root); err == nil {
				t.Fatal("unknown control resource accepted")
			}
			if kind == "lock" {
				data, _ := os.ReadFile(path)
				if string(data) != "developer file" {
					t.Fatal("developer lock overwritten")
				}
			}
		})
	}
}
func TestRepositoryRelocationWithoutWorkspaces(t *testing.T) {
	ctx := context.Background()
	repo := repoFixture(t)
	if err := Initialize(ctx, repo.Root); err != nil {
		t.Fatal(err)
	}
	session, err := Open(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	id := session.RepositoryID
	session.Close()
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(repo.Root, moved); err != nil {
		t.Fatal(err)
	}
	relocated, err := git.Discover(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, relocated)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.RepositoryID != id {
		t.Fatal("identity changed after relocation")
	}
}

func TestProjectLockProcess(t *testing.T) {
	if os.Getenv("AGIT_LOCK_TEST_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	repo, err := git.Discover(context.Background(), os.Getenv("AGIT_LOCK_TEST_REPO"))
	if err != nil {
		os.Exit(2)
	}
	release, err := acquire(context.Background(), repo)
	if err != nil {
		os.Exit(3)
	}
	defer release()
	fmt.Fprintln(os.Stdout, "locked")
	var input [1]byte
	os.Stdin.Read(input[:])
}
func TestRepositoryLockReleasedAfterProcessExit(t *testing.T) {
	repo := repoFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProjectLockProcess$")
	cmd.Env = append(os.Environ(), "AGIT_LOCK_TEST_HELPER=1", "AGIT_LOCK_TEST_REPO="+repo.Root)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "locked" {
		t.Fatal("child did not acquire lock")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	release, err := acquire(waitCtx, repo)
	if err != nil {
		t.Fatal("lock survived process exit:", err)
	}
	release()
}
