package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/torstendittmann/composer-go/internal/manifest"
)

func newInstallCmd() *cobra.Command {
	var projectDir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Resolve dependencies from the lockfile and materialize vendor/",
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectDir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				projectDir = wd
			}
			path := filepath.Join(projectDir, "composer.json")
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("install: %w", err)
			}
			m, err := manifest.Parse(data)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "manifest %s with %d direct requires\n", m.Name, len(m.Require))
			return nil
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory containing composer.json (defaults to cwd)")
	return cmd
}
