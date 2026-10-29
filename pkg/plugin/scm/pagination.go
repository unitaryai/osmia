package scm

import (
	"net/http"
	"net/url"
	"strings"
)

// MaxPages bounds how many pages a backend follows when listing a
// paginated collection. With 100 items per page this allows 5,000 comments
// on one pull request, far beyond any real review, while stopping a server
// that keeps returning a next link from looping a poller forever.
const MaxPages = 50

// NextPageURL returns the URL of the next page from an RFC 8288 Link header,
// as sent by both GitHub and GitLab, or "" when there is no next page.
//
// The URL is only returned when it has the same scheme and host as current.
// Backends send their API token with every request, so following a next
// link to another host would hand that token to it.
func NextPageURL(h http.Header, current string) string {
	cur, err := url.Parse(current)
	if err != nil {
		return ""
	}
	for _, link := range h.Values("Link") {
		for _, part := range strings.Split(link, ",") {
			target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
			if !ok || !hasRelNext(params) {
				continue
			}
			target = strings.TrimSpace(target)
			if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
				continue
			}
			next, err := url.Parse(target[1 : len(target)-1])
			if err != nil {
				return ""
			}
			if next.Scheme != cur.Scheme || !strings.EqualFold(next.Host, cur.Host) {
				return ""
			}
			return next.String()
		}
	}
	return ""
}

// hasRelNext reports whether a Link header's parameters include rel="next".
// rel may hold several space-separated relation types.
func hasRelNext(params string) bool {
	for _, p := range strings.Split(params, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "rel") {
			continue
		}
		for _, rel := range strings.Fields(strings.Trim(strings.TrimSpace(value), `"`)) {
			if strings.EqualFold(rel, "next") {
				return true
			}
		}
	}
	return false
}
