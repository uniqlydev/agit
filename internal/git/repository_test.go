package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func command(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func TestDiscoveryAndGitState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	command(t, dir, "init", "-b", "main")
	nested := filepath.Join(dir, "nested", "deep")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	repo, err := Discover(ctx, nested)
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := filepath.EvalSymlinks(dir)
	actual, _ := filepath.EvalSymlinks(repo.Root)
	if actual != expected {
		t.Fatalf("root %s != %s", actual, expected)
	}
	if branch, err := repo.Branch(ctx); err != nil || branch != "main" {
		t.Fatalf("%s %v", branch, err)
	}
	if head, err := repo.Head(ctx); err != nil || head != "" {
		t.Fatalf("%s %v", head, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	if status, err := repo.WorkingTreeStatus(ctx); err != nil || !strings.Contains(status, "?? file.txt") {
		t.Fatalf("%s %v", status, err)
	}
	command(t, dir, "add", "file.txt")
	command(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "test fixture")
	expectedHead := command(t, dir, "rev-parse", "HEAD")
	if head, err := repo.Head(ctx); err != nil || head != expectedHead {
		t.Fatalf("%s %v", head, err)
	}
	command(t, dir, "checkout", "--detach")
	if branch, err := repo.Branch(ctx); err != nil || branch != "HEAD (detached)" {
		t.Fatalf("%s %v", branch, err)
	}
	command(t, dir, "checkout", "--orphan", "new-branch")
	if head, err := repo.Head(ctx); err != nil || head != "" {
		t.Fatalf("orphan: %s %v", head, err)
	}
}
func TestDiscoveryRejectsNonWorkingRepository(t *testing.T) {
	dir := t.TempDir()
	if _, err := Discover(context.Background(), dir); err == nil {
		t.Fatal("accepted non-repository")
	}
	command(t, dir, "init", "--bare")
	if _, err := Discover(context.Background(), dir); err == nil {
		t.Fatal("accepted bare repository")
	}
}
func TestCanceledDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, t.TempDir()); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestBranchCollisionAndCheckoutFilters(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	command(t, dir, "init", "-b", "main")
	command(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fixture")
	repo, err := Discover(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	command(t, dir, "branch", "agit/collision")
	for _, branch := range []string{"agit/collision", "agit/collision/nested", "agit", "AGIT/COLLISION"} {
		if err := repo.BranchAvailable(ctx, branch); err == nil {
			t.Fatalf("accepted collision %s", branch)
		}
	}
	if err := repo.BranchAvailable(ctx, "agit/new-branch"); err != nil {
		t.Fatal(err)
	}
	command(t, dir, "config", "filter.test.smudge", "cat")
	if err := repo.CheckCheckoutSafety(ctx); err == nil {
		t.Fatal("checkout filter accepted")
	}
}
func TestCommandFailureAndRoutingEnvironment(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	command(t, dir, "init", "-b", "main")
	if _, err := Raw(ctx, dir, "definitely-not-a-command"); err == nil || ExitCode(err) < 0 {
		t.Fatalf("missing typed exit code: %v", err)
	}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "not-a-repository"))
	if _, err := Discover(ctx, dir); err != nil {
		t.Fatal("routing environment was not sanitized:", err)
	}
}
