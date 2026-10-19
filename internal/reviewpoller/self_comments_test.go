package reviewpoller

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

// identityBackend is a fakeSCMBackend that also implements the optional
// scm.IdentityProvider capability, reporting a configurable account name.
type identityBackend struct {
	*fakeSCMBackend

	idMu    sync.Mutex
	user    string
	err     error
	idCalls int
}

var _ scm.IdentityProvider = (*identityBackend)(nil)

func (b *identityBackend) AuthenticatedUser(_ context.Context) (string, error) {
	b.idMu.Lock()
	defer b.idMu.Unlock()
	b.idCalls++
	if b.err != nil {
		return "", b.err
	}
	return b.user, nil
}

func (b *identityBackend) identityCalls() int {
	b.idMu.Lock()
	defer b.idMu.Unlock()
	return b.idCalls
}

// osmiaSummary is the comment a follow-up job posts once it has finished,
// taken from attune-workflows!502 where it fed a self-reply loop. It contains
// "fixed" and "errors", both of which the keyword classifier reads as
// actionable at error severity.
func osmiaSummary(id, author string) scm.ReviewComment {
	return scm.ReviewComment{
		ID:      id,
		Author:  author,
		Body:    "Addressed. Fixed the 2 pyright reportOptionalMemberAccess errors in tests/cnr/test_activities.py.",
		Created: time.Now(),
	}
}

const prURL = "https://gitlab.com/acme/widgets/-/merge_requests/502"

func newTrackedPoller(t *testing.T, backend scm.Backend, classifier Classifier) (*Poller, *TrackedPR) {
	t.Helper()
	p := New(baseCfg(), classifier, testLogger()).WithSCMBackend(backend)
	p.Register(prURL, "48290", "Title", "Description", "https://gitlab.com/acme/widgets")
	return p, p.tracked[prURL]
}

// TestPoller_IgnoresOwnComments reproduces the loop seen on
// attune-workflows!502 with the real keyword classifier: Osmia's account was
// named unitary_robodev, which the name-based bot filter does not recognise,
// so its own summary comment was classified as actionable and spawned
// another follow-up job.
func TestPoller_IgnoresOwnComments(t *testing.T) {
	t.Run("own summary comment does not trigger a follow-up", func(t *testing.T) {
		backend := &identityBackend{
			fakeSCMBackend: &fakeSCMBackend{comments: []scm.ReviewComment{osmiaSummary("n1", "unitary_robodev")}},
			user:           "unitary_robodev",
		}
		p, pr := newTrackedPoller(t, backend, NewRuleBasedClassifier(nil))

		p.pollPR(context.Background(), prURL, pr)

		assert.Nil(t, p.DrainFollowUps(), "Osmia must not act on its own comment")
		assert.True(t, pr.ProcessedIDs["n1"], "own comment should be marked processed so it is not re-examined")
		assert.Zero(t, pr.FollowUpCount, "own comment must not consume the follow-up budget")
	})

	t.Run("match is case-insensitive", func(t *testing.T) {
		backend := &identityBackend{
			fakeSCMBackend: &fakeSCMBackend{comments: []scm.ReviewComment{osmiaSummary("n1", "Unitary_RoboDev")}},
			user:           "unitary_robodev",
		}
		p, pr := newTrackedPoller(t, backend, NewRuleBasedClassifier(nil))

		p.pollPR(context.Background(), prURL, pr)

		assert.Nil(t, p.DrainFollowUps())
	})

	t.Run("a human comment beside an own comment is still acted on, alone", func(t *testing.T) {
		human := scm.ReviewComment{ID: "n2", Author: "mattcamp", Body: "Please fix the lint CI stage errors.", Created: time.Now()}
		backend := &identityBackend{
			fakeSCMBackend: &fakeSCMBackend{comments: []scm.ReviewComment{osmiaSummary("n1", "unitary_robodev"), human}},
			user:           "unitary_robodev",
		}
		p, pr := newTrackedPoller(t, backend, NewRuleBasedClassifier(nil))

		p.pollPR(context.Background(), prURL, pr)

		followUps := p.DrainFollowUps()
		require.Len(t, followUps, 1)
		require.Len(t, followUps[0].Comments, 1, "only the human comment belongs in the follow-up")
		assert.Equal(t, "n2", followUps[0].Comments[0].ID)
	})

	t.Run("identity is looked up once and cached across polls", func(t *testing.T) {
		backend := &identityBackend{
			fakeSCMBackend: &fakeSCMBackend{comments: []scm.ReviewComment{osmiaSummary("n1", "unitary_robodev")}},
			user:           "unitary_robodev",
		}
		p, pr := newTrackedPoller(t, backend, NewRuleBasedClassifier(nil))

		p.pollPR(context.Background(), prURL, pr)
		p.pollPR(context.Background(), prURL, pr)
		p.pollPR(context.Background(), prURL, pr)

		assert.Equal(t, 1, backend.identityCalls())
	})
}

// TestPoller_SelfIdentityFallback covers the cases where Osmia cannot learn
// its own identity. Both must fall back to the behaviour that existed before
// identity lookup, rather than failing the poll.
func TestPoller_SelfIdentityFallback(t *testing.T) {
	t.Run("lookup error falls back to classification and is retried next poll", func(t *testing.T) {
		backend := &identityBackend{
			fakeSCMBackend: &fakeSCMBackend{comments: []scm.ReviewComment{osmiaSummary("n1", "unitary_robodev")}},
			err:            fmt.Errorf("403 forbidden"),
		}
		p, pr := newTrackedPoller(t, backend, NewRuleBasedClassifier(nil))

		p.pollPR(context.Background(), prURL, pr)
		require.Len(t, p.DrainFollowUps(), 1, "without an identity the comment is classified as before")

		p.pollPR(context.Background(), prURL, pr)
		assert.Equal(t, 2, backend.identityCalls(), "a failed lookup must not be cached")
	})

	t.Run("backend without the capability behaves exactly as before", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{osmiaSummary("n1", "unitary_robodev")}}
		p, pr := newTrackedPoller(t, backend, NewRuleBasedClassifier(nil))

		p.pollPR(context.Background(), prURL, pr)

		assert.Len(t, p.DrainFollowUps(), 1)
	})

	t.Run("name-based filter still catches an account named after the project", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{osmiaSummary("n1", "osmia-bot")}}
		p, pr := newTrackedPoller(t, backend, NewRuleBasedClassifier(nil))

		p.pollPR(context.Background(), prURL, pr)

		assert.Nil(t, p.DrainFollowUps())
	})
}
