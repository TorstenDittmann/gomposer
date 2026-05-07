package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Resolve dependencies from the lockfile and materialize vendor/",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("install: not implemented yet")
		},
	}
}
