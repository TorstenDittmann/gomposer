package manifest

import "fmt"

// IsGit reports whether r is a VCS repository that this stage's VCS source
// can handle. Only "vcs" and "git" types map to git.
func (r Repository) IsGit() bool {
	switch r.Type {
	case "vcs", "git":
		return true
	}
	return false
}

// Validate returns nil for repository entries the resolver can use, or a
// human-readable error for unsupported types. The orchestrator calls this
// once per repository at startup.
func (r Repository) Validate() error {
	if r.URL == "" {
		return fmt.Errorf("manifest: repository missing `url`")
	}
	switch r.Type {
	case "vcs", "git":
		return nil
	case "composer":
		return fmt.Errorf("manifest: `composer` repositories are not supported in stage 2 (CG204): %s", r.URL)
	case "path":
		return fmt.Errorf("manifest: `path` repositories are not supported (CG205): %s", r.URL)
	case "package":
		return fmt.Errorf("manifest: inline `package` repositories are not supported (CG206)")
	case "artifact":
		return fmt.Errorf("manifest: `artifact` repositories are not supported (CG207)")
	case "":
		return fmt.Errorf("manifest: repository missing `type`: %s", r.URL)
	default:
		return fmt.Errorf("manifest: unknown repository type %q (CG208)", r.Type)
	}
}
