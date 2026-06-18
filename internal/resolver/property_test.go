package resolver

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/torstendittmann/gomposer/internal/constraint"
	"github.com/torstendittmann/gomposer/internal/manifest"
	"github.com/torstendittmann/gomposer/internal/registry"
	"github.com/torstendittmann/gomposer/internal/resolver/testlookup"
)

func TestPropertyRandomSatisfiableGraphs(t *testing.T) {
	for seed := int64(1); seed <= 25; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(seed))
			pkgs := genGraph(r)
			src := testlookup.New(pkgs)

			rootReqs := map[string]string{}
			// Take 1-3 random packages as direct requires.
			names := keysOf(pkgs)
			r.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
			n := 1 + r.Intn(3)
			if n > len(names) {
				n = len(names)
			}
			for _, name := range names[:n] {
				// Use "*" for liberal satisfiability.
				rootReqs[name] = "*"
			}
			m := &manifest.Manifest{Name: "u/a", Require: rootReqs}

			res, err := Solve(context.Background(), Input{Manifest: m, Source: src})
			if err != nil {
				t.Fatalf("Solve: %v (manifest=%v)", err, rootReqs)
			}

			// Verify every chosen version's transitive requires are satisfied
			// by another chosen version.
			chosen := map[string]constraint.Version{}
			for _, p := range res.Packages {
				chosen[p.Name] = p.Version
			}
			for _, p := range res.Packages {
				for depName, depRaw := range p.Record.Require {
					if isPlatformPackage(depName) {
						continue
					}
					depV, ok := chosen[depName]
					if !ok {
						t.Errorf("%s requires %s but %s not chosen", p.Name, depName, depName)
						continue
					}
					c, err := constraint.Parse(depRaw)
					if err != nil {
						continue
					}
					if !c.Satisfies(depV) {
						t.Errorf("%s -> %s: chosen %s does not satisfy %s",
							p.Name, depName, depV.Original, depRaw)
					}
				}
			}

			// Determinism.
			res2, err := Solve(context.Background(), Input{Manifest: m, Source: src})
			if err != nil {
				t.Fatalf("Solve(2): %v", err)
			}
			if len(res.Packages) != len(res2.Packages) {
				t.Fatalf("non-deterministic length")
			}
			for i := range res.Packages {
				if res.Packages[i].Name != res2.Packages[i].Name ||
					!res.Packages[i].Version.Equal(res2.Packages[i].Version) {
					t.Errorf("non-deterministic at %d", i)
				}
			}
		})
	}
}

// genGraph builds a small package universe with random deps. Versions are
// always picked so the universe is satisfiable: each dep's constraint is
// "*", which is always satisfied as long as the depended-on package has at
// least one version.
func genGraph(r *rand.Rand) map[string][]registry.PackageVersion {
	count := 5 + r.Intn(11)
	names := make([]string, count)
	for i := range names {
		names[i] = fmt.Sprintf("p%02d/x", i)
	}
	out := map[string][]registry.PackageVersion{}
	for i, n := range names {
		nVers := 1 + r.Intn(3)
		var vs []registry.PackageVersion
		for v := 0; v < nVers; v++ {
			req := map[string]string{}
			// Each version may depend on 0..2 strictly-later-indexed packages.
			if i+1 < len(names) {
				maxDeps := 2
				if remaining := len(names) - i - 1; remaining < maxDeps {
					maxDeps = remaining
				}
				if maxDeps > 0 {
					for d := 0; d < r.Intn(maxDeps+1); d++ {
						pick := i + 1 + r.Intn(len(names)-i-1)
						req[names[pick]] = "*"
					}
				}
			}
			vs = append(vs, testlookup.Pkg(n, fmt.Sprintf("1.%d.0", v), req))
		}
		out[n] = vs
	}
	return out
}

func keysOf(m map[string][]registry.PackageVersion) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
