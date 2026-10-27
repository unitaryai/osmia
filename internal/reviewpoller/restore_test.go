package reviewpoller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

const restoredURL = "https://gitlab.com/acme/widgets/-/merge_requests/256"

func commentAt(id string, created time.Time) scm.ReviewComment {
	return scm.ReviewComment{ID: id, Author: "mattcamp", Body: "please fix this", Created: created}
}

func restoredPoller(t *testing.T, backend scm.Backend, r RestoredPR) (*Poller, *TrackedPR) {
	t.Helper()
	p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
	p.Restore(r)
	return p, p.tracked[r.PRURL]
}

func TestPoller_Restore(t *testing.T) {
	handled := time.Date(2026, 10, 27, 10, 53, 0, 0, time.UTC)

	t.Run("comments after the handled point are acted on, earlier ones are not", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{
			commentAt("before", handled.Add(-time.Minute)),
			commentAt("after", handled.Add(9*time.Minute)),
		}}
		p, pr := restoredPoller(t, backend, RestoredPR{PRURL: restoredURL, TicketID: "T-1", HandledUntil: handled})

		p.pollPR(context.Background(), restoredURL, pr)

		followUps := p.DrainFollowUps()
		require.Len(t, followUps, 1)
		require.Len(t, followUps[0].Comments, 1)
		assert.Equal(t, "after", followUps[0].Comments[0].ID)
		assert.True(t, pr.ProcessedIDs["before"], "pre-restart comment is recorded as handled")
	})

	t.Run("a comment with no timestamp is not replayed", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{commentAt("untimed", time.Time{})}}
		p, pr := restoredPoller(t, backend, RestoredPR{PRURL: restoredURL, HandledUntil: handled})

		p.pollPR(context.Background(), restoredURL, pr)

		assert.Nil(t, p.DrainFollowUps())
	})

	t.Run("the follow-up budget survives the restart", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{commentAt("new", handled.Add(time.Hour))}}
		p, pr := restoredPoller(t, backend, RestoredPR{PRURL: restoredURL, FollowUpCount: 3, HandledUntil: handled})

		p.pollPR(context.Background(), restoredURL, pr)

		assert.Nil(t, p.DrainFollowUps(), "budget of 3 was already spent before the restart")
	})

	t.Run("settling is measured from before the restart, not restarted", func(t *testing.T) {
		cfg := baseCfg()
		cfg.SettlingMinutes = 10
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{commentAt("new", time.Now())}}
		p := New(cfg, requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Restore(RestoredPR{PRURL: restoredURL, HandledUntil: time.Now().Add(-time.Hour)})

		p.pollPR(context.Background(), restoredURL, p.tracked[restoredURL])

		assert.Len(t, p.DrainFollowUps(), 1)
	})

	t.Run("does not overwrite a PR registered live during startup", func(t *testing.T) {
		p := New(baseCfg(), requiresActionClassifier(), testLogger())
		p.Register(restoredURL, "T-1", "live title", "", "")

		p.Restore(RestoredPR{PRURL: restoredURL, OriginalTitle: "restored title", FollowUpCount: 2, HandledUntil: handled})

		pr := p.tracked[restoredURL]
		assert.Equal(t, "live title", pr.OriginalTitle)
		assert.Zero(t, pr.FollowUpCount)
		assert.True(t, pr.IgnoreBefore.IsZero())
	})

	t.Run("a live-registered PR never filters by time", func(t *testing.T) {
		backend := &fakeSCMBackend{comments: []scm.ReviewComment{commentAt("old", handled.Add(-24*time.Hour))}}
		p := New(baseCfg(), requiresActionClassifier(), testLogger()).WithSCMBackend(backend)
		p.Register(restoredURL, "T-1", "", "", "")

		p.pollPR(context.Background(), restoredURL, p.tracked[restoredURL])

		assert.Len(t, p.DrainFollowUps(), 1, "behaviour for normally registered PRs is unchanged")
	})
}
