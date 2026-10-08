package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uniqlydev/agit/internal/git"
	"github.com/uniqlydev/agit/internal/safeio"
	"github.com/uniqlydev/agit/internal/transaction"
)

// Persistence is the focused failure-injection boundary. The caller holds the
// repository lock for the lifetime of a Service operation.
type Persistence interface {
	List(context.Context, bool) ([]transaction.Transaction, error)
	InsertWorkspace(context.Context, Workspace) error
	UpdateWorkspace(context.Context, Workspace, State) error
	ListWorkspaces(context.Context, string) ([]Workspace, error)
}
type Service struct {
	Store                     Persistence
	Repository                *git.Repository
	MetadataDir, RepositoryID string
}
type manifest struct {
	Version                                                                            int
	RepositoryID, WorkspaceID, TransactionID, Branch, Path, BaseCommit, OperationToken string
}

func (s *Service) owner(w Workspace) manifest {
	return manifest{Version: 1, RepositoryID: s.RepositoryID, WorkspaceID: w.ID, TransactionID: w.TransactionID, Branch: w.Branch, Path: w.Path, BaseCommit: w.BaseCommit, OperationToken: w.OperationToken}
}
func (s *Service) SelectTransaction(ctx context.Context, id string, create bool) (transaction.Transaction, error) {
	if id != "" && !ValidID(id) {
		return transaction.Transaction{}, fmt.Errorf("invalid transaction ID")
	}
	txs, err := s.Store.List(ctx, id == "")
	if err != nil {
		return transaction.Transaction{}, err
	}
	if id == "" {
		if len(txs) == 0 {
			return transaction.Transaction{}, fmt.Errorf("no eligible active transaction; run agit tx begin")
		}
		if len(txs) != 1 {
			ids := []string{}
			for _, tx := range txs {
				ids = append(ids, tx.ID)
			}
			return transaction.Transaction{}, fmt.Errorf("ambiguous transaction selection; supply --tx; active IDs: %s", strings.Join(ids, ", "))
		}
		id = txs[0].ID
	}
	for _, tx := range txs {
		if tx.ID == id {
			if create && tx.State != transaction.StateActive {
				return transaction.Transaction{}, fmt.Errorf("workspace creation requires an active transaction")
			}
			return tx, nil
		}
	}
	return transaction.Transaction{}, fmt.Errorf("transaction %s not found in this repository", id)
}
func (s *Service) root() (string, error) {
	if !ValidID(s.RepositoryID) {
		return "", fmt.Errorf("invalid repository identity")
	}
	if err := safeio.Directory(s.MetadataDir); err != nil {
		return "", err
	}
	return filepath.Join(s.MetadataDir, "workspaces"), nil
}
func (s *Service) ensureRoot() error {
	root, err := s.root()
	if err != nil {
		return err
	}
	err = os.Mkdir(root, 0700)
	if err != nil && !os.IsExist(err) {
		return err
	}
	if err == nil {
		if err := safeio.WriteExclusive(filepath.Join(root, "format"), []byte("AGIT managed workspaces v1\n")); err != nil {
			return err
		}
		if err := safeio.SyncDir(s.MetadataDir); err != nil {
			return err
		}
	}
	return s.checkRoot(root)
}
func (s *Service) checkRoot(root string) error {
	if err := safeio.Directory(root); err != nil {
		return err
	}
	data, err := safeio.Read(filepath.Join(root, "format"))
	if err != nil {
		return err
	}
	if string(data) != "AGIT managed workspaces v1\n" {
		return fmt.Errorf("unrecognized workspace directory; preserve and inspect manually")
	}
	return nil
}
func (s *Service) path(w Workspace) (string, error) {
	if !ValidID(w.ID) || !ValidID(w.TransactionID) || !ValidID(w.OperationToken) {
		return "", fmt.Errorf("invalid workspace identity")
	}
	if err := ValidateName(w.Name); err != nil {
		return "", err
	}
	expected := filepath.ToSlash(filepath.Join("workspaces", w.ID, "tree"))
	if w.Path != expected || w.Branch != "agit/"+w.TransactionID+"/"+w.ID+"-"+w.Name {
		return "", fmt.Errorf("workspace path/branch does not match immutable identity")
	}
	root, err := s.root()
	if err != nil {
		return "", err
	}
	if err := s.checkRoot(root); err != nil {
		return "", err
	}
	container := filepath.Join(root, w.ID)
	if err := safeio.Directory(container); err != nil {
		return "", err
	}
	var owner manifest
	if err := safeio.ReadJSON(filepath.Join(container, "owner.json"), &owner); err != nil {
		return "", fmt.Errorf("workspace ownership manifest unavailable: %w", err)
	}
	if owner != s.owner(w) {
		return "", fmt.Errorf("workspace ownership manifest mismatch")
	}
	return filepath.Join(container, "tree"), nil
}
func safeAdmin(common, admin string) (string, error) {
	relative, err := filepath.Rel(common, admin)
	if err != nil {
		return "", err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 2 || parts[0] != "worktrees" || parts[1] == ".." || parts[1] == "" {
		return "", fmt.Errorf("unexpected Git administrative identity: %s", admin)
	}
	for _, path := range []string{filepath.Join(common, "worktrees"), admin} {
		if err := safeio.Directory(path); err != nil {
			return "", err
		}
	}
	return filepath.ToSlash(relative), nil
}

// inspect verifies Git ownership. Only pending creation may recover a missing
// administrative marker; an existing contradictory marker is never replaced.
func (s *Service) inspect(ctx context.Context, w Workspace, recoverCreation bool) (*git.Repository, string, error) {
	path, err := s.path(w)
	if err != nil {
		return nil, "", err
	}
	if err := safeio.Directory(path); err != nil {
		return nil, "", err
	}
	trees, err := s.Repository.Worktrees(ctx)
	if err != nil {
		return nil, "", err
	}
	found := false
	for _, tree := range trees {
		if filepath.Clean(tree.Path) != path {
			continue
		}
		found = true
		if tree.Bare || tree.Branch != "refs/heads/"+w.Branch || tree.Prunable {
			return nil, "", fmt.Errorf("Git worktree registration contradicts workspace identity")
		}
		if recoverCreation && tree.Head != w.BaseCommit {
			return nil, "", fmt.Errorf("interrupted creation HEAD differs from expected base; manual intervention required")
		}
	}
	if !found {
		return nil, "", fmt.Errorf("workspace is not registered with Git")
	}
	repo, err := git.Discover(ctx, path)
	if err != nil {
		return nil, "", err
	}
	if repo.Root != path || repo.CommonDir != s.Repository.CommonDir {
		return nil, "", fmt.Errorf("workspace repository identity mismatch")
	}
	gitfile, err := safeio.Read(filepath.Join(path, ".git"))
	if err != nil || !strings.HasPrefix(string(gitfile), "gitdir: ") {
		return nil, "", fmt.Errorf("workspace .git is not a regular native gitfile")
	}
	if recoverCreation {
		head, err := repo.Head(ctx)
		if err != nil {
			return nil, "", err
		}
		if head != w.BaseCommit {
			return nil, "", fmt.Errorf("interrupted creation HEAD differs from base")
		}
		if err := repo.CleanForRemoval(ctx); err != nil {
			return nil, "", fmt.Errorf("interrupted creation checkout cannot be verified: %w", err)
		}
	}
	admin, err := safeAdmin(repo.CommonDir, repo.GitDir)
	if err != nil {
		return nil, "", err
	}
	if w.AdminPath != "" && w.AdminPath != admin {
		return nil, "", fmt.Errorf("workspace administrative identity mismatch")
	}
	// Check both links; Git registration alone is insufficient ownership evidence.
	back, err := safeio.Read(filepath.Join(repo.GitDir, "gitdir"))
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSuffix(string(back), "\n") != filepath.Join(path, ".git") {
		return nil, "", fmt.Errorf("Git worktree backpointer mismatch")
	}
	var owner manifest
	marker := filepath.Join(repo.GitDir, "agit-owner.json")
	err = safeio.ReadJSON(marker, &owner)
	if os.IsNotExist(err) && recoverCreation && w.Operation == "create" {
		// Durable token-bearing outer manifest + exact Git registration + base +
		// common directory + backpointer conclusively bind this interrupted add.
		if err := safeio.WriteJSON(marker, s.owner(w)); err != nil {
			return nil, "", err
		}
		owner = s.owner(w)
		err = nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("administrative ownership marker unavailable: %w", err)
	}
	if owner != s.owner(w) {
		return nil, "", fmt.Errorf("administrative ownership marker mismatch")
	}
	return repo, admin, nil
}
func (s *Service) transition(ctx context.Context, w *Workspace, state State, diagnostic string) error {
	old := w.State
	next := *w
	next.State = state
	next.UpdatedAt = time.Now().UTC()
	next.LastError = diagnostic
	if len(next.LastError) > 4096 {
		next.LastError = next.LastError[:4096]
	}
	if state == Removed {
		next.RemovedAt = next.UpdatedAt
	}
	if err := s.Store.UpdateWorkspace(ctx, next, old); err != nil {
		return err
	}
	*w = next
	return nil
}

type resourceError struct{ cause error }

func (e *resourceError) Error() string { return e.cause.Error() }
func (e *resourceError) Unwrap() error { return e.cause }
func (s *Service) fail(ctx context.Context, w *Workspace, cause error) error {
	if w.State == Removed {
		return cause
	}
	if err := s.transition(ctx, w, Error, cause.Error()); err != nil {
		return errors.Join(cause, fmt.Errorf("could not persist error; durable operation intent remains: %w", err))
	}
	return &resourceError{cause: cause}
}
func (s *Service) Create(ctx context.Context, txID, name string) (Workspace, error) {
	if err := ValidateName(name); err != nil {
		return Workspace{}, err
	}
	tx, err := s.SelectTransaction(ctx, txID, true)
	if err != nil {
		return Workspace{}, err
	}
	existing, err := s.List(ctx, tx.ID)
	if err != nil {
		return Workspace{}, err
	}
	for _, w := range existing {
		if w.Name == name {
			return Workspace{}, fmt.Errorf("workspace name %q already exists (ID %s, state %s)", name, w.ID, w.State)
		}
	}
	if err := s.Repository.VerifyCommit(ctx, tx.HeadCommit); err != nil {
		return Workspace{}, err
	}
	if err := s.Repository.CheckCheckoutSafety(ctx); err != nil {
		return Workspace{}, err
	}
	if err := s.ensureRoot(); err != nil {
		return Workspace{}, err
	}
	id, err := NewID()
	if err != nil {
		return Workspace{}, err
	}
	token, err := NewID()
	if err != nil {
		return Workspace{}, err
	}
	now := time.Now().UTC()
	w := Workspace{ID: id, TransactionID: tx.ID, Name: name, Branch: "agit/" + tx.ID + "/" + id + "-" + name, Path: filepath.ToSlash(filepath.Join("workspaces", id, "tree")), BaseCommit: tx.HeadCommit, State: Creating, CreatedAt: now, UpdatedAt: now, OperationToken: token, Operation: "create"}
	if err := s.Repository.BranchAvailable(ctx, w.Branch); err != nil {
		return Workspace{}, err
	}
	path := filepath.Join(s.MetadataDir, filepath.FromSlash(w.Path))
	anchor, err := git.Discover(ctx, filepath.Dir(s.MetadataDir))
	if err != nil {
		return Workspace{}, err
	}
	if anchor.CommonDir != s.Repository.CommonDir {
		return Workspace{}, fmt.Errorf("metadata anchor repository mismatch")
	}
	if err := anchor.CheckIgnored(ctx, path); err != nil {
		return Workspace{}, err
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		return Workspace{}, fmt.Errorf("workspace destination already exists or cannot be inspected")
	}
	if err := s.Store.InsertWorkspace(ctx, w); err != nil {
		return Workspace{}, err
	}
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		failure := s.fail(ctx, &w, err)
		return w, failure
	}
	if err := safeio.SyncDir(filepath.Dir(filepath.Dir(path))); err != nil {
		failure := s.fail(ctx, &w, err)
		return w, failure
	}
	if err := safeio.WriteJSON(filepath.Join(filepath.Dir(path), "owner.json"), s.owner(w)); err != nil {
		failure := s.fail(ctx, &w, err)
		return w, failure
	}
	if err := s.Repository.AddWorktree(ctx, path, w.Branch, w.BaseCommit); err != nil {
		failure := s.fail(ctx, &w, err)
		return w, failure
	}
	_, admin, err := s.inspect(ctx, w, true)
	if err != nil {
		failure := s.fail(ctx, &w, err)
		return w, failure
	}
	w.AdminPath = admin
	if err := s.transition(ctx, &w, Ready, ""); err != nil {
		return w, fmt.Errorf("Git workspace created but READY persistence failed; inspect workspace to reconcile: %w", err)
	}
	return w, nil
}
func (s *Service) removalAbsent(ctx context.Context, w Workspace) (bool, error) {
	path, err := s.path(w)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	trees, err := s.Repository.Worktrees(ctx)
	if err != nil {
		return false, err
	}
	for _, tree := range trees {
		if filepath.Clean(tree.Path) == path {
			return false, fmt.Errorf("missing directory retains stale Git registration; manual intervention required")
		}
	}
	if w.AdminPath == "" {
		return false, fmt.Errorf("removal lacks administrative identity")
	}
	parts := strings.Split(w.AdminPath, "/")
	if len(parts) != 2 || parts[0] != "worktrees" || parts[1] == ".." || parts[1] == "" {
		return false, fmt.Errorf("invalid removal administrative identity")
	}
	if err := safeio.Directory(filepath.Join(s.Repository.CommonDir, "worktrees")); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	if _, err := os.Lstat(filepath.Join(s.Repository.CommonDir, filepath.FromSlash(w.AdminPath))); err == nil {
		return false, fmt.Errorf("Git administrative directory remains after removal")
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}
func (s *Service) reconcile(ctx context.Context, w *Workspace) error {
	if w.State == Removed {
		return nil
	}
	if w.Operation == "remove" && (w.State == Removing || w.State == Error) {
		absent, err := s.removalAbsent(ctx, *w)
		if err != nil {
			return s.fail(ctx, w, err)
		}
		if absent {
			return s.transition(ctx, w, Removed, "")
		}
		// Reads never resume a destructive operation. Verify ownership and report pending intent.
		if _, _, err := s.inspect(ctx, *w, false); err != nil {
			return s.fail(ctx, w, err)
		}
		return nil
	}
	pending := w.State == Creating || (w.State == Error && w.Operation == "create" && w.AdminPath == "")
	_, admin, err := s.inspect(ctx, *w, pending)
	if err != nil {
		return s.fail(ctx, w, err)
	}
	if w.State == Creating || w.State == Error {
		w.AdminPath = admin
		return s.transition(ctx, w, Ready, "")
	}
	return nil
}
func (s *Service) List(ctx context.Context, txID string) ([]Workspace, error) {
	tx, err := s.SelectTransaction(ctx, txID, false)
	if err != nil {
		return nil, err
	}
	result, err := s.Store.ListWorkspaces(ctx, tx.ID)
	if err != nil {
		return nil, err
	}
	for i := range result {
		if err := s.reconcile(ctx, &result[i]); err != nil {
			// Persisted ERROR is an observable record; a persistence/cancellation failure must propagate.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var resource *resourceError
			if !errors.As(err, &resource) {
				return nil, err
			}
		}
	}
	return result, nil
}
func (s *Service) Status(ctx context.Context, txID, name string) (Workspace, error) {
	if err := ValidateName(name); err != nil {
		return Workspace{}, err
	}
	all, err := s.List(ctx, txID)
	if err != nil {
		return Workspace{}, err
	}
	for _, w := range all {
		if w.Name == name {
			if w.State == Error {
				return w, fmt.Errorf("workspace requires manual inspection: %s", w.LastError)
			}
			return w, nil
		}
	}
	return Workspace{}, fmt.Errorf("workspace %q not found", name)
}
func (s *Service) Remove(ctx context.Context, txID, name string) (Workspace, error) {
	if err := ValidateName(name); err != nil {
		return Workspace{}, err
	}
	all, err := s.List(ctx, txID)
	if err != nil {
		return Workspace{}, err
	}
	var w Workspace
	found := false
	for _, candidate := range all {
		if candidate.Name == name {
			w = candidate
			found = true
			break
		}
	}
	if !found {
		return w, fmt.Errorf("workspace %q not found", name)
	}
	if w.State == Removed {
		return w, nil
	}
	if w.State != Ready && w.State != Error && w.State != Removing {
		return w, fmt.Errorf("workspace state %s cannot be removed", w.State)
	}
	preflight := func() error {
		repo, admin, err := s.inspect(ctx, w, false)
		if err != nil {
			return err
		}
		w.AdminPath = admin
		if repo.Root == s.Repository.Root {
			return fmt.Errorf("cannot remove the invoking worktree; run from another repository worktree")
		}
		if repo.Root == filepath.Dir(s.MetadataDir) {
			return fmt.Errorf("cannot remove the metadata anchor")
		}
		trees, err := s.Repository.Worktrees(ctx)
		if err != nil {
			return err
		}
		for i, tree := range trees {
			if filepath.Clean(tree.Path) == repo.Root && (i == 0 || tree.Locked || tree.Prunable) {
				return fmt.Errorf("main, locked, or prunable worktree cannot be removed")
			}
		}
		return repo.CleanForRemoval(ctx)
	}
	if err := preflight(); err != nil {
		return w, err
	}
	if w.State != Removing {
		w.Operation = "remove"
		if err := s.transition(ctx, &w, Removing, ""); err != nil {
			return w, err
		}
	}
	if err := preflight(); err != nil {
		failure := s.fail(ctx, &w, err)
		return w, failure
	}
	path, err := s.path(w)
	if err != nil {
		failure := s.fail(ctx, &w, err)
		return w, failure
	}
	if err := s.Repository.RemoveWorktree(ctx, path); err != nil {
		failure := s.fail(ctx, &w, err)
		return w, failure
	}
	absent, err := s.removalAbsent(ctx, w)
	if err != nil {
		failure := s.fail(ctx, &w, err)
		return w, failure
	}
	if !absent {
		failure := s.fail(ctx, &w, fmt.Errorf("worktree removal could not be verified"))
		return w, failure
	}
	if err := s.transition(ctx, &w, Removed, ""); err != nil {
		return w, fmt.Errorf("Git worktree removed but final persistence failed; status will reconcile: %w", err)
	}
	return w, nil
}
