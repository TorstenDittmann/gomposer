package cli

import (
	"fmt"
	"math"
	"strings"

	"github.com/spf13/cobra"

	"github.com/torstendittmann/gomposer/internal/cache"
)

func newCacheCmd() *cobra.Command {
	cacheCmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect the gomposer cache",
		Long:  "Prints the cache location and per-layer disk usage. Use `cache dir` for the raw path and `cache clear` to delete layers.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCacheInfo(cmd)
		},
	}
	cacheCmd.AddCommand(newCacheDirCmd())
	cacheCmd.AddCommand(newCacheClearCmd())
	return cacheCmd
}

func runCacheInfo(cmd *cobra.Command) error {
	if flagQuiet {
		return nil
	}
	root, err := cache.Root()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, root)
	var total int64
	for _, l := range cache.Layers() {
		size, err := l.Size()
		if err != nil {
			return err
		}
		total += size
		fmt.Fprintf(out, "  %-11s %9s\n", l.Name, humanBytes(size))
	}
	fmt.Fprintf(out, "  %-11s %9s\n", "total", humanBytes(total))
	return nil
}

func newCacheDirCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dir",
		Short: "Print the cache directory path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := cache.Root()
			if err != nil {
				return err
			}
			// The path IS the output, not decoration — print it even
			// under --quiet so `du -sh $(gomposer cache dir)` composes.
			fmt.Fprintln(cmd.OutOrStdout(), root)
			return nil
		},
	}
}

func newCacheClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear [layer...]",
		Short: "Clear cache layers (all layers when none are named)",
		RunE: func(cmd *cobra.Command, args []string) error {
			layers, err := resolveLayerArgs(args)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var total int64
			for _, l := range layers {
				freed, err := l.Clear()
				if err != nil {
					return err
				}
				total += freed
				if !flagQuiet {
					fmt.Fprintf(out, "cleared %s (%s)\n", l.Name, humanBytes(freed))
				}
			}
			if !flagQuiet && len(layers) > 1 {
				fmt.Fprintf(out, "freed %s\n", humanBytes(total))
			}
			return nil
		},
	}
}

// resolveLayerArgs maps CLI layer names to registry layers. No args →
// every layer. Unknown names fail before anything is cleared;
// duplicates collapse, preserving first-mention order.
func resolveLayerArgs(args []string) ([]cache.Layer, error) {
	if len(args) == 0 {
		return cache.Layers(), nil
	}
	seen := make(map[string]bool, len(args))
	layers := make([]cache.Layer, 0, len(args))
	for _, name := range args {
		l, ok := cache.LayerByName(name)
		if !ok {
			valid := make([]string, 0, len(cache.Layers()))
			for _, v := range cache.Layers() {
				valid = append(valid, v.Name)
			}
			return nil, fmt.Errorf("unknown cache layer %q (valid: %s)", name, strings.Join(valid, ", "))
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		layers = append(layers, l)
	}
	return layers, nil
}

// humanBytes renders n with decimal units (1000-based) and one decimal
// place: 999 B, 1.0 kB, 142.3 MB, 3.1 GB. GB is the cap — larger
// values render as (possibly >1000) GB.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 2; m /= unit {
		div *= unit
		exp++
	}
	mantissa := float64(n) / float64(div)
	// %.1f rounds; if that rounding would display 1000.0 (e.g. 999_950
	// kB rounds to "1000.0 kB"), bump to the next unit so it reads
	// "1.0 MB" instead. GB is still the cap (exp < 2 guards that).
	if exp < 2 && math.Round(mantissa*10) >= 10000 {
		div *= unit
		exp++
		mantissa = float64(n) / float64(div)
	}
	return fmt.Sprintf("%.1f %cB", mantissa, "kMG"[exp])
}
