package httpsafe

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seenHeaders records the credential headers a redirect target received.
type seenHeaders struct {
	token, auth string
}

// redirectTo serves a redirect to target.
func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// recordHeaders serves 200 and records the credential headers it received.
func recordHeaders(seen *seenHeaders) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		seen.token = r.Header.Get("PRIVATE-TOKEN")
		seen.auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}
}

func getWithCredentials(t *testing.T, client *http.Client, url string) error {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("PRIVATE-TOKEN", "secret")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func TestCredentialSafeRedirects(t *testing.T) {
	t.Run("a redirect to another host loses the credentials", func(t *testing.T) {
		var seen seenHeaders
		target := httptest.NewServer(recordHeaders(&seen))
		defer target.Close()
		origin := httptest.NewServer(redirectTo(target.URL))
		defer origin.Close()

		require.NoError(t, getWithCredentials(t, CredentialSafeRedirects(nil, "PRIVATE-TOKEN"), origin.URL))
		assert.Empty(t, seen.token, "a custom token header must not follow the redirect")
		assert.Empty(t, seen.auth, "Authorization must not follow a redirect to another port")
	})

	t.Run("a redirect on the same origin keeps the credentials", func(t *testing.T) {
		var seen seenHeaders
		mux := http.NewServeMux()
		mux.HandleFunc("/start", redirectTo("/end"))
		mux.HandleFunc("/end", recordHeaders(&seen))
		srv := httptest.NewServer(mux)
		defer srv.Close()

		require.NoError(t, getWithCredentials(t, CredentialSafeRedirects(nil, "PRIVATE-TOKEN"), srv.URL+"/start"))
		assert.Equal(t, "secret", seen.token)
		assert.Equal(t, "Bearer secret", seen.auth)
	})

	t.Run("a redirect from https to http is refused", func(t *testing.T) {
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("the http target must not be requested")
			w.WriteHeader(http.StatusOK)
		}))
		defer plain.Close()
		secure := httptest.NewTLSServer(redirectTo(plain.URL))
		defer secure.Close()

		err := getWithCredentials(t, CredentialSafeRedirects(secure.Client(), "PRIVATE-TOKEN"), secure.URL)
		require.ErrorContains(t, err, "refusing redirect from https to http")
	})

	t.Run("the client passed in is not changed and its own policy still runs", func(t *testing.T) {
		policyErr := errors.New("no redirects here")
		base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return policyErr }}
		srv := httptest.NewServer(redirectTo("/elsewhere"))
		defer srv.Close()

		safe := CredentialSafeRedirects(base, "PRIVATE-TOKEN")
		require.NotSame(t, base, safe)
		err := getWithCredentials(t, safe, srv.URL)
		require.ErrorIs(t, err, policyErr)
		assert.Nil(t, http.DefaultClient.CheckRedirect, "http.DefaultClient must be left alone")
	})

	t.Run("redirect loops stop", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/loop", redirectTo("/loop"))
		srv := httptest.NewServer(mux)
		defer srv.Close()

		err := getWithCredentials(t, CredentialSafeRedirects(nil), srv.URL+"/loop")
		require.ErrorContains(t, err, "stopped after 10 redirects")
	})
}
