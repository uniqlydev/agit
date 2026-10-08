//go:build darwin || linux

package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/uniqlydev/agit/internal/git"
	"github.com/uniqlydev/agit/internal/safeio"
	"golang.org/x/sys/unix"
)

const lockFormat = "AGIT repository lock v1\n"

func acquire(ctx context.Context, repo *git.Repository) (func() error, error) {
	path := filepath.Join(repo.CommonDir, "agit-control.lock")
	if err := safeio.WriteExclusive(path, []byte(lockFormat)); err != nil && !os.IsExist(err) {
		return nil, err
	}
	f, err := safeio.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	acquired := false
	defer func() {
		if !acquired {
			f.Close()
		}
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("repository is busy; retry after the other AGIT operation finishes")
		case <-ticker.C:
		}
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, current) {
		return nil, fmt.Errorf("repository lock identity changed")
	}
	data, err := safeio.Read(path)
	if err != nil {
		return nil, err
	}
	if string(data) != lockFormat {
		return nil, fmt.Errorf("unrecognized existing repository lock; manual inspection required")
	}
	acquired = true
	return func() error { unlock := unix.Flock(int(f.Fd()), unix.LOCK_UN); return errors.Join(unlock, f.Close()) }, nil
}
