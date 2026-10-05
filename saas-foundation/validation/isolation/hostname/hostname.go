// Package hostname resolves a request host to an Organization selector.
//
// Resolution is selector-only: a host can name at most one Organization,
// and naming an Organization grants nothing. Authority always comes from
// the policy resolver (membership/grants) afterwards. The base domain is
// configuration (PLATFORM_BASE_DOMAIN), never a literal in code.
package hostname

import (
	"errors"
	"net"
	"regexp"
	"strings"
)

// BaseDomainEnv is the configuration key for the platform base domain.
const BaseDomainEnv = "PLATFORM_BASE_DOMAIN"

// slugPattern matches an Organization slug (one DNS label, kebab-case).
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// labelPattern matches one normalized DNS label.
var labelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ErrInvalidHost is returned for hosts that cannot be normalized.
var ErrInvalidHost = errors.New("hostname: invalid host")

// Directory maps an Organization slug to its ID.
type Directory interface {
	OrganizationIDBySlug(slug string) (string, bool)
}

// CustomDomain is an Organization-owned host. Only verified domains resolve.
type CustomDomain struct {
	Host           string
	OrganizationID string
	Verified       bool
}

// Config configures a Resolver.
type Config struct {
	BaseDomain    string // PLATFORM_BASE_DOMAIN
	Directory     Directory
	CustomDomains []CustomDomain
}

// Selection is the outcome of a successful resolution: a selector, not a
// grant.
type Selection struct {
	OrganizationID string
}

// Resolver resolves hosts against the base domain and verified custom
// domains.
type Resolver struct {
	base   string
	dir    Directory
	custom map[string]string // normalized host -> organization ID (verified only)
}

// BaseDomainFromEnv reads and normalizes the base domain from getenv
// (normally os.Getenv).
func BaseDomainFromEnv(getenv func(string) string) (string, error) {
	return Normalize(getenv(BaseDomainEnv))
}

// New validates cfg and returns a resolver.
func New(cfg Config) (*Resolver, error) {
	base, err := Normalize(cfg.BaseDomain)
	if err != nil || !strings.Contains(base, ".") {
		return nil, errors.New("hostname: base domain must be a configured multi-label domain")
	}
	if cfg.Directory == nil {
		return nil, errors.New("hostname: directory required")
	}
	r := &Resolver{base: base, dir: cfg.Directory, custom: map[string]string{}}
	for _, d := range cfg.CustomDomains {
		if !d.Verified || d.OrganizationID == "" {
			continue // unverified domains never resolve
		}
		h, err := Normalize(d.Host)
		if err != nil {
			return nil, err
		}
		if h == base || strings.HasSuffix(h, "."+base) {
			return nil, errors.New("hostname: custom domain inside the platform base domain")
		}
		if _, dup := r.custom[h]; dup {
			return nil, errors.New("hostname: duplicate verified custom domain " + h)
		}
		r.custom[h] = d.OrganizationID
	}
	return r, nil
}

// Normalize lowercases host, strips a port and one trailing dot, and
// validates every label. IP literals are rejected.
func Normalize(host string) (string, error) {
	h := strings.TrimSpace(host)
	if h == "" {
		return "", ErrInvalidHost
	}
	if strings.HasPrefix(h, "[") { // bracketed IPv6, with or without port
		return "", ErrInvalidHost
	}
	if strings.Contains(h, ":") {
		hostOnly, _, err := net.SplitHostPort(h)
		if err != nil {
			return "", ErrInvalidHost
		}
		h = hostOnly
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "" || len(h) > 253 || net.ParseIP(h) != nil {
		return "", ErrInvalidHost
	}
	for _, label := range strings.Split(h, ".") {
		if !labelPattern.MatchString(label) {
			return "", ErrInvalidHost
		}
	}
	return h, nil
}

// Resolve returns the Organization selected by host, if any. Unknown,
// unverified, look-alike (alpha.<base>.evil.test, evilalpha.<base>) and
// nested (x.alpha.<base>) hosts resolve to nothing.
func (r *Resolver) Resolve(host string) (Selection, bool) {
	h, err := Normalize(host)
	if err != nil {
		return Selection{}, false
	}
	if org, ok := r.custom[h]; ok {
		return Selection{OrganizationID: org}, true
	}
	suffix := "." + r.base
	if !strings.HasSuffix(h, suffix) {
		return Selection{}, false
	}
	slug := strings.TrimSuffix(h, suffix)
	if strings.Contains(slug, ".") || !slugPattern.MatchString(slug) {
		return Selection{}, false
	}
	id, ok := r.dir.OrganizationIDBySlug(slug)
	if !ok || id == "" {
		return Selection{}, false
	}
	return Selection{OrganizationID: id}, true
}
