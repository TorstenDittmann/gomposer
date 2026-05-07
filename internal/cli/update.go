package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Re-resolve dependencies and rewrite the lockfile",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("update: not implemented yet")
		},
	}
}
