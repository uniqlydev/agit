package workspace_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uniqlydev/agit/internal/git"
	"github.com/uniqlydev/agit/internal/project"
	"github.com/uniqlydev/agit/internal/storage"
	"github.com/uniqlydev/agit/internal/transaction"
	"github.com/uniqlydev/agit/internal/workspace"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func fixture(t *testing.T) (*project.Session, *workspace.Service, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	gitCmd(t, root, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/.agit/\n*.cache\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, root, "add", ".")
	gitCmd(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "fixture")
	if err := project.Initialize(ctx, root); err != nil {
		t.Fatal(err)
	}
	repo, err := git.Discover(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	session, err := project.Open(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	head, err := repo.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := (transaction.Service{Store: session.Store}).Begin(ctx, "objective", "main", head)
	if err != nil {
		t.Fatal(err)
	}
	service := &workspace.Service{Store: session.Store, Repository: repo, MetadataDir: session.MetadataDir, RepositoryID: session.RepositoryID}
	return session, service, tx.ID
}
func mustCreate(t *testing.T, s *workspace.Service, tx, name string) workspace.Workspace {
	t.Helper()
	w, err := s.Create(context.Background(), tx, name)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func fullPath(s *workspace.Service, w workspace.Workspace) string {
	return filepath.Join(s.MetadataDir, filepath.FromSlash(w.Path))
}
func TestMultipleIndependentWorkspaces(t *testing.T) {
	session, s, tx := fixture(t)
	ctx := context.Background()
	before := gitCmd(t, s.Repository.Root, "status", "--porcelain")
	originalHead := gitCmd(t, s.Repository.Root, "rev-parse", "HEAD")
	backend := mustCreate(t, s, tx, "backend")
	frontend := mustCreate(t, s, tx, "frontend")
	if backend.ID == frontend.ID || backend.Branch == frontend.Branch || backend.Path == frontend.Path {
		t.Fatal("workspaces not isolated")
	}
	for _, w := range []workspace.Workspace{backend, frontend} {
		if w.State != workspace.Ready {
			t.Fatalf("%+v", w)
		}
		if got := gitCmd(t, fullPath(s, w), "branch", "--show-current"); got != w.Branch {
			t.Fatalf("%s != %s", got, w.Branch)
		}
		if got := gitCmd(t, fullPath(s, w), "rev-parse", "HEAD"); got != w.BaseCommit {
			t.Fatal("base mismatch")
		}
	}
	if got := gitCmd(t, s.Repository.Root, "status", "--porcelain"); got != before {
		t.Fatalf("original status changed: %s", got)
	}
	if got := gitCmd(t, s.Repository.Root, "rev-parse", "HEAD"); got != originalHead {
		t.Fatal("original branch modified")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	linked, err := git.Discover(ctx, fullPath(s, backend))
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(linked.Root, "nested")
	if err := os.Mkdir(nested, 0755); err != nil {
		t.Fatal(err)
	}
	linked, err = git.Discover(ctx, nested)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := project.Open(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	if shared.MetadataDir != s.MetadataDir || shared.RepositoryID != s.RepositoryID {
		t.Fatal("metadata not shared")
	}
	records, err := shared.List(ctx, false)
	if err != nil || len(records) != 1 || records[0].ID != tx {
		t.Fatalf("%v %v", records, err)
	}
	workspaces, err := shared.ListWorkspaces(ctx, tx)
	if err != nil || len(workspaces) != 2 {
		t.Fatalf("%v %v", workspaces, err)
	}
}
func TestDuplicateAndInvalidWorkspaceNames(t *testing.T) {
	_, s, tx := fixture(t)
	mustCreate(t, s, tx, "backend")
	for _, name := range []string{"backend", "", "../escape", "Backend", "a/b", "-option", "trailing-", "a.b", "a b", "é", strings.Repeat("a", 49)} {
		if _, err := s.Create(context.Background(), tx, name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	records, err := s.Store.ListWorkspaces(context.Background(), tx)
	if err != nil || len(records) != 1 {
		t.Fatalf("%v %v", records, err)
	}
}
func TestTransactionSelection(t *testing.T) {
	session, s, tx := fixture(t)
	ctx := context.Background()
	if _, err := s.SelectTransaction(ctx, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectTransaction(ctx, strings.Repeat("0", 32), true); err == nil {
		t.Fatal("missing transaction accepted")
	}
	if _, err := s.SelectTransaction(ctx, "../../path", true); err == nil {
		t.Fatal("invalid transaction ID accepted")
	}
	head, _ := s.Repository.Head(ctx)
	if _, err := (transaction.Service{Store: session.Store}).Begin(ctx, "second", "main", head); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectTransaction(ctx, "", true); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("%v", err)
	}
	if _, err := s.SelectTransaction(ctx, tx, true); err != nil {
		t.Fatal(err)
	}
}
func TestUnbornTransactionBaseRejected(t *testing.T) {
	session, s, _ := fixture(t)
	ctx := context.Background()
	tx, err := (transaction.Service{Store: session.Store}).Begin(ctx, "unborn", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, tx.ID, "backend"); err == nil {
		t.Fatal("unborn base accepted")
	}
	records, err := s.Store.ListWorkspaces(ctx, tx.ID)
	if err != nil || len(records) != 0 {
		t.Fatalf("%v %v", records, err)
	}
}
func TestDirtyAndIgnoredRemovalRejected(t *testing.T) {
	for _, kind := range []string{"tracked", "staged", "untracked", "ignored"} {
		t.Run(kind, func(t *testing.T) {
			_, s, tx := fixture(t)
			w := mustCreate(t, s, tx, "backend")
			path := fullPath(s, w)
			filename := "tracked.txt"
			if kind == "untracked" {
				filename = "new.txt"
			}
			if kind == "ignored" {
				filename = "build.cache"
			}
			file := filepath.Join(path, filename)
			if err := os.WriteFile(file, []byte("developer changes"), 0644); err != nil {
				t.Fatal(err)
			}
			if kind == "staged" {
				gitCmd(t, path, "add", filename)
			}
			if _, err := s.Remove(context.Background(), tx, w.Name); err == nil {
				t.Fatal("dirty removal accepted")
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != "developer changes" {
				t.Fatalf("data lost: %q %v", data, err)
			}
			records, _ := s.Store.ListWorkspaces(context.Background(), tx)
			if records[0].State != workspace.Ready {
				t.Fatal("dirty rejection changed lifecycle")
			}
		})
	}
}
func TestCleanRemovalRetainsBranchAndHistory(t *testing.T) {
	_, s, tx := fixture(t)
	ctx := context.Background()
	w := mustCreate(t, s, tx, "backend")
	removed, err := s.Remove(ctx, tx, w.Name)
	if err != nil {
		t.Fatal(err)
	}
	if removed.State != workspace.Removed || removed.RemovedAt.IsZero() {
		t.Fatalf("%+v", removed)
	}
	if _, err := os.Lstat(fullPath(s, w)); !os.IsNotExist(err) {
		t.Fatalf("tree remains: %v", err)
	}
	gitCmd(t, s.Repository.Root, "show-ref", "--verify", "refs/heads/"+w.Branch)
	if _, err := os.Stat(filepath.Join(filepath.Dir(fullPath(s, w)), "owner.json")); err != nil {
		t.Fatal("tombstone missing")
	}
	if _, err := s.Create(ctx, tx, w.Name); err == nil {
		t.Fatal("M1 reused historical name")
	}
	if again, err := s.Remove(ctx, tx, w.Name); err != nil || again.State != workspace.Removed {
		t.Fatalf("%+v %v", again, err)
	}
}
func TestOwnershipManifestMismatch(t *testing.T) {
	for _, marker := range []string{"outer", "admin"} {
		t.Run(marker, func(t *testing.T) {
			_, s, tx := fixture(t)
			w := mustCreate(t, s, tx, "backend")
			path := filepath.Join(filepath.Dir(fullPath(s, w)), "owner.json")
			if marker == "admin" {
				path = filepath.Join(s.Repository.CommonDir, filepath.FromSlash(w.AdminPath), "agit-owner.json")
			}
			if err := os.WriteFile(path, []byte(`{"Version":1}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Remove(context.Background(), tx, w.Name); err == nil {
				t.Fatal("unowned removal accepted")
			}
			if _, err := os.Stat(fullPath(s, w)); err != nil {
				t.Fatal("workspace deleted")
			}
		})
	}
}
func TestMissingWorktreePreservesRegistration(t *testing.T) {
	_, s, tx := fixture(t)
	w := mustCreate(t, s, tx, "backend")
	path := fullPath(s, w)
	preserved := filepath.Join(t.TempDir(), "preserved")
	if err := os.Rename(path, preserved); err != nil {
		t.Fatal(err)
	}
	got, err := s.Status(context.Background(), tx, w.Name)
	if err == nil || got.State != workspace.Error {
		t.Fatalf("%+v %v", got, err)
	}
	trees, err := s.Repository.Worktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tree := range trees {
		if tree.Path == path {
			found = true
		}
	}
	if !found {
		t.Fatal("stale registration silently pruned")
	}
	gitCmd(t, s.Repository.Root, "show-ref", "--verify", "refs/heads/"+w.Branch)
}

type failingFinalization struct {
	*storage.Store
	state workspace.State
}

func (f *failingFinalization) UpdateWorkspace(ctx context.Context, w workspace.Workspace, from workspace.State) error {
	if w.State == f.state {
		return errors.New("injected final SQLite commit failure")
	}
	return f.Store.UpdateWorkspace(ctx, w, from)
}
func TestCreationFinalizationRecovery(t *testing.T) {
	session, s, tx := fixture(t)
	ctx := context.Background()
	s.Store = &failingFinalization{Store: session.Store, state: workspace.Ready}
	w, err := s.Create(ctx, tx, "backend")
	if err == nil || w.ID == "" {
		t.Fatalf("%+v %v", w, err)
	}
	records, _ := session.ListWorkspaces(ctx, tx)
	if records[0].State != workspace.Creating {
		t.Fatal("creation intent lost")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := project.Open(ctx, s.Repository)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	s.Store = restarted.Store
	recovered, err := s.Status(ctx, tx, "backend")
	if err != nil || recovered.State != workspace.Ready {
		t.Fatalf("%+v %v", recovered, err)
	}
}
func TestInterruptedCreationBeforeAdminMarkerRecovery(t *testing.T) {
	session, s, tx := fixture(t)
	ctx := context.Background()
	s.Store = &failingFinalization{Store: session.Store, state: workspace.Ready}
	w, err := s.Create(ctx, tx, "backend")
	if err == nil {
		t.Fatal("injection failed")
	}
	admin := filepath.Join(s.Repository.CommonDir, filepath.FromSlash(w.AdminPath), "agit-owner.json")
	if err := os.Remove(admin); err != nil {
		t.Fatal(err)
	}
	s.Store = session.Store
	recovered, err := s.Status(ctx, tx, w.Name)
	if err != nil || recovered.State != workspace.Ready {
		t.Fatalf("%+v %v", recovered, err)
	}
	if _, err := os.Stat(admin); err != nil {
		t.Fatal("admin marker not recovered")
	}
}
func TestIncompleteCreationEvidencePreserved(t *testing.T) {
	for _, kind := range []string{"changed-base", "missing-outer", "contradictory-admin"} {
		t.Run(kind, func(t *testing.T) {
			session, s, tx := fixture(t)
			ctx := context.Background()
			s.Store = &failingFinalization{Store: session.Store, state: workspace.Ready}
			w, err := s.Create(ctx, tx, "backend")
			if err == nil {
				t.Fatal("injection failed")
			}
			marker := filepath.Join(s.Repository.CommonDir, filepath.FromSlash(w.AdminPath), "agit-owner.json")
			if kind == "contradictory-admin" {
				if err := os.WriteFile(marker, []byte(`{"Version":1}`), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "changed-base" {
				gitCmd(t, fullPath(s, w), "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "new commit")
			}
			if kind == "missing-outer" {
				if err := os.Remove(filepath.Join(filepath.Dir(fullPath(s, w)), "owner.json")); err != nil {
					t.Fatal(err)
				}
			}
			s.Store = session.Store
			got, err := s.Status(ctx, tx, w.Name)
			if err == nil || got.State != workspace.Error {
				t.Fatalf("%+v %v", got, err)
			}
			if _, err := os.Stat(fullPath(s, w)); err != nil {
				t.Fatal("resources removed")
			}
			gitCmd(t, s.Repository.Root, "show-ref", "--verify", "refs/heads/"+w.Branch)
		})
	}
}
func TestRemovalFinalizationRecovery(t *testing.T) {
	session, s, tx := fixture(t)
	ctx := context.Background()
	w := mustCreate(t, s, tx, "backend")
	s.Store = &failingFinalization{Store: session.Store, state: workspace.Removed}
	if _, err := s.Remove(ctx, tx, w.Name); err == nil {
		t.Fatal("injection failed")
	}
	records, _ := session.ListWorkspaces(ctx, tx)
	if records[0].State != workspace.Removing {
		t.Fatal("removal intent lost")
	}
	s.Store = session.Store
	got, err := s.Status(ctx, tx, w.Name)
	if err != nil || got.State != workspace.Removed {
		t.Fatalf("%+v %v", got, err)
	}
	gitCmd(t, s.Repository.Root, "show-ref", "--verify", "refs/heads/"+w.Branch)
}
func TestInterruptedRemovalRequiresExplicitRetry(t *testing.T) {
	session, s, tx := fixture(t)
	ctx := context.Background()
	w := mustCreate(t, s, tx, "backend")
	w.State = workspace.Removing
	w.Operation = "remove"
	if err := session.UpdateWorkspace(ctx, w, workspace.Ready); err != nil {
		t.Fatal(err)
	}
	got, err := s.Status(ctx, tx, w.Name)
	if err != nil || got.State != workspace.Removing {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := os.Stat(fullPath(s, w)); err != nil {
		t.Fatal("read removed worktree")
	}
	if got, err := s.Remove(ctx, tx, w.Name); err != nil || got.State != workspace.Removed {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestWorkspacePathSymlinkRejected(t *testing.T) {
	_, s, tx := fixture(t)
	w := mustCreate(t, s, tx, "backend")
	path := fullPath(s, w)
	target := filepath.Join(t.TempDir(), "developer")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(context.Background(), tx, w.Name); err == nil {
		t.Fatal("symlink removal accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "tracked.txt")); err != nil {
		t.Fatal("developer data removed")
	}
}
func TestWorkspaceRemovalFromSelfRejected(t *testing.T) {
	_, s, tx := fixture(t)
	w := mustCreate(t, s, tx, "backend")
	linked, err := git.Discover(context.Background(), fullPath(s, w))
	if err != nil {
		t.Fatal(err)
	}
	s.Repository = linked
	if _, err := s.Remove(context.Background(), tx, w.Name); err == nil {
		t.Fatal("removed invoking worktree")
	}
}
func TestLockedAndNestedRepositoryRemovalRejected(t *testing.T) {
	for _, kind := range []string{"locked", "nested"} {
		t.Run(kind, func(t *testing.T) {
			_, s, tx := fixture(t)
			w := mustCreate(t, s, tx, "backend")
			path := fullPath(s, w)
			if kind == "locked" {
				gitCmd(t, s.Repository.Root, "worktree", "lock", path)
			} else {
				nested := filepath.Join(path, "nested")
				if err := os.Mkdir(nested, 0755); err != nil {
					t.Fatal(err)
				}
				gitCmd(t, nested, "init")
			}
			if _, err := s.Remove(context.Background(), tx, w.Name); err == nil {
				t.Fatal("unsafe removal accepted")
			}
		})
	}
}
func TestReconciliationPersistenceFailurePropagates(t *testing.T) {
	session, s, tx := fixture(t)
	ctx := context.Background()
	w := mustCreate(t, s, tx, "backend")
	path := fullPath(s, w)
	if err := os.Rename(path, filepath.Join(t.TempDir(), "preserve")); err != nil {
		t.Fatal(err)
	}
	s.Store = &failingFinalization{Store: session.Store, state: workspace.Error}
	if _, err := s.List(ctx, tx); err == nil {
		t.Fatal("reconciliation persistence failure hidden")
	}
}

// A subprocess executable boundary simulates Git errors before/after native mutation.
// Production code contains no test-only failure switches.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "git" && os.Getenv("AGIT_GIT_TEST_MODE") != "" {
		args := os.Args[1:]
		target := os.Getenv("AGIT_GIT_TEST_OPERATION")
		if target == "" {
			target = "add"
		}
		add := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "worktree" && args[i+1] == target {
				add = true
				break
			}
		}
		if add && os.Getenv("AGIT_GIT_TEST_MODE") == "before" {
			os.Exit(1)
		}
		cmd := exec.Command(os.Getenv("AGIT_REAL_GIT"), args...)
		cmd.Env = os.Environ()
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			os.Exit(1)
		}
		if add {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestPartialGitCreationFailures(t *testing.T) {
	for _, mode := range []string{"before", "after"} {
		t.Run(mode, func(t *testing.T) {
			session, s, tx := fixture(t)
			ctx := context.Background()
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			helper := t.TempDir()
			testBinary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(testBinary, filepath.Join(helper, "git")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGIT_REAL_GIT", realGit)
			t.Setenv("AGIT_GIT_TEST_MODE", mode)
			t.Setenv("PATH", helper+string(os.PathListSeparator)+os.Getenv("PATH"))
			w, err := s.Create(ctx, tx, "backend")
			if err == nil || w.ID == "" {
				t.Fatalf("partial failure not observed: %+v %v", w, err)
			}
			records, err := session.ListWorkspaces(ctx, tx)
			if err != nil || records[0].State != workspace.Error {
				t.Fatalf("%v %v", records, err)
			}
			got, err := s.Status(ctx, tx, w.Name)
			if mode == "before" {
				if err == nil || got.State != workspace.Error {
					t.Fatalf("incomplete intent recovered: %+v %v", got, err)
				}
			} else {
				if err != nil || got.State != workspace.Ready {
					t.Fatalf("complete evidence not recovered: %+v %v", got, err)
				}
			}
		})
	}
}

// Reserve a colliding branch after the preflight but before Git mutation.
type branchCollisionStore struct {
	*storage.Store
	t      *testing.T
	root   string
	branch string
}

func (f *branchCollisionStore) InsertWorkspace(ctx context.Context, w workspace.Workspace) error {
	if err := f.Store.InsertWorkspace(ctx, w); err != nil {
		return err
	}
	f.branch = w.Branch
	gitCmd(f.t, f.root, "branch", w.Branch, w.BaseCommit)
	return nil
}
func TestConcurrentExternalBranchCollisionPreserved(t *testing.T) {
	session, s, tx := fixture(t)
	ctx := context.Background()
	collision := &branchCollisionStore{Store: session.Store, t: t, root: s.Repository.Root}
	s.Store = collision
	w, err := s.Create(ctx, tx, "backend")
	if err == nil || w.State != workspace.Error {
		t.Fatalf("%+v %v", w, err)
	}
	gitCmd(t, s.Repository.Root, "show-ref", "--verify", "refs/heads/"+collision.branch)
	if _, err := os.Stat(fullPath(s, w)); !os.IsNotExist(err) {
		t.Fatal("colliding branch adopted")
	}
	if _, err := s.Status(ctx, tx, w.Name); err == nil {
		t.Fatal("branch alone establishes ownership")
	}
}
func TestGitOperationInProgressBlocksRemoval(t *testing.T) {
	_, s, tx := fixture(t)
	w := mustCreate(t, s, tx, "backend")
	marker := filepath.Join(s.Repository.CommonDir, filepath.FromSlash(w.AdminPath), "MERGE_HEAD")
	if err := os.WriteFile(marker, []byte(w.BaseCommit+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(context.Background(), tx, w.Name); err == nil {
		t.Fatal("merge in progress removed")
	}
}

func TestCreationFromLinkedWorktree(t *testing.T) {
	session, s, tx := fixture(t)
	ctx := context.Background()
	first := mustCreate(t, s, tx, "backend")
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	linked, err := git.Discover(ctx, fullPath(s, first))
	if err != nil {
		t.Fatal(err)
	}
	shared, err := project.Open(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	defer shared.Close()
	s.Store = shared.Store
	s.Repository = linked
	second := mustCreate(t, s, tx, "frontend")
	if second.State != workspace.Ready || second.TransactionID != first.TransactionID {
		t.Fatal("linked creation did not share transaction")
	}
}
func TestEmptyDeveloperDirectoryBlocksRemoval(t *testing.T) {
	_, s, tx := fixture(t)
	w := mustCreate(t, s, tx, "backend")
	empty := filepath.Join(fullPath(s, w), "developer-empty")
	if err := os.Mkdir(empty, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(context.Background(), tx, w.Name); err == nil {
		t.Fatal("empty developer directory deleted")
	}
	if _, err := os.Stat(empty); err != nil {
		t.Fatal("developer directory lost")
	}
}
func TestAdvancedWorkspaceCommitRemainsOwned(t *testing.T) {
	_, s, tx := fixture(t)
	w := mustCreate(t, s, tx, "backend")
	ctx := context.Background()
	gitCmd(t, fullPath(s, w), "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "workspace progress")
	inspected, err := s.Status(ctx, tx, w.Name)
	if err != nil || inspected.State != workspace.Ready {
		t.Fatalf("%+v %v", inspected, err)
	}
	if _, err := s.Remove(ctx, tx, w.Name); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, s.Repository.Root, "show-ref", "--verify", "refs/heads/"+w.Branch)
}

func TestHiddenIndexChangesBlockRemoval(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			_, s, tx := fixture(t)
			w := mustCreate(t, s, tx, "backend")
			path := fullPath(s, w)
			gitCmd(t, path, "update-index", flag, "tracked.txt")
			file := filepath.Join(path, "tracked.txt")
			if err := os.WriteFile(file, []byte("hidden developer changes"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Remove(context.Background(), tx, w.Name); err == nil {
				t.Fatal("hidden edits discarded")
			}
			data, err := os.ReadFile(file)
			if err != nil || string(data) != "hidden developer changes" {
				t.Fatal("developer data lost")
			}
		})
	}
}

func TestPartialGitRemovalFailures(t *testing.T) {
	for _, mode := range []string{"before", "after"} {
		t.Run(mode, func(t *testing.T) {
			_, s, tx := fixture(t)
			ctx := context.Background()
			w := mustCreate(t, s, tx, "backend")
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			originalPath := os.Getenv("PATH")
			helper := t.TempDir()
			testBinary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(testBinary, filepath.Join(helper, "git")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGIT_REAL_GIT", realGit)
			t.Setenv("AGIT_GIT_TEST_MODE", mode)
			t.Setenv("AGIT_GIT_TEST_OPERATION", "remove")
			t.Setenv("PATH", helper+string(os.PathListSeparator)+originalPath)
			if _, err := s.Remove(ctx, tx, w.Name); err == nil {
				t.Fatal("partial Git removal failure hidden")
			}
			got, err := s.Status(ctx, tx, w.Name)
			if mode == "before" {
				if err == nil || got.State != workspace.Error {
					t.Fatalf("%+v %v", got, err)
				}
				if _, err := os.Stat(fullPath(s, w)); err != nil {
					t.Fatal("failed remove deleted worktree")
				}
				t.Setenv("PATH", originalPath)
				if removed, err := s.Remove(ctx, tx, w.Name); err != nil || removed.State != workspace.Removed {
					t.Fatalf("retry: %+v %v", removed, err)
				}
			} else if err != nil || got.State != workspace.Removed {
				t.Fatalf("completed removal not reconciled: %+v %v", got, err)
			}
			gitCmd(t, s.Repository.Root, "show-ref", "--verify", "refs/heads/"+w.Branch)
		})
	}
}
