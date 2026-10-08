package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/uniqlydev/agit/internal/git"
	"github.com/uniqlydev/agit/internal/safeio"
	"github.com/uniqlydev/agit/internal/storage"
)

func Initialize(ctx context.Context, root string) error {
	repo, err := git.Discover(ctx, root)
	if err != nil {
		return err
	}
	release, err := acquire(ctx, repo)
	if err != nil {
		return err
	}
	defer release()
	dirs, err := candidates(ctx, repo)
	if err != nil {
		return err
	}
	if len(dirs) != 0 {
		return fmt.Errorf("AGIT metadata already exists at %s; refusing reinitialization", dirs[0])
	}
	// A stale locator is not permission to create a new database.
	control := filepath.Join(repo.CommonDir, "agit-control")
	if _, err := os.Lstat(control); err == nil {
		if err := validateControl(control); err != nil {
			return err
		}
		if _, err := os.Lstat(filepath.Join(control, "metadata.json")); err == nil {
			return fmt.Errorf("shared metadata locator exists; refusing reinitialization")
		} else if !os.IsNotExist(err) {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := initializeLegacy(ctx, repo.Root); err != nil {
		return err
	}
	session, err := adopt(ctx, repo, filepath.Join(repo.Root, ".agit"), nil)
	if err != nil {
		return fmt.Errorf("metadata created but adoption incomplete; preserve .agit and retry status: %w", err)
	}
	return session.Store.Close()
}

func initializeLegacy(ctx context.Context, root string) error {
	dir := filepath.Join(root, ".agit")
	if err := os.Mkdir(dir, 0700); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("AGIT path already exists at %s; refusing reinitialization", dir)
		}
		return err
	}
	// Only remove files created by this operation if initialization fails.
	success := false
	defer func() {
		if !success {
			os.Remove(filepath.Join(dir, storage.DatabaseName))
			os.Remove(dir)
		}
	}()
	store, err := storage.Initialize(ctx, filepath.Join(dir, storage.DatabaseName))
	if err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	if err := ignoreAGIT(root); err != nil {
		return err
	}
	success = true
	return nil
}
func ignoreAGIT(root string) error {
	path := filepath.Join(root, ".gitignore")
	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to modify non-regular .gitignore")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == ".agit/" || line == "/.agit/" {
			return nil
		}
	}
	suffix := "/.agit/\n"
	if len(data) > 0 && data[len(data)-1] != '\n' {
		suffix = "\n" + suffix
	}
	f, err := safeio.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(suffix)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
