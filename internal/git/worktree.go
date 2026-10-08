package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func (r *Repository) WorkingTreeStatus(ctx context.Context) (string, error) {
	return run(ctx, r.Root, "status", "--short", "--untracked-files=normal")
}

type Worktree struct {
	Path, Head, Branch     string
	Bare, Locked, Prunable bool
}

func (r *Repository) Worktrees(ctx context.Context) ([]Worktree, error) {
	output, err := Raw(ctx, r.Root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var result []Worktree
	var current *Worktree
	for _, field := range strings.Split(string(output), "\x00") {
		if field == "" {
			current = nil
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		if key == "worktree" {
			result = append(result, Worktree{Path: value})
			current = &result[len(result)-1]
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("invalid Git worktree listing")
		}
		switch key {
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = value
		case "bare":
			current.Bare = true
		case "locked":
			current.Locked = true
		case "prunable":
			current.Prunable = true
		case "detached":
		default:
			return nil, fmt.Errorf("unknown Git worktree field %q", key)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("Git returned no worktrees")
	}
	return result, nil
}

var objectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (r *Repository) VerifyCommit(ctx context.Context, commit string) error {
	if !objectID.MatchString(commit) {
		return fmt.Errorf("workspace creation requires a committed transaction base; commit first and begin a new transaction")
	}
	kind, err := run(ctx, r.Root, "cat-file", "-t", commit)
	if err != nil {
		return err
	}
	if kind != "commit" {
		return fmt.Errorf("transaction base is not a commit")
	}
	return nil
}
func (r *Repository) BranchAvailable(ctx context.Context, branch string) error {
	if _, err := Raw(ctx, r.Root, "check-ref-format", "refs/heads/"+branch); err != nil {
		return err
	}
	refs, err := run(ctx, r.Root, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return err
	}
	want := strings.ToLower("refs/heads/" + branch)
	for _, ref := range strings.Split(refs, "\n") {
		ref = strings.ToLower(ref)
		if ref == want || strings.HasPrefix(want, ref+"/") || strings.HasPrefix(ref, want+"/") {
			return fmt.Errorf("workspace branch collision: %s", ref)
		}
	}
	return nil
}
func (r *Repository) CheckCheckoutSafety(ctx context.Context) error {
	output, err := Raw(ctx, r.Root, "config", "--get-regexp", `^filter\..*\.(process|smudge|clean)$`)
	if ExitCode(err) == 1 {
		return nil
	}
	if err != nil {
		return err
	}
	if len(output) > 0 {
		return fmt.Errorf("external Git checkout filters are unsupported for M1 workspace creation")
	}
	return nil
}
func (r *Repository) AddWorktree(ctx context.Context, path, branch, base string) error {
	_, err := Raw(ctx, r.Root, "worktree", "add", "-b", branch, path, base)
	return err
}
func (r *Repository) RemoveWorktree(ctx context.Context, path string) error {
	_, err := Raw(ctx, r.Root, "worktree", "remove", path)
	return err
}
func (r *Repository) CheckIgnored(ctx context.Context, path string) error {
	if _, err := Raw(ctx, r.Root, "check-ignore", "--quiet", "--", path); err != nil {
		return fmt.Errorf("managed workspace layout must be ignored by Git: %w", err)
	}
	return nil
}
func (r *Repository) CleanForRemoval(ctx context.Context) error {
	// Git remove alone can discard ignored files. Explicitly include them here.
	status, err := Raw(ctx, r.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if len(status) > 0 {
		return fmt.Errorf("workspace has tracked, untracked, or ignored changes; removal rejected")
	}
	// Index flags can hide developer edits from status and Git's removal check.
	flags, err := Raw(ctx, r.Root, "ls-files", "-v", "-z")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(string(flags), "\x00") {
		if entry == "" {
			continue
		}
		if entry[0] == 'S' || (entry[0] >= 'a' && entry[0] <= 'z') {
			return fmt.Errorf("assume-unchanged or skip-worktree index flags block safe removal")
		}
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "BISECT_LOG", "index.lock"} {
		if _, err := os.Lstat(filepath.Join(r.GitDir, name)); err == nil {
			return fmt.Errorf("workspace Git operation in progress: %s", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	modules, err := run(ctx, r.Root, "submodule", "status", "--recursive")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(modules, "\n") {
		if line != "" && !strings.HasPrefix(line, "-") {
			return fmt.Errorf("initialized submodules block workspace removal")
		}
	}
	// Empty developer-created directories are omitted by Git status too.
	tracked, err := Raw(ctx, r.Root, "ls-files", "-z")
	if err != nil {
		return err
	}
	expectedDirs := map[string]bool{r.Root: true}
	for _, file := range strings.Split(string(tracked), "\x00") {
		if file == "" {
			continue
		}
		path := filepath.Dir(filepath.Join(r.Root, file))
		for path != r.Root && path != filepath.Dir(path) {
			expectedDirs[path] = true
			path = filepath.Dir(path)
		}
	}
	return filepath.WalkDir(r.Root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != filepath.Join(r.Root, ".git") && d.Name() == ".git" {
			return fmt.Errorf("nested repository blocks removal: %s", path)
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() && !expectedDirs[path] {
			// An uninitialized submodule is a tracked gitlink, not an unknown directory.
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				trackedDir := false
				relative, err := filepath.Rel(r.Root, path)
				if err != nil {
					return err
				}
				for _, file := range strings.Split(string(tracked), "\x00") {
					if filepath.ToSlash(relative) == file {
						trackedDir = true
						break
					}
				}
				if !trackedDir {
					return fmt.Errorf("untracked empty directory blocks removal: %s", path)
				}
			}
		}
		return nil
	})
}
