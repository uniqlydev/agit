//go:build !darwin && !linux

package project

import (
	"context"
	"fmt"
	"github.com/uniqlydev/agit/internal/git"
)

func acquire(ctx context.Context, repo *git.Repository) (func() error, error) {
	return nil, fmt.Errorf("AGIT M1 requires macOS or Linux local filesystems")
}
