package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Repository struct{ Root, CommonDir, GitDir string }

// CommandError retains exit status and cancellation instead of treating failures as clean.
type CommandError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("git %s: %s (%v)", strings.Join(e.Args, " "), strings.TrimSpace(e.Stderr), e.Err)
}
func (e *CommandError) Unwrap() error { return e.Err }
func ExitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func Raw(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "submodule.recurse=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}, args...)...)
	// Repository-routing and injected Git configuration must not redirect owned operations.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	output, err := cmd.Output()
	if err != nil {
		stderr := ""
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			stderr = string(exit.Stderr)
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return output, &CommandError{Args: args, Stderr: stderr, Err: err}
	}
	return output, nil
}
func run(ctx context.Context, dir string, args ...string) (string, error) {
	output, err := Raw(ctx, dir, args...)
	return strings.TrimSuffix(string(output), "\n"), err
}
func Discover(ctx context.Context, dir string) (*Repository, error) {
	root, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("an existing Git working repository is required: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	common, err := run(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return nil, err
	}
	admin, err := run(ctx, root, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return nil, err
	}
	admin, err = filepath.EvalSymlinks(admin)
	if err != nil {
		return nil, err
	}
	return &Repository{Root: root, CommonDir: common, GitDir: admin}, nil
}
func (r *Repository) Branch(ctx context.Context) (string, error) {
	branch, err := run(ctx, r.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		return branch, nil
	}
	if ExitCode(err) != 1 {
		return "", err
	}
	head, headErr := r.Head(ctx)
	if headErr != nil {
		return "", headErr
	}
	if head == "" {
		return "", err
	}
	return "HEAD (detached)", nil
}
func (r *Repository) Head(ctx context.Context) (string, error) {
	head, err := run(ctx, r.Root, "rev-parse", "--verify", "HEAD")
	if err == nil {
		return head, nil
	}
	ref, symErr := run(ctx, r.Root, "symbolic-ref", "--quiet", "HEAD")
	if symErr != nil {
		return "", err
	}
	if _, checkErr := Raw(ctx, r.Root, "show-ref", "--verify", "--quiet", ref); ExitCode(checkErr) == 1 {
		return "", nil
	}
	return "", err
}
