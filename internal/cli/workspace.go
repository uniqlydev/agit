package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/uniqlydev/agit/internal/workspace"
)

func newWorkspaceCommand() *cobra.Command {
	root := &cobra.Command{Use: "workspace", Short: "Manage isolated transaction Git worktrees"}
	var txID string
	root.PersistentFlags().StringVar(&txID, "tx", "", "Explicit transaction ID (required when active selection is ambiguous)")
	for _, action := range []string{"create", "list", "status", "remove"} {
		command := &cobra.Command{Use: action, Args: cobra.NoArgs}
		if action != "list" {
			command.Use += " <name>"
			command.Args = cobra.ExactArgs(1)
		}
		command.Short = map[string]string{"create": "Create an isolated workspace", "list": "List workspace lifecycle records", "status": "Inspect a workspace", "remove": "Remove an owned clean worktree; retain its branch"}[action]
		command.RunE = func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			repo, err := repository(ctx)
			if err != nil {
				return err
			}
			session, err := openStore(ctx, repo)
			if err != nil {
				return err
			}
			defer session.Close()
			service := workspace.Service{Store: session.Store, Repository: repo, MetadataDir: session.MetadataDir, RepositoryID: session.RepositoryID}
			printWorkspace := func(w workspace.Workspace) {
				fmt.Fprintf(cmd.OutOrStdout(), "Workspace: %s\nID: %s\nTransaction: %s\nState: %s\nBranch: %s\nPath: %s\nBase: %s\n", w.Name, w.ID, w.TransactionID, w.State, w.Branch, filepath.Join(session.MetadataDir, filepath.FromSlash(w.Path)), w.BaseCommit)
				if w.LastError != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Diagnostic: %q\n", w.LastError)
				}
			}
			switch action {
			case "list":
				all, err := service.List(ctx, txID)
				if err != nil {
					return err
				}
				if len(all) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No workspaces.")
				}
				for _, w := range all {
					printWorkspace(w)
				}
				return nil
			case "create":
				w, err := service.Create(ctx, txID, args[0])
				if w.ID != "" {
					printWorkspace(w)
				}
				return err
			case "status":
				w, err := service.Status(ctx, txID, args[0])
				if w.ID != "" {
					printWorkspace(w)
				}
				return err
			case "remove":
				w, err := service.Remove(ctx, txID, args[0])
				if w.ID != "" {
					printWorkspace(w)
				}
				return err
			}
			return nil
		}
		root.AddCommand(command)
	}
	return root
}
