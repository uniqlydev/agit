package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/uniqlydev/agit/internal/git"
	"github.com/uniqlydev/agit/internal/safeio"
	"github.com/uniqlydev/agit/internal/storage"
	"github.com/uniqlydev/agit/internal/workspace"
)

const controlFormat = "AGIT shared metadata control v1\n"

type Session struct {
	*storage.Store
	Repository                *git.Repository
	MetadataDir, RepositoryID string
	release                   func() error
}

func (s *Session) Close() error {
	err := s.Store.Close()
	if s.release != nil {
		err = errors.Join(err, s.release())
		s.release = nil
	}
	return err
}

type locator struct {
	Version                    int
	RepositoryID, MetadataPath string
}

func ensureControl(repo *git.Repository) (string, error) {
	dir := filepath.Join(repo.CommonDir, "agit-control")
	err := os.Mkdir(dir, 0700)
	if err != nil && !os.IsExist(err) {
		return "", err
	}
	if err == nil {
		if err := safeio.WriteExclusive(filepath.Join(dir, "format"), []byte(controlFormat)); err != nil {
			return "", err
		}
		if err := safeio.SyncDir(repo.CommonDir); err != nil {
			return "", err
		}
	}
	if err := validateControl(dir); err != nil {
		return "", err
	}
	return dir, nil
}
func validateControl(dir string) error {
	if err := safeio.Directory(dir); err != nil {
		return err
	}
	data, err := safeio.Read(filepath.Join(dir, "format"))
	if err != nil {
		return fmt.Errorf("unrecognized metadata control directory: %w", err)
	}
	if string(data) != controlFormat {
		return fmt.Errorf("unrecognized metadata control directory")
	}
	return nil
}

func candidates(ctx context.Context, repo *git.Repository) ([]string, error) {
	trees, err := repo.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	roots := []string{repo.Root}
	for _, tree := range trees {
		if !tree.Bare {
			roots = append(roots, tree.Path)
		}
	}
	seen := map[string]bool{}
	var dirs []string
	for _, root := range roots {
		canonical, err := filepath.EvalSymlinks(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		dir := filepath.Join(canonical, ".agit")
		if _, err := os.Lstat(dir); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := safeio.Directory(dir); err != nil {
			return nil, fmt.Errorf("conflicting metadata path %s: %w", dir, err)
		}
		if err := storage.Inspect(ctx, filepath.Join(dir, storage.DatabaseName)); err != nil {
			return nil, fmt.Errorf("invalid metadata candidate %s: %w", dir, err)
		}
		dirs = append(dirs, dir)
	}
	if len(dirs) > 1 {
		return nil, fmt.Errorf("conflicting AGIT metadata locations: %v; preserve all databases and resolve manually", dirs)
	}
	return dirs, nil
}
func resolve(ctx context.Context, repo *git.Repository) (string, *locator, error) {
	dirs, err := candidates(ctx, repo)
	if err != nil {
		return "", nil, err
	}
	control := filepath.Join(repo.CommonDir, "agit-control")
	var loc locator
	if _, err := os.Lstat(control); err == nil {
		if err := validateControl(control); err != nil {
			return "", nil, err
		}
		err = safeio.ReadJSON(filepath.Join(control, "metadata.json"), &loc)
		if err == nil {
			if loc.Version != 1 || !workspace.ValidID(loc.RepositoryID) || filepath.IsAbs(loc.MetadataPath) {
				return "", nil, fmt.Errorf("invalid shared metadata locator")
			}
			expected := filepath.Clean(filepath.Join(repo.CommonDir, loc.MetadataPath))
			if len(dirs) != 1 || dirs[0] != expected {
				return "", nil, fmt.Errorf("shared metadata locator is stale or conflicts with repository worktrees; manual inspection required")
			}
			return expected, &loc, nil
		}
		if !os.IsNotExist(err) {
			return "", nil, err
		}
	} else if !os.IsNotExist(err) {
		return "", nil, err
	}
	if len(dirs) == 0 {
		return "", nil, fmt.Errorf("AGIT is not initialized; run agit init")
	}
	return dirs[0], nil, nil
}
func adopt(ctx context.Context, repo *git.Repository, dir string, loc *locator) (*Session, error) {
	store, err := storage.Open(ctx, filepath.Join(dir, storage.DatabaseName))
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			store.Close()
		}
	}()
	// A locator must never adopt or migrate a different legacy database.
	if loc != nil {
		id, err := store.RepositoryID(ctx)
		if err != nil || id != loc.RepositoryID {
			return nil, fmt.Errorf("repository identity does not match shared locator")
		}
	} else {
		if err := store.Upgrade(ctx); err != nil {
			return nil, err
		}
	}
	id, err := store.RepositoryID(ctx)
	if err != nil {
		return nil, err
	}
	if loc == nil {
		control, err := ensureControl(repo)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(repo.CommonDir, dir)
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(locator{Version: 1, RepositoryID: id, MetadataPath: relative})
		if err != nil {
			return nil, err
		}
		if err := safeio.WriteExclusive(filepath.Join(control, "metadata.json"), data); err != nil {
			return nil, err
		}
	}
	success = true
	return &Session{Store: store, Repository: repo, MetadataDir: dir, RepositoryID: id}, nil
}
func Open(ctx context.Context, repo *git.Repository) (*Session, error) {
	release, err := acquire(ctx, repo)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			release()
		}
	}()
	dir, loc, err := resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	session, err := adopt(ctx, repo, dir, loc)
	if err != nil {
		return nil, err
	}
	session.release = release
	success = true
	return session, nil
}
