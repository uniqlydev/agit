package cli

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"github.com/uniqlydev/agit/internal/git"
	"github.com/uniqlydev/agit/internal/project"
)

func NewRootCommand(version string) *cobra.Command {
	root := &cobra.Command{Use: "agit", Short: "Agent-native, Git-compatible development", Version: version, SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(newInitCommand(), newStatusCommand(), newTransactionCommand())
	return root
}
func repository(ctx context.Context) (*git.Repository, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return git.Discover(ctx, cwd)
}
func openStore(ctx context.Context, repo *git.Repository) (*project.Session, error) {
	return project.Open(ctx, repo)
}
