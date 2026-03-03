// Package scmrouter implements host-based routing to multiple SCM backends.
// It selects the appropriate backend for a given repository URL by matching
// the URL's host against a list of glob patterns in registration order.
package scmrouter

import (
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/unitaryai/robodev/pkg/plugin/scm"
)

// Entry pairs a host-pattern with an SCM backend.
type Entry struct {
	// Match is a host or glob pattern matched against the repository URL host
	// (e.g. "github.com", "*.internal.example.com").
	Match string

	// Backend is the SCM backend to use when Match is satisfied.
	Backend scm.Backend
}

// backendEntry is the unexported internal representation of a routing entry.
type backendEntry struct {
	match   string
	backend scm.Backend
}

// Router holds an ordered list of SCM backends and routes repository URLs to
// the appropriate backend based on host-pattern matching.
type Router struct {
	backends []backendEntry
}

// NewRouter constructs a Router from the provided entries. Entries are
// evaluated in order; the first matching entry wins.
func NewRouter(entries ...Entry) *Router {
	be := make([]backendEntry, len(entries))
	for i, e := range entries {
		be[i] = backendEntry{match: e.Match, backend: e.Backend}
	}
	return &Router{backends: be}
}

// For returns the SCM backend appropriate for the given repoURL. The host
// component of the URL is extracted and tested against each registered
// pattern using filepath.Match semantics. The first matching backend is
// returned. If no pattern matches, the first configured backend is returned
// as a fallback. If no backends are configured, an error is returned.
func (r *Router) For(repoURL string) (scm.Backend, error) {
	if len(r.backends) == 0 {
		return nil, fmt.Errorf("no SCM backends configured")
	}

	u, err := url.Parse(repoURL)
	if err != nil {
		return nil, fmt.Errorf("parsing repository URL: %w", err)
	}

	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("repository URL has no host: %s", repoURL)
	}

	for _, entry := range r.backends {
		matched, err := filepath.Match(entry.match, host)
		if err != nil {
			// filepath.Match only returns an error for malformed patterns.
			return nil, fmt.Errorf("invalid host pattern %q: %w", entry.match, err)
		}
		if matched {
			return entry.backend, nil
		}
	}

	// No pattern matched; return the first backend as a fallback.
	return r.backends[0].backend, nil
}

// Len returns the number of backends registered with this router.
func (r *Router) Len() int {
	return len(r.backends)
}
