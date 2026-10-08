package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uniqlydev/agit/internal/storage"
)

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := NewRootCommand("test-version")
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCommand(t, dir, "init", "-b", "main")
	t.Chdir(dir)
	return dir
}
func TestInitializationAndTransactions(t *testing.T) {
	dir := newRepo(t)
	original := "# developer rules\n*.log"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "init"); err != nil || !strings.Contains(out, "Initialized AGIT") {
		t.Fatalf("%s %v", out, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original+"\n/.agit/\n" {
		t.Fatalf("ignore contents: %q", data)
	}
	before, err := os.ReadFile(filepath.Join(dir, ".agit", storage.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "init"); err == nil {
		t.Fatal("repeat init succeeded")
	}
	after, err := os.ReadFile(filepath.Join(dir, ".agit", storage.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("repeat init changed database")
	}
	if out, err := runCLI(t, "status"); err != nil || !strings.Contains(out, "Branch: main") || !strings.Contains(out, "Active transactions: 0") {
		t.Fatalf("%s %v", out, err)
	}
	objective := "Implement authentication ' safely; DROP TABLE transactions;"
	if out, err := runCLI(t, "tx", "begin", objective); err != nil || !strings.Contains(out, "State: active") {
		t.Fatalf("%s %v", out, err)
	}
	store, err := storage.Open(context.Background(), filepath.Join(dir, ".agit", storage.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	transactions, err := store.List(context.Background(), false)
	store.Close()
	if err != nil || len(transactions) != 1 {
		t.Fatalf("transactions: %v %v", transactions, err)
	}
	if tx := transactions[0]; tx.Objective != objective || tx.ID == "" || tx.Branch != "main" || tx.HeadCommit != "" {
		t.Fatalf("transaction: %+v", tx)
	}
	if out, err := runCLI(t, "tx", "status"); err != nil || !strings.Contains(out, transactions[0].ID) {
		t.Fatalf("%s %v", out, err)
	}
	if out, err := runCLI(t, "status"); err != nil || !strings.Contains(out, "Active transactions: 1") {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := runCLI(t, "tx", "begin", "  "); err == nil {
		t.Fatal("blank objective accepted")
	}
}
func TestOutsideGit(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := runCLI(t, "init"); err == nil {
		t.Fatal("init outside Git succeeded")
	}
	if _, err := os.Stat(".agit"); !os.IsNotExist(err) {
		t.Fatalf("unexpected AGIT directory: %v", err)
	}
}
func TestMissingInitialization(t *testing.T) {
	newRepo(t)
	for _, args := range [][]string{{"status"}, {"tx", "status"}, {"tx", "begin", "objective"}} {
		if _, err := runCLI(t, args...); err == nil || !strings.Contains(err.Error(), "run agit init") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if _, err := os.Stat(".agit"); !os.IsNotExist(err) {
		t.Fatal("read command created AGIT")
	}
}
func TestNestedInitialization(t *testing.T) {
	dir := newRepo(t)
	nested := filepath.Join(dir, "nested", "deep")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	if _, err := runCLI(t, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".agit", storage.DatabaseName)); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "tx", "begin", "nested objective"); err != nil {
		t.Fatal(err)
	}
}
func TestPreserveExistingAGIT(t *testing.T) {
	dir := newRepo(t)
	path := filepath.Join(dir, ".agit")
	if err := os.WriteFile(path, []byte("developer file"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "init"); err == nil {
		t.Fatal("overwrote existing file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "developer file" {
		t.Fatalf("%q %v", data, err)
	}
}
func TestIgnoreSymlinkIsPreserved(t *testing.T) {
	dir := newRepo(t)
	target := filepath.Join(dir, "developer.ignore")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "init"); err == nil {
		t.Fatal("modified symlink")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "original" {
		t.Fatal("changed target")
	}
	if _, err := os.Stat(filepath.Join(dir, ".agit")); !os.IsNotExist(err) {
		t.Fatal("failed init not cleaned up")
	}
}
func TestVersionAndArguments(t *testing.T) {
	if out, err := runCLI(t, "--version"); err != nil || !strings.Contains(out, "test-version") {
		t.Fatalf("%s %v", out, err)
	}
	for _, args := range [][]string{{"init", "extra"}, {"tx", "begin"}, {"tx", "begin", "one", "two"}, {"status", "extra"}} {
		if _, err := runCLI(t, args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestWorkspaceCLIWorkflow(t *testing.T) {
	dir := newRepo(t)
	gitCommand(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fixture")
	if _, err := runCLI(t, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "tx", "begin", "objective"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"tx", "workspace", "create", "backend"}, {"tx", "workspace", "create", "frontend"}, {"tx", "workspace", "list"}, {"tx", "workspace", "status", "backend"}} {
		if out, err := runCLI(t, args...); err != nil || !strings.Contains(out, "State: ready") {
			t.Fatalf("%v: %s %v", args, out, err)
		}
	}
	if out, err := runCLI(t, "tx", "workspace", "remove", "backend"); err != nil || !strings.Contains(out, "State: removed") {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := runCLI(t, "tx", "workspace", "status", "missing"); err == nil {
		t.Fatal("missing workspace accepted")
	}
}

// Run the real Cobra entry path in independent processes without building a second binary.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("AGIT_TEST_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("AGIT_TEST_ARGS")), &args); err != nil {
		os.Exit(2)
	}
	cmd := NewRootCommand("test")
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
func childCommand(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIProcess$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "AGIT_TEST_CHILD=1", "AGIT_TEST_ARGS="+string(data))
	return cmd
}
func TestConcurrentWorkspaceCreationProcesses(t *testing.T) {
	for _, sameName := range []bool{false, true} {
		t.Run(fmt.Sprint(sameName), func(t *testing.T) {
			dir := newRepo(t)
			gitCommand(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fixture")
			if _, err := runCLI(t, "init"); err != nil {
				t.Fatal(err)
			}
			if _, err := runCLI(t, "tx", "begin", "objective"); err != nil {
				t.Fatal(err)
			}
			second := "frontend"
			if sameName {
				second = "backend"
			}
			firstCmd := childCommand(t, dir, "tx", "workspace", "create", "backend")
			secondCmd := childCommand(t, dir, "tx", "workspace", "create", second)
			var firstOut, secondOut bytes.Buffer
			firstCmd.Stdout = &firstOut
			firstCmd.Stderr = &firstOut
			secondCmd.Stdout = &secondOut
			secondCmd.Stderr = &secondOut
			if err := firstCmd.Start(); err != nil {
				t.Fatal(err)
			}
			if err := secondCmd.Start(); err != nil {
				firstCmd.Wait()
				t.Fatal(err)
			}
			firstErr, secondErr := firstCmd.Wait(), secondCmd.Wait()
			if sameName {
				if (firstErr == nil) == (secondErr == nil) {
					t.Fatalf("expected exactly one successful duplicate create: %v %v\n%s\n%s", firstErr, secondErr, firstOut.String(), secondOut.String())
				}
			} else if firstErr != nil || secondErr != nil {
				t.Fatalf("%v %v\n%s\n%s", firstErr, secondErr, firstOut.String(), secondOut.String())
			}
			out, err := runCLI(t, "tx", "workspace", "list")
			if err != nil {
				t.Fatal(err)
			}
			count := 2
			if sameName {
				count = 1
			}
			if strings.Count(out, "State: ready") != count {
				t.Fatalf("%s", out)
			}
		})
	}
}
func TestWorkspaceExplicitTransactionFlag(t *testing.T) {
	dir := newRepo(t)
	gitCommand(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fixture")
	if _, err := runCLI(t, "init"); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "tx", "begin", "first")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out)
	id := fields[1]
	if _, err := runCLI(t, "tx", "begin", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, "tx", "workspace", "create", "backend"); err == nil {
		t.Fatal("ambiguous implicit selection accepted")
	}
	for _, args := range [][]string{{"create", "backend"}, {"list"}, {"status", "backend"}, {"remove", "backend"}} {
		command := append([]string{"tx", "workspace"}, args...)
		command = append(command, "--tx", id)
		if out, err := runCLI(t, command...); err != nil {
			t.Fatalf("%v: %s %v", command, out, err)
		}
	}
}

func TestConcurrentRepositoryInitialization(t *testing.T) {
	dir := newRepo(t)
	gitCommand(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fixture")
	first, second := childCommand(t, dir, "init"), childCommand(t, dir, "init")
	var a, b bytes.Buffer
	first.Stdout = &a
	first.Stderr = &a
	second.Stdout = &b
	second.Stderr = &b
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		first.Wait()
		t.Fatal(err)
	}
	e1, e2 := first.Wait(), second.Wait()
	if (e1 == nil) == (e2 == nil) {
		t.Fatalf("expected one successful init: %v %v\n%s\n%s", e1, e2, a.String(), b.String())
	}
	if _, err := runCLI(t, "status"); err != nil {
		t.Fatal(err)
	}
}
