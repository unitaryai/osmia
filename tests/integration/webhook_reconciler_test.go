//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/controller"
	"github.com/unitaryai/osmia/internal/jobbuilder"
	"github.com/unitaryai/osmia/internal/webhook"
	"github.com/unitaryai/osmia/pkg/engine"
	"github.com/unitaryai/osmia/pkg/engine/claudecode"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

const testWebhookSecret = "integration-test-secret"

// webhookTestTicketing implements ticketing.Backend for webhook integration tests.
type webhookTestTicketing struct {
	mu             sync.Mutex
	markedProgress []string
	markedComplete []string
	markedFailed   []string
}

func (m *webhookTestTicketing) PollReadyTickets(_ context.Context) ([]ticketing.Ticket, error) {
	return nil, nil
}

func (m *webhookTestTicketing) MarkInProgress(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markedProgress = append(m.markedProgress, id)
	return nil
}

func (m *webhookTestTicketing) MarkComplete(_ context.Context, id string, _ engine.TaskResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markedComplete = append(m.markedComplete, id)
	return nil
}

func (m *webhookTestTicketing) MarkFailed(_ context.Context, id string, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.markedFailed = append(m.markedFailed, id)
	return nil
}

func (m *webhookTestTicketing) AddComment(_ context.Context, _ string, _ string) error { return nil }
func (m *webhookTestTicketing) Name() string                                           { return "mock" }
func (m *webhookTestTicketing) InterfaceVersion() int                                  { return ticketing.InterfaceVersion }

// webhookAdapter bridges webhook events to the reconciler, mirroring the
// pattern used in cmd/osmia/main.go.
type webhookAdapter struct {
	reconciler *controller.Reconciler
	logger     *slog.Logger
}

func (a *webhookAdapter) HandleWebhookEvent(ctx context.Context, source string, tickets []ticketing.Ticket) error {
	for i := range tickets {
		if err := a.reconciler.ProcessTicket(ctx, tickets[i]); err != nil {
			a.logger.Error("failed to process webhook ticket",
				"source", source,
				"ticket_id", tickets[i].ID,
				"error", err,
			)
		}
	}
	return nil
}

// computeTestGitHubSignature computes a GitHub HMAC-SHA256 signature for tests.
func computeTestGitHubSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}

// githubIssuePayload returns a sample GitHub issues.opened webhook payload.
func githubIssuePayload(issueNumber int) []byte {
	return []byte(fmt.Sprintf(`{
		"action": "opened",
		"issue": {
			"number": %d,
			"title": "Test webhook issue",
			"body": "Integration test body",
			"html_url": "https://github.com/org/repo/issues/%d",
			"labels": [{"name": "osmia"}]
		},
		"repository": {
			"full_name": "org/repo",
			"html_url": "https://github.com/org/repo"
		}
	}`, issueNumber, issueNumber))
}

// newWebhookTestStack sets up a full webhook → reconciler → fake K8s test stack.
func newWebhookTestStack(t *testing.T) (*httptest.Server, *fake.Clientset, *webhookTestTicketing) {
	t.Helper()

	k8s := fake.NewSimpleClientset()
	tb := &webhookTestTicketing{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg := &config.Config{
		Engines: config.EnginesConfig{Default: "claude-code"},
		GuardRails: config.GuardRailsConfig{
			MaxConcurrentJobs:     5,
			MaxJobDurationMinutes: 120,
			AllowedRepos:          []string{"https://github.com/org/*"},
			AllowedTaskTypes:      []string{"issue"},
		},
	}

	eng := claudecode.New()
	jb := jobbuilder.NewJobBuilder("test-ns")

	reconciler := controller.NewReconciler(cfg, logger,
		controller.WithTicketing(tb),
		controller.WithEngine(eng),
		controller.WithJobBuilder(jb),
		controller.WithK8sClient(k8s),
		controller.WithNamespace("test-ns"),
	)

	adapter := &webhookAdapter{reconciler: reconciler, logger: logger}
	whServer := webhook.NewServer(logger, adapter,
		webhook.WithSecret("github", testWebhookSecret),
	)

	ts := httptest.NewServer(whServer)
	t.Cleanup(ts.Close)

	return ts, k8s, tb
}

// TestWebhookToReconcilerPipeline verifies the full flow: a valid GitHub
// webhook payload is accepted, parsed, and results in a K8s Job creation.
func TestWebhookToReconcilerPipeline(t *testing.T) {
	t.Parallel()

	ts, k8s, tb := newWebhookTestStack(t)

	payload := githubIssuePayload(42)
	sig := computeTestGitHubSignature(payload, testWebhookSecret)

	req, err := http.NewRequest("POST", ts.URL+"/webhooks/github", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", sig)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify a Job was created.
	ctx := context.Background()
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 1, "expected one Job to be created from webhook")

	// Verify ticket was marked in progress.
	tb.mu.Lock()
	assert.Contains(t, tb.markedProgress, "42")
	tb.mu.Unlock()
}

// TestWebhookInvalidSignatureBlocksProcessing verifies that a request with
// an incorrect HMAC signature is rejected and no Job is created.
func TestWebhookInvalidSignatureBlocksProcessing(t *testing.T) {
	t.Parallel()

	ts, k8s, _ := newWebhookTestStack(t)

	payload := githubIssuePayload(43)
	badSig := computeTestGitHubSignature(payload, "wrong-secret")

	req, err := http.NewRequest("POST", ts.URL+"/webhooks/github", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", badSig)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Verify no Job was created.
	ctx := context.Background()
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, jobs.Items, "no Jobs should be created with invalid signature")
}

// TestWebhookNonIssueEventNoJob verifies that non-issue events (e.g. push)
// are accepted but do not create Jobs.
func TestWebhookNonIssueEventNoJob(t *testing.T) {
	t.Parallel()

	ts, k8s, _ := newWebhookTestStack(t)

	payload := []byte(`{"ref":"refs/heads/main","commits":[]}`)
	sig := computeTestGitHubSignature(payload, testWebhookSecret)

	req, err := http.NewRequest("POST", ts.URL+"/webhooks/github", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", sig)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "push events should be accepted")

	// Verify no Job was created.
	ctx := context.Background()
	jobs, err := k8s.BatchV1().Jobs("test-ns").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, jobs.Items, "push events should not create Jobs")
}
