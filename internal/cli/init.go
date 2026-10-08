package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/uniqlydev/agit/internal/project"
)

func newInitCommand() *cobra.Command {
	return &cobra.Command{Use: "init", Short: "Initialize AGIT in an existing Git repository", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		repo, err := repository(cmd.Context())
		if err != nil {
			return err
		}
		if err := project.Initialize(cmd.Context(), repo.Root); err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Initialized AGIT in %s\n", filepath.Join(repo.Root, ".agit"))
		return err
	}}
}
