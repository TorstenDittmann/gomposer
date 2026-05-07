package auth

import (
	"net/http"
	"strings"
)

// Kind tags the credential variant. Each kind corresponds to a section
// in auth.json.
type Kind int

const (
	KindNone Kind = iota
	KindHTTPBasic
	KindBearer
	KindGitHubOAuth
	KindGitLabToken
	KindGitLabOAuth
)

func (k Kind) String() string {
	switch k {
	case KindHTTPBasic:
		return "http-basic"
	case KindBearer:
		return "bearer"
	case KindGitHubOAuth:
		return "github-oauth"
	case KindGitLabToken:
		return "gitlab-token"
	case KindGitLabOAuth:
		return "gitlab-oauth"
	}
	return "none"
}

// Credentials carries one resolved credential. Only the fields appropriate
// for the Kind are populated; the rest are zero.
type Credentials struct {
	Kind     Kind
	Host     string
	Username string // http-basic, gitlab-token
	Password string // http-basic
	Token    string // bearer, github-oauth, gitlab-token, gitlab-oauth
}

// Empty reports whether c carries no usable credential.
func (c Credentials) Empty() bool { return c.Kind == KindNone }

// Apply attaches c to req as an Authorization header. No-op on Empty.
//
// Mapping:
//   - http-basic     -> req.SetBasicAuth(Username, Password)
//   - bearer         -> Authorization: Bearer <Token>
//   - github-oauth   -> Authorization: token <Token>          (GitHub convention)
//   - gitlab-token   -> Private-Token: <Token>                (GitLab convention)
//   - gitlab-oauth   -> Authorization: Bearer <Token>
func (c Credentials) Apply(req *http.Request) {
	if c.Empty() || req == nil {
		return
	}
	switch c.Kind {
	case KindHTTPBasic:
		req.SetBasicAuth(c.Username, c.Password)
	case KindBearer, KindGitLabOAuth:
		req.Header.Set("Authorization", "Bearer "+c.Token)
	case KindGitHubOAuth:
		req.Header.Set("Authorization", "token "+c.Token)
	case KindGitLabToken:
		req.Header.Set("Private-Token", c.Token)
	}
}

// normHost lowercases and strips a trailing port from h, so "GitHub.com:443"
// and "github.com" both match a single auth.json entry.
func normHost(h string) string {
	h = strings.ToLower(h)
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h
}

// Store is the merged credential index.
type Store struct {
	merged file // post-merge view
}

// loadStore reads both files (either may be empty string to skip) and
// merges with user winning per-host-per-kind on collision.
func loadStore(composerPath, userPath string) (*Store, error) {
	composer := &file{}
	user := &file{}
	if composerPath != "" {
		f, err := loadOptional(composerPath)
		if err != nil {
			return nil, err
		}
		composer = f
	}
	if userPath != "" {
		f, err := loadOptional(userPath)
		if err != nil {
			return nil, err
		}
		user = f
	}
	return &Store{merged: mergeFiles(composer, user)}, nil
}

// Lookup returns the credential registered for host (case-insensitive,
// port-insensitive). When multiple kinds share a host, the priority is:
//
//	bearer > http-basic > github-oauth > gitlab-token > gitlab-oauth
//
// In practice users rarely register more than one kind per host. The order
// favours the most specific/scoped header form first.
func (s *Store) Lookup(host string) (Credentials, bool) {
	if s == nil {
		return Credentials{}, false
	}
	h := normHost(host)
	if t, ok := s.merged.Bearer[h]; ok && t != "" {
		return Credentials{Kind: KindBearer, Host: h, Token: t}, true
	}
	if b, ok := s.merged.HTTPBasic[h]; ok && (b.Username != "" || b.Password != "") {
		return Credentials{Kind: KindHTTPBasic, Host: h, Username: b.Username, Password: b.Password}, true
	}
	if t, ok := s.merged.GitHubOAuth[h]; ok && t != "" {
		return Credentials{Kind: KindGitHubOAuth, Host: h, Token: t}, true
	}
	if g, ok := s.merged.GitLabToken[h]; ok && g.Token != "" {
		return Credentials{Kind: KindGitLabToken, Host: h, Username: g.Username, Token: g.Token}, true
	}
	if t, ok := s.merged.GitLabOAuth[h]; ok && t != "" {
		return Credentials{Kind: KindGitLabOAuth, Host: h, Token: t}, true
	}
	return Credentials{}, false
}

// mergeFiles produces a new file where, for every map, user entries
// override composer entries on host collision. Hosts present in only one
// side are preserved as-is.
func mergeFiles(composer, user *file) file {
	out := file{
		HTTPBasic:   map[string]basicCred{},
		Bearer:      map[string]string{},
		GitHubOAuth: map[string]string{},
		GitLabToken: map[string]gitLabCred{},
		GitLabOAuth: map[string]string{},
	}
	for h, v := range composer.HTTPBasic {
		out.HTTPBasic[normHost(h)] = v
	}
	for h, v := range user.HTTPBasic {
		out.HTTPBasic[normHost(h)] = v
	}
	for h, v := range composer.Bearer {
		out.Bearer[normHost(h)] = v
	}
	for h, v := range user.Bearer {
		out.Bearer[normHost(h)] = v
	}
	for h, v := range composer.GitHubOAuth {
		out.GitHubOAuth[normHost(h)] = v
	}
	for h, v := range user.GitHubOAuth {
		out.GitHubOAuth[normHost(h)] = v
	}
	for h, v := range composer.GitLabToken {
		out.GitLabToken[normHost(h)] = v
	}
	for h, v := range user.GitLabToken {
		out.GitLabToken[normHost(h)] = v
	}
	for h, v := range composer.GitLabOAuth {
		out.GitLabOAuth[normHost(h)] = v
	}
	for h, v := range user.GitLabOAuth {
		out.GitLabOAuth[normHost(h)] = v
	}
	return out
}
