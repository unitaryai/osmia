package reviewpoller

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/scmrouter"
	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

// testLogger returns a logger that discards output, keeping test output clean.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSCMBackend is a configurable in-package fake implementing scm.Backend,
// used to drive Poller.pollPR through its success and error branches without
// depending on the integration-tagged mock in tests/integration (different
// build tag, unrelated module-external directory).
type fakeSCMBackend struct {
	mu sync.Mutex

	prState  string // "open", "merged", "closed"; defaults to "open"
	comments []scm.ReviewComment

	statusErr error
	listErr   error
	replyErr  error

	statusCalls int
	listCalls   int

	replyToCommentCalls []replyCall
	resolveThreadCalls  []resolveCall
}

type replyCall struct {
	prURL     string
	commentID string
	threadID  string
	body      string
}

type resolveCall struct {
	prURL    string
	threadID string
}

var _ scm.Backend = (*fakeSCMBackend)(nil)

func (f *fakeSCMBackend) Name() string { return "fake" }

func (f *fakeSCMBackend) InterfaceVersion() int { return scm.InterfaceVersion }

func (f *fakeSCMBackend) CreateBranch(_ context.Context, _, _, _ string) error {
	panic("fakeSCMBackend.CreateBranch not implemented")
}

func (f *fakeSCMBackend) CreatePullRequest(_ context.Context, _ scm.CreatePullRequestInput) (*scm.PullRequest, error) {
	panic("fakeSCMBackend.CreatePullRequest not implemented")
}

func (f *fakeSCMBackend) GetPullRequestStatus(_ context.Context, prURL string) (*scm.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	state := f.prState
	if state == "" {
		state = "open"
	}
	return &scm.PullRequest{URL: prURL, State: state}, nil
}

func (f *fakeSCMBackend) ListReviewComments(_ context.Context, _ string) ([]scm.ReviewComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]scm.ReviewComment, len(f.comments))
	copy(out, f.comments)
	return out, nil
}

func (f *fakeSCMBackend) ReplyToComment(_ context.Context, prURL, commentID, threadID, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replyErr != nil {
		return f.replyErr
	}
	f.replyToCommentCalls = append(f.replyToCommentCalls, replyCall{prURL: prURL, commentID: commentID, threadID: threadID, body: body})
	return nil
}

func (f *fakeSCMBackend) ResolveThread(_ context.Context, prURL, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolveThreadCalls = append(f.resolveThreadCalls, resolveCall{prURL: prURL, threadID: threadID})
	return nil
}

func (f *fakeSCMBackend) GetDiff(_ context.Context, _, _, _ string) (string, error) {
	return "", nil
}

func (f *fakeSCMBackend) callCounts() (status, list int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statusCalls, f.listCalls
}

// stubClassifier returns a fixed classification for every comment, bypassing
// RuleBasedClassifier's keyword logic entirely.
type stubClassifier struct {
	classification Classification
	severity       string
}

func (s stubClassifier) Classify(_ context.Context, comment scm.ReviewComment) (ClassifiedComment, error) {
	return ClassifiedComment{
		ReviewComment:  comment,
		Classification: s.classification,
		Severity:       s.severity,
		Reason:         "stub",
	}, nil
}

// errClassifier always returns an error, used to exercise pollPR's
// classification-error handling branch.
type errClassifier struct{}

func (errClassifier) Classify(_ context.Context, _ scm.ReviewComment) (ClassifiedComment, error) {
	return ClassifiedComment{}, fmt.Errorf("classification boom")
}

// actionableComment builds a scm.ReviewComment that requiresActionClassifier
// treats as actionable at error severity.
func fakeComment(id string) scm.ReviewComment {
	return scm.ReviewComment{ID: id, ThreadID: "thread-" + id, Author: "alice", Body: "please fix this", Created: time.Now()}
}

func requiresActionClassifier() stubClassifier {
	return stubClassifier{classification: ClassificationRequiresAction, severity: "error"}
}

func informationalClassifier() stubClassifier {
	return stubClassifier{classification: ClassificationInformational, severity: "info"}
}

func baseCfg() config.ReviewResponseConfig {
	return config.ReviewResponseConfig{
		Enabled:             true,
		PollIntervalMinutes: 5,
		MinSeverity:         "warning",
		MaxFollowUpJobs:     3,
		ReplyToComments:     false,
	}
}

// TestPoller_Register_DuplicateIsNoOp verifies that registering the same PR
// URL twice is a no-op the second time, retaining the first call's metadata.
func TestPoller_Register_DuplicateIsNoOp(t *testing.T) {
	p := New(baseCfg(), requiresActionClassifier(), testLogger())

	p.Register("https://example.com/pr/1", "TICKET-1", "First Title", "First description", "https://example.com/repo")
	p.Register("https://example.com/pr/1", "TICKET-2", "Second Title", "Second description", "https://example.com/repo")

	require.Len(t, p.tracked, 1)
	tracked := p.tracked["https://example.com/pr/1"]
	require.NotNil(t, tracked)
	assert.Equal(t, "TICKET-1", tracked.TicketID)
	assert.Equal(t, "First Title", tracked.OriginalTitle)
	assert.Equal(t, "First description", tracked.OriginalDescription)
}

// TestPoller_DrainFollowUps_DrainsOnce verifies drain-once semantics: an
// empty poller drains to nil, and a follow-up appended once is only
// returned by the first subsequent drain.
func TestPoller_DrainFollowUps_DrainsOnce(t *testing.T) {
	p := New(baseCfg(), requiresActionClassifier(), testLogger())

	assert.Nil(t, p.DrainFollowUps())

	p.followUps = append(p.followUps, FollowUpRequest{PRURL: "https://example.com/pr/1"})

	first := p.DrainFollowUps()
	require.Len(t, first, 1)
	assert.Equal(t, "https://example.com/pr/1", first[0].PRURL)

	second := p.DrainFollowUps()
	assert.Nil(t, second)
}

// TestPoller_PollPR covers pollPR's core branches: emission, idempotency,
// severity filtering, classification-error fault isolation, PR untracking,
// backend error handling, reply behaviour, batching, and the max follow-up
// job limit.
func TestPoller_PollPR(t *testing.T) {
	t.Run("single actionable comment emits one follow-up", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{fakeComment("c1")}}
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")

		pr := p.tracked["https://example.com/pr/1"]
		p.pollPR(context.Background(), "https://example.com/pr/1", pr)

		followUps := p.DrainFollowUps()
		require.Len(t, followUps, 1)
		require.Len(t, followUps[0].Comments, 1)
		assert.Equal(t, "c1", followUps[0].Comments[0].ID)
	})

	t.Run("re-polling the same comment does not re-emit", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{fakeComment("c1")}}
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)
		require.Len(t, p.DrainFollowUps(), 1)

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)
		assert.Nil(t, p.DrainFollowUps(), "same comment should not be re-emitted on a subsequent poll")
	})

	t.Run("informational comments produce no follow-up", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{fakeComment("c1")}}
		p := New(baseCfg(), informationalClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)
		assert.Nil(t, p.DrainFollowUps())
	})

	t.Run("below MinSeverity comments produce no follow-up", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{fakeComment("c1")}}
		cfg := baseCfg()
		cfg.MinSeverity = "error"
		p := New(cfg, stubClassifier{classification: ClassificationRequiresAction, severity: "warning"}, testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)
		assert.Nil(t, p.DrainFollowUps())
	})

	t.Run("classifier error marks comment processed without emitting or crashing", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{fakeComment("c1")}}
		p := New(baseCfg(), errClassifier{}, testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		require.NotPanics(t, func() {
			p.pollPR(context.Background(), "https://example.com/pr/1", pr)
		})
		assert.Nil(t, p.DrainFollowUps())
		assert.True(t, pr.ProcessedIDs["c1"], "comment should be marked processed even on classification error")
	})

	t.Run("merged PR is untracked", func(t *testing.T) {
		backend := &fakeSCMBackend{prState: "merged"}
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)

		_, stillTracked := p.tracked["https://example.com/pr/1"]
		assert.False(t, stillTracked)
	})

	t.Run("closed PR is untracked", func(t *testing.T) {
		backend := &fakeSCMBackend{prState: "closed"}
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)

		_, stillTracked := p.tracked["https://example.com/pr/1"]
		assert.False(t, stillTracked)
	})

	t.Run("GetPullRequestStatus error leaves PR tracked without panicking", func(t *testing.T) {
		backend := &fakeSCMBackend{statusErr: fmt.Errorf("status boom")}
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		require.NotPanics(t, func() {
			p.pollPR(context.Background(), "https://example.com/pr/1", pr)
		})
		_, stillTracked := p.tracked["https://example.com/pr/1"]
		assert.True(t, stillTracked)
		assert.Nil(t, p.DrainFollowUps())
	})

	t.Run("ListReviewComments error leaves PR tracked without panicking", func(t *testing.T) {
		backend := &fakeSCMBackend{listErr: fmt.Errorf("list boom")}
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		require.NotPanics(t, func() {
			p.pollPR(context.Background(), "https://example.com/pr/1", pr)
		})
		_, stillTracked := p.tracked["https://example.com/pr/1"]
		assert.True(t, stillTracked)
		assert.Nil(t, p.DrainFollowUps())
	})

	t.Run("ReplyToComments true replies once per actionable comment", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{fakeComment("c1"), fakeComment("c2")}}
		cfg := baseCfg()
		cfg.ReplyToComments = true
		p := New(cfg, requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)

		assert.Len(t, backend.replyToCommentCalls, 2)
	})

	t.Run("ReplyToComments false never replies", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{fakeComment("c1")}}
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)

		assert.Empty(t, backend.replyToCommentCalls)
	})

	t.Run("multiple actionable comments in one poll are batched into a single follow-up", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{fakeComment("c1"), fakeComment("c2"), fakeComment("c3")}}
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)

		followUps := p.DrainFollowUps()
		require.Len(t, followUps, 1)
		assert.Len(t, followUps[0].Comments, 3)
	})

	t.Run("MaxFollowUpJobs stops emitting once the limit is reached", func(t *testing.T) {
		cfg := baseCfg()
		cfg.MaxFollowUpJobs = 2
		backend := &fakeSCMBackend{}
		p := New(cfg, requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		var totalEmitted int
		for i := 0; i < 4; i++ {
			backend.comments = []scm.ReviewComment{fakeComment(fmt.Sprintf("poll-%d", i))}
			p.pollPR(context.Background(), "https://example.com/pr/1", pr)
			totalEmitted += len(p.DrainFollowUps())
		}

		assert.Equal(t, 2, totalEmitted, "emission should stop once MaxFollowUpJobs is reached")
		assert.Equal(t, 2, pr.FollowUpCount)
	})

	t.Run("SettlingMinutes skips poll entirely for a just-registered PR", func(t *testing.T) {
		cfg := baseCfg()
		cfg.SettlingMinutes = 10
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{fakeComment("c1")}}
		p := New(cfg, requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)

		status, list := backend.callCounts()
		assert.Equal(t, 0, status, "no backend I/O should occur during the settling period")
		assert.Equal(t, 0, list, "no backend I/O should occur during the settling period")
		assert.Nil(t, p.DrainFollowUps())
	})

	t.Run("SettlingMinutes elapsed polls normally", func(t *testing.T) {
		cfg := baseCfg()
		cfg.SettlingMinutes = 10
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{fakeComment("c1")}}
		p := New(cfg, requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register("https://example.com/pr/1", "T-1", "Title", "Description", "https://example.com/repo")
		pr := p.tracked["https://example.com/pr/1"]
		pr.RegisteredAt = time.Now().Add(-11 * time.Minute)

		p.pollPR(context.Background(), "https://example.com/pr/1", pr)

		status, list := backend.callCounts()
		assert.Equal(t, 1, status)
		assert.Equal(t, 1, list)
		require.Len(t, p.DrainFollowUps(), 1)
	})
}

// TestPoller_ScmFor covers scmFor's backend-resolution branches: no backend
// configured, a single backend, and a multi-backend router.
func TestPoller_ScmFor(t *testing.T) {
	t.Run("no backend configured returns an error", func(t *testing.T) {
		p := New(baseCfg(), requiresActionClassifier(), testLogger())
		_, err := p.scmFor("https://example.com/repo")
		require.Error(t, err)
	})

	t.Run("WithSCMBackend returns the configured backend", func(t *testing.T) {
		backend := &fakeSCMBackend{}
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		got, err := p.scmFor("https://example.com/repo")
		require.NoError(t, err)
		assert.Same(t, backend, got)
	})

	t.Run("WithSCMRouter delegates to the router by host", func(t *testing.T) {
		githubBackend := &fakeSCMBackend{}
		gitlabBackend := &fakeSCMBackend{}
		router := scmrouter.NewRouter(
			scmrouter.Entry{Match: "github.com", Backend: githubBackend},
			scmrouter.Entry{Match: "gitlab.com", Backend: gitlabBackend},
		)
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMRouter(router)

		got, err := p.scmFor("https://gitlab.com/acme/widgets")
		require.NoError(t, err)
		assert.Same(t, gitlabBackend, got)

		got, err = p.scmFor("https://github.com/acme/widgets")
		require.NoError(t, err)
		assert.Same(t, githubBackend, got)
	})
}

// TestPoller_Start_StopsOnContextCancelled verifies that Start returns
// promptly when given an already-cancelled context, without waiting for the
// minute-granularity ticker to fire.
func TestPoller_Start_StopsOnContextCancelled(t *testing.T) {
	p := New(baseCfg(), requiresActionClassifier(), testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		p.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
		// expected: Start returned promptly via ctx.Done().
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return promptly after context cancellation")
	}
}

// TestBuildEnrichedDescription covers buildEnrichedDescription's formatting
// for zero, one (with and without file position), and multiple comments.
func TestBuildEnrichedDescription(t *testing.T) {
	t.Run("no comments", func(t *testing.T) {
		got := buildEnrichedDescription("Original description.", nil, nil)
		assert.Contains(t, got, "Original description.")
		assert.Contains(t, got, "# Review Comments")
		assert.Contains(t, got, "Please address all of the above review comments.")
	})

	t.Run("single comment with FilePath and Line", func(t *testing.T) {
		comments := []ClassifiedComment{
			{
				ReviewComment: scm.ReviewComment{Author: "alice", Body: "fix this", FilePath: "main.go", Line: 42},
			},
		}
		got := buildEnrichedDescription("Original description.", comments, nil)
		assert.Contains(t, got, "Comment from @alice")
		assert.Contains(t, got, "> fix this")
		assert.Contains(t, got, "main.go:42")
	})

	t.Run("single comment without FilePath", func(t *testing.T) {
		comments := []ClassifiedComment{
			{ReviewComment: scm.ReviewComment{Author: "bob", Body: "please improve this"}},
		}
		got := buildEnrichedDescription("Original description.", comments, nil)
		assert.Contains(t, got, "Comment from @bob")
		assert.NotContains(t, got, ".go:")
	})

	t.Run("multiple comments use a separator between entries", func(t *testing.T) {
		comments := []ClassifiedComment{
			{ReviewComment: scm.ReviewComment{Author: "alice", Body: "first comment"}},
			{ReviewComment: scm.ReviewComment{Author: "bob", Body: "second comment"}},
		}
		got := buildEnrichedDescription("Original description.", comments, nil)
		assert.Contains(t, got, "Comment from @alice")
		assert.Contains(t, got, "Comment from @bob")
		assert.Contains(t, got, "\n---\n")
	})
}

// TestMeetsMinSeverity covers all ordered pairs across info/warning/error,
// plus an unrecognised minSeverity value.
func TestMeetsMinSeverity(t *testing.T) {
	tests := []struct {
		severity    string
		minSeverity string
		want        bool
	}{
		{"info", "info", true},
		{"info", "warning", false},
		{"info", "error", false},
		{"warning", "info", true},
		{"warning", "warning", true},
		{"warning", "error", false},
		{"error", "info", true},
		{"error", "warning", true},
		{"error", "error", true},
		// Unrecognised minSeverity falls back to the map's zero value (0),
		// so any known severity meets it. Documents existing behaviour.
		{"info", "unrecognised", true},
		// An unrecognised severity also resolves to 0, so it only meets an
		// unrecognised or "info" minimum.
		{"unrecognised", "info", true},
		{"unrecognised", "warning", false},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.severity, tt.minSeverity), func(t *testing.T) {
			assert.Equal(t, tt.want, meetsMinSeverity(tt.severity, tt.minSeverity))
		})
	}
}
