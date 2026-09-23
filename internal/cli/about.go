package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

func newAboutCmd() *cobra.Command {
	var (
		projectDir string
		checkPHP   bool
	)
	cmd := &cobra.Command{
		Use:   "about",
		Short: "Print gomposer version and environment details",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAbout(cmd, projectDir, checkPHP)
		},
	}
	cmd.Flags().StringVar(&projectDir, "project", "", "project directory to inspect (defaults to cwd)")
	cmd.Flags().BoolVar(&checkPHP, "check-php", false, "also probe the local php executable")
	return cmd
}

func runAbout(cmd *cobra.Command, projectDir string, checkPHP bool) error {
	if flagQuiet {
		return nil
	}
	out := cmd.OutOrStdout()
	version := cmd.Root().Version
	fmt.Fprintf(out, "gomposer %s\n", version)

	dir := projectDir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir = wd
	}
	resolved, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "project: %s\n", resolved)

	composerPath := filepath.Join(resolved, "composer.json")
	f, err := os.Open(composerPath)
	if err != nil {
		fmt.Fprintf(out, "composer.json: (missing)\n")
	} else {
		defer f.Close()
		buf := make([]byte, 64)
		n, _ := f.Read(buf)
		fmt.Fprintf(out, "composer.json: present (%d bytes peeked)\n", n)
	}

	if checkPHP {
		phpInfo, err := probePHPVersions()
		if err != nil {
			return fmt.Errorf("about: php probe failed: %w", err)
		}
		for name, ver := range phpInfo {
			fmt.Fprintf(out, "php[%s]: %s\n", name, ver)
		}
	}
	return nil
}

// probePHPVersions runs `php -v` for each candidate binary name concurrently
// and returns a name→version map.
func probePHPVersions() (map[string]string, error) {
	candidates := []string{"php", "php8.2", "php8.3"}
	results := make(map[string]string)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, name := range candidates {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			out, err := exec.Command(name, "-v").Output()
			if err != nil {
				return
			}
			line := strings.SplitN(string(out), "\n", 2)[0]
			mu.Lock()
			results[name] = line
			mu.Unlock()
		}(name)
	}
	wg.Wait()
	if len(results) == 0 {
		return nil, fmt.Errorf("no php binary found")
	}
	return results, nil
}
