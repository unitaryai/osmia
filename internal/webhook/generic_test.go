package webhook

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	svix "github.com/svix/svix-webhooks/go"
)

func TestHandleGeneric_HMAC(t *testing.T) {
	secret := "test-generic-secret"

	cfg := &GenericConfig{
		AuthMode: GenericAuthHMAC,
		Secret:   secret,
		FieldMapping: map[string]string{
			"issue.id":          "id",
			"issue.title":       "title",
			"issue.description": "description",
			"issue.type":        "ticket_type",
			"issue.repo":        "repo_url",
			"issue.url":         "external_url",
		},
	}

	validPayload := map[string]any{
		"issue": map[string]any{
			"id":          "42",
			"title":       "Fix bug",
			"description": "Something is broken",
			"type":        "bug",
			"repo":        "https://github.com/owner/repo",
			"url":         "https://github.com/owner/repo/issues/42",
		},
	}

	tests := []struct {
		name       string
		payload    any
		cfg        *GenericConfig
		sigFunc    func([]byte) string
		wantStatus int
		wantCalls  int
	}{
		{
			name:    "valid hmac payload",
			payload: validPayload,
			cfg:     cfg,
			sigFunc: func(b []byte) string {
				return computeGenericHMACSignature(b, secret)
			},
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
		{
			name:       "invalid hmac signature",
			payload:    validPayload,
			cfg:        cfg,
			sigFunc:    func(_ []byte) string { return "sha256=deadbeef" },
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:       "missing signature",
			payload:    validPayload,
			cfg:        cfg,
			sigFunc:    func(_ []byte) string { return "" },
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name: "missing ticket ID returns 400",
			payload: map[string]any{
				"issue": map[string]any{
					"title": "No ID here",
				},
			},
			cfg: cfg,
			sigFunc: func(b []byte) string {
				return computeGenericHMACSignature(b, secret)
			},
			wantStatus: http.StatusBadRequest,
			wantCalls:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockEventHandler{}
			srv := NewServer(testLogger(), mock, WithGenericConfig(tc.cfg))

			body, err := json.Marshal(tc.payload)
			require.NoError(t, err)

			sig := tc.sigFunc(body)

			req := httptest.NewRequest(http.MethodPost, "/webhooks/generic", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if sig != "" {
				req.Header.Set("X-Webhook-Signature", sig)
			}

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Len(t, mock.calls, tc.wantCalls)

			if tc.wantCalls > 0 {
				call := mock.calls[0]
				assert.Equal(t, "generic", call.source)
				require.Len(t, call.tickets, 1)
				assert.Equal(t, "42", call.tickets[0].ID)
				assert.Equal(t, "Fix bug", call.tickets[0].Title)
				assert.Equal(t, "Something is broken", call.tickets[0].Description)
				assert.Equal(t, "bug", call.tickets[0].TicketType)
				assert.Equal(t, "https://github.com/owner/repo", call.tickets[0].RepoURL)
				assert.Equal(t, "https://github.com/owner/repo/issues/42", call.tickets[0].ExternalURL)
			}
		})
	}
}

func TestHandleGeneric_Bearer(t *testing.T) {
	token := "my-bearer-token"

	cfg := &GenericConfig{
		AuthMode: GenericAuthBearer,
		Secret:   token,
		FieldMapping: map[string]string{
			"id":    "id",
			"title": "title",
		},
	}

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
		wantCalls  int
	}{
		{
			name:       "valid bearer token",
			authHeader: "Bearer " + token,
			wantStatus: http.StatusOK,
			wantCalls:  1,
		},
		{
			name:       "wrong bearer token",
			authHeader: "Bearer wrong-token",
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
		{
			name:       "missing authorization header",
			authHeader: "",
			wantStatus: http.StatusUnauthorized,
			wantCalls:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockEventHandler{}
			srv := NewServer(testLogger(), mock, WithGenericConfig(cfg))

			payload := map[string]any{"id": "1", "title": "Test"}
			body, _ := json.Marshal(payload)

			req := httptest.NewRequest(http.MethodPost, "/webhooks/generic", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Len(t, mock.calls, tc.wantCalls)
		})
	}
}

func TestHandleGeneric_CustomSignatureHeader(t *testing.T) {
	secret := "test-secret"

	cfg := &GenericConfig{
		AuthMode:        GenericAuthHMAC,
		Secret:          secret,
		SignatureHeader: "X-Custom-Sig",
		FieldMapping: map[string]string{
			"id":    "id",
			"title": "title",
		},
	}

	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock, WithGenericConfig(cfg))

	payload := map[string]any{"id": "1", "title": "Test"}
	body, _ := json.Marshal(payload)
	sig := computeGenericHMACSignature(body, secret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/generic", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Custom-Sig", sig)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, mock.calls, 1)
}

func TestHandleGeneric_NotConfigured(t *testing.T) {
	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock) // no generic config

	req := httptest.NewRequest(http.MethodPost, "/webhooks/generic", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHandleGeneric_MalformedJSON(t *testing.T) {
	secret := "test-secret"
	cfg := &GenericConfig{
		AuthMode:     GenericAuthHMAC,
		Secret:       secret,
		FieldMapping: map[string]string{"id": "id"},
	}

	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock, WithGenericConfig(cfg))

	body := []byte(`not json`)
	sig := computeGenericHMACSignature(body, secret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/generic", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleGeneric_HandlerError(t *testing.T) {
	secret := "test-secret"
	cfg := &GenericConfig{
		AuthMode:     GenericAuthHMAC,
		Secret:       secret,
		FieldMapping: map[string]string{"id": "id", "title": "title"},
	}

	mock := &mockEventHandler{err: fmt.Errorf("handler failed")}
	srv := NewServer(testLogger(), mock, WithGenericConfig(cfg))

	payload := map[string]any{"id": "1", "title": "Test"}
	body, _ := json.Marshal(payload)
	sig := computeGenericHMACSignature(body, secret)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/generic", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", sig)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestExtractJSONPath(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		path string
		want string
	}{
		{
			name: "simple string field",
			data: map[string]any{"title": "Hello"},
			path: "title",
			want: "Hello",
		},
		{
			name: "nested field",
			data: map[string]any{
				"issue": map[string]any{
					"title": "Nested title",
				},
			},
			path: "issue.title",
			want: "Nested title",
		},
		{
			name: "deeply nested field",
			data: map[string]any{
				"a": map[string]any{
					"b": map[string]any{
						"c": "deep value",
					},
				},
			},
			path: "a.b.c",
			want: "deep value",
		},
		{
			name: "numeric field",
			data: map[string]any{"id": float64(42)},
			path: "id",
			want: "42",
		},
		{
			name: "boolean field",
			data: map[string]any{"active": true},
			path: "active",
			want: "true",
		},
		{
			name: "missing field",
			data: map[string]any{"title": "Hello"},
			path: "missing",
			want: "",
		},
		{
			name: "missing nested field",
			data: map[string]any{
				"issue": map[string]any{},
			},
			path: "issue.title",
			want: "",
		},
		{
			name: "path through non-map",
			data: map[string]any{"title": "string"},
			path: "title.nested",
			want: "",
		},
		{
			name: "nil value",
			data: map[string]any{"title": nil},
			path: "title",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractJSONPath(tc.data, tc.path)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestValidateGenericHMACSignature(t *testing.T) {
	body := []byte(`{"test": true}`)
	secret := "my-secret"

	tests := []struct {
		name string
		sig  string
		want bool
	}{
		{
			name: "valid with prefix",
			sig:  computeGenericHMACSignature(body, secret),
			want: true,
		},
		{
			name: "valid without prefix",
			sig: func() string {
				full := computeGenericHMACSignature(body, secret)
				return full[len("sha256="):]
			}(),
			want: true,
		},
		{
			name: "empty signature",
			sig:  "",
			want: false,
		},
		{
			name: "invalid hex",
			sig:  "sha256=zzzz",
			want: false,
		},
		{
			name: "wrong secret",
			sig:  computeGenericHMACSignature(body, "wrong-secret"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := validateGenericHMACSignature(body, tc.sig, secret)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestHandleGeneric_Svix(t *testing.T) {
	// Build a valid Svix-style secret: "whsec_" + base64(key bytes). The
	// library accepts this format directly via NewWebhook.
	secret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("test-svix-key-material-32-bytes!"))
	wrongSecret := "whsec_" + base64.StdEncoding.EncodeToString([]byte("wrong-key-material--------xxxxxx"))

	cfg := &GenericConfig{
		AuthMode: GenericAuthSvix,
		Secret:   secret,
		FieldMapping: map[string]string{
			"id":    "id",
			"title": "title",
		},
	}

	payload := map[string]any{"id": "msg_001", "title": "Test"}
	body, _ := json.Marshal(payload)

	// Use the library to compute valid signatures so the test exercises
	// the same code path the production handler relies on.
	signWith := func(t *testing.T, msgSecret string, ts time.Time) string {
		t.Helper()
		wh, err := svix.NewWebhook(msgSecret)
		require.NoError(t, err)
		sig, err := wh.Sign("msg_2KWPB", ts, body)
		require.NoError(t, err)
		return sig
	}

	tests := []struct {
		name        string
		signSecret  string // empty means do not sign (test missing/invalid)
		signAt      time.Time
		idHeader    string
		tsHeader    string // empty means derive from signAt
		sigHeader   string // empty means compute via signWith
		headerStyle string // "webhook" or "svix"
		wantStatus  int
		wantCalls   int
	}{
		{
			name:        "valid signature with webhook- prefix",
			signSecret:  secret,
			signAt:      time.Now(),
			idHeader:    "msg_2KWPB",
			headerStyle: "webhook",
			wantStatus:  http.StatusOK,
			wantCalls:   1,
		},
		{
			name:        "valid signature with svix- prefix",
			signSecret:  secret,
			signAt:      time.Now(),
			idHeader:    "msg_2KWPB",
			headerStyle: "svix",
			wantStatus:  http.StatusOK,
			wantCalls:   1,
		},
		{
			name:        "wrong secret rejected",
			signSecret:  wrongSecret,
			signAt:      time.Now(),
			idHeader:    "msg_2KWPB",
			headerStyle: "webhook",
			wantStatus:  http.StatusUnauthorized,
			wantCalls:   0,
		},
		{
			name:        "stale timestamp rejected",
			signSecret:  secret,
			signAt:      time.Now().Add(-10 * time.Minute),
			idHeader:    "msg_2KWPB",
			headerStyle: "webhook",
			wantStatus:  http.StatusUnauthorized,
			wantCalls:   0,
		},
		{
			name:        "missing headers rejected",
			signSecret:  "",
			headerStyle: "webhook",
			wantStatus:  http.StatusUnauthorized,
			wantCalls:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockEventHandler{}
			srv := NewServer(testLogger(), mock, WithGenericConfig(cfg))

			req := httptest.NewRequest(http.MethodPost, "/webhooks/generic", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")

			if tc.signSecret != "" {
				ts := tc.signAt
				sig := signWith(t, tc.signSecret, ts)
				req.Header.Set(tc.headerStyle+"-id", tc.idHeader)
				req.Header.Set(tc.headerStyle+"-timestamp", strconv.FormatInt(ts.Unix(), 10))
				req.Header.Set(tc.headerStyle+"-signature", sig)
			}

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Len(t, mock.calls, tc.wantCalls)
		})
	}
}

func TestHandleGeneric_SvixInvalidSecret(t *testing.T) {
	// A non-base64 secret with the whsec_ prefix should be rejected at
	// configuration time (NewWebhook returns an error), surfaced as 500.
	cfg := &GenericConfig{
		AuthMode:     GenericAuthSvix,
		Secret:       "whsec_!!!notbase64!!!",
		FieldMapping: map[string]string{"id": "id"},
	}

	mock := &mockEventHandler{}
	srv := NewServer(testLogger(), mock, WithGenericConfig(cfg))

	body := []byte(`{"id": "x"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/generic", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("webhook-id", "msg_x")
	req.Header.Set("webhook-timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("webhook-signature", "v1,deadbeef")

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, mock.calls)
}
