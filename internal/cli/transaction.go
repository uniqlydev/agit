package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/uniqlydev/agit/internal/transaction"
)

func newTransactionCommand() *cobra.Command {
	tx := &cobra.Command{Use: "tx", Short: "Manage development transactions"}
	tx.AddCommand(&cobra.Command{Use: "begin <objective>", Short: "Begin a transaction", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := transaction.ValidateObjective(args[0]); err != nil {
			return err
		}
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
		head, err := repo.Head(ctx)
		if err != nil {
			return err
		}
		created, err := (transaction.Service{Store: store}).Begin(ctx, args[0], branch, head)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Transaction: %s\nState: %s\nObjective: %q\n", created.ID, created.State, created.Objective)
		return err
	}})
	tx.AddCommand(&cobra.Command{Use: "status", Short: "List transactions and their states", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
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
		transactions, err := store.List(ctx, false)
		if err != nil {
			return err
		}
		if len(transactions) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No transactions.")
		}
		for _, t := range transactions {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %q\n", t.ID, t.State, t.Objective)
		}
		return nil
	}})
	tx.AddCommand(newWorkspaceCommand())
	return tx
}
