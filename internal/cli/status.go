package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newStatusCommand() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show repository and active transaction status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		repo, err := repository(ctx)
		if err != nil {
			return err
		}
		store, err := openStore(ctx, repo)
		if err != nil {
			return err
		}
		defer store.Close()
		branch, err := repo.Branch(ctx)
		if err != nil {
			return err
		}
		status, err := repo.WorkingTreeStatus(ctx)
		if err != nil {
			return err
		}
		transactions, err := store.List(ctx, true)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Repository: %s\nRoot: %s\nBranch: %s\nWorking tree:\n", filepath.Base(repo.Root), repo.Root, branch)
		if status == "" {
			fmt.Fprintln(out, "  clean")
		} else {
			fmt.Fprintln(out, status)
		}
		fmt.Fprintf(out, "Active transactions: %d\n", len(transactions))
		for _, tx := range transactions {
			fmt.Fprintf(out, "%s  %s  %q\n", tx.ID, tx.State, tx.Objective)
		}
		return nil
	}}
}
