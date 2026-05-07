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
