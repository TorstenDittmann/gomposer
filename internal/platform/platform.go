// Package platform exposes a structured snapshot of the PHP runtime that
// will execute the user's project.
//
// The snapshot is captured by Probe(), which shells out to `php -r` once
// per process and parses the resulting JSON. Subsequent calls return the
// cached result — PHP versions don't change while a single gomposer
// run is in progress.
//
// Cross-process: NOT cached on disk. PHP versions can change with OS
// upgrades, brew installs, or Docker image swaps; probing is cheap enough
// (~30ms cold) that re-probing per run is the correct trade-off.
//
// The string Fingerprint(), inherited from stage 1, still flows into every
// cache key. Stage 2's fingerprint shape is "php-<version>;ext-foo;ext-bar"
// — different from stage 1's "php-unknown" — so all stage-1 cache entries
// naturally invalidate on the upgrade.
package platform

import (
	"sort"
	"strings"
	"sync"

	"github.com/torstendittmann/gomposer/internal/constraint"
)

// Platform is a structured snapshot of the runtime PHP.
type Platform struct {
	// PHPVersion is the parsed PHP_VERSION (e.g. 8.2.14).
	PHPVersion constraint.Version
	// Extensions maps extension name (without the "ext-" prefix) to its
	// reported version. Many extensions report an empty string; in that
	// case the Version is the zero value and callers should treat it as
	// "any version present" — a constraint of `*` is satisfied, but a
	// concrete version constraint like `^7.4` is not.
	Extensions map[string]constraint.Version
}

// Fingerprint returns the canonical string fingerprint for this Platform,
// suitable as part of a cache key. Format:
//
//	php-<version>;ext-name1;ext-name2[@<version>];...
//
// Extension names are sorted to keep the string deterministic. Versions
// are appended only when known (non-empty), since adding "@<empty>" would
// be noise.
func (p *Platform) Fingerprint() string {
	var sb strings.Builder
	sb.WriteString("php-")
	if p == nil || p.PHPVersion.Original == "" {
		sb.WriteString("unknown")
		return sb.String()
	}
	sb.WriteString(p.PHPVersion.Original)
	names := make([]string, 0, len(p.Extensions))
	for n := range p.Extensions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sb.WriteString(";ext-")
		sb.WriteString(n)
		if v := p.Extensions[n]; v.Original != "" {
			sb.WriteString("@")
			sb.WriteString(v.Original)
		}
	}
	return sb.String()
}

// HasExtension reports whether `ext-<name>` is loaded. Names should NOT
// include the `ext-` prefix.
func (p *Platform) HasExtension(name string) bool {
	if p == nil || p.Extensions == nil {
		return false
	}
	_, ok := p.Extensions[name]
	return ok
}

// ExtensionVersion returns the loaded version of an extension and whether
// it is present. The returned version may be the zero Version (for
// extensions that don't report a version string).
func (p *Platform) ExtensionVersion(name string) (constraint.Version, bool) {
	if p == nil {
		return constraint.Version{}, false
	}
	v, ok := p.Extensions[name]
	return v, ok
}

// --- process-level cache ---

var (
	probeOnce   sync.Once
	probeResult *Platform
	probeErr    error
)

// Probe returns the runtime Platform. The first call shells out to PHP;
// subsequent calls return the cached result.
func Probe() (*Platform, error) {
	probeOnce.Do(func() {
		probeResult, probeErr = runProbe()
	})
	return probeResult, probeErr
}

// resetProbeCacheForTests is exposed for testing. It is a no-op outside
// tests; production callers MUST NOT call this.
func resetProbeCacheForTests() {
	probeOnce = sync.Once{}
	probeResult = nil
	probeErr = nil
}

// Fingerprint preserves the stage-1 entry point. Production callers prefer
// Probe(); this exists so existing cache-key code keeps compiling.
func Fingerprint() (string, error) {
	p, err := Probe()
	if err != nil {
		return "", err
	}
	return p.Fingerprint(), nil
}

// SetTestPlatform installs a fake Platform for the lifetime of a test.
// It pre-populates the process-level Probe cache with a Platform whose
// PHP version is `phpVersion` and whose extensions are a small standard
// set (`json`, `mbstring`). The previous cache is restored on test
// cleanup.
//
// This is intentionally not a generic public API; it exists so tests that
// span multiple packages (orchestrator, resolver) can avoid shelling out
// to a real `php` binary.
func SetTestPlatform(t interface{ Cleanup(func()) }, phpVersion string) {
	v, err := constraint.ParseVersion(phpVersion)
	if err != nil {
		panic("SetTestPlatform: " + err.Error())
	}
	savedOnce := probeOnce
	savedResult := probeResult
	savedErr := probeErr
	probeOnce = sync.Once{}
	probeResult = &Platform{
		PHPVersion: v,
		Extensions: map[string]constraint.Version{"json": {}, "mbstring": {}},
	}
	probeErr = nil
	probeOnce.Do(func() {})
	t.Cleanup(func() {
		probeOnce = savedOnce
		probeResult = savedResult
		probeErr = savedErr
	})
}
