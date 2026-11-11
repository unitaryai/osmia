// Package httpsafe holds HTTP client helpers for plugins that send
// credentials to an external API.
package httpsafe

import (
	"errors"
	"net/http"
)

// maxRedirects matches the limit of Go's default redirect policy.
const maxRedirects = 10

// CredentialSafeRedirects returns a copy of client whose redirect policy
// removes the named credential headers whenever a redirect leaves the origin
// of the first request, and refuses a redirect from https to http.
//
// Go's client already drops Authorization and Cookie on a redirect to a
// different domain, but it compares hosts loosely (the same host on another
// port keeps them) and it copies every other header, including custom token
// headers such as GitLab's PRIVATE-TOKEN, to any host at all. An API answering
// with a redirect, for example to object storage for a job log, would then
// hand the token to that host.
//
// The client passed in is not modified, so it is safe to pass
// http.DefaultClient or a client shared with other code. Any CheckRedirect it
// already has still runs, after the headers have been removed.
func CredentialSafeRedirects(client *http.Client, credentialHeaders ...string) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	safe := *client
	next := client.CheckRedirect
	safe.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("stopped after 10 redirects")
		}
		first := via[0].URL
		if first.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("refusing redirect from https to http")
		}
		if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
			for _, h := range credentialHeaders {
				req.Header.Del(h)
			}
			req.Header.Del("Authorization")
		}
		if next != nil {
			return next(req, via)
		}
		return nil
	}
	return &safe
}
