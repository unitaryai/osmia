package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/unitaryai/osmia/internal/config"
	"github.com/unitaryai/osmia/internal/reviewpoller"
	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// TestProcessFollowUpTaskCarriesParentLabels checks that a review follow-up
// keeps the parent ticket's labels. The engine chooses plugin profiles from
// them, so without them a follow-up would lose the plugins the original task
// ran with.
func TestProcessFollowUpTaskCarriesParentLabels(t *testing.T) {
	eng := &recordingEngine{name: "claude-code"}
	r := NewReconciler(&config.Config{}, testLogger(),
		WithEngine(eng),
		WithJobBuilder(&mockJobBuilder{}),
		WithK8sClient(fake.NewSimpleClientset()),
		WithNamespace("test-ns"),
	)
	r.ticketCache["sc-1"] = ticketing.Ticket{ID: "sc-1", Labels: []string{"osmia-plugins:build", "lang:go"}}

	r.processFollowUpTask(context.Background(), reviewpoller.FollowUpRequest{
		TicketID:      "sc-1",
		OriginalTitle: "Add a widget",
		RepoURL:       "https://github.com/example/repo",
		PRURL:         "https://github.com/example/repo/pull/7",
	})

	require.Equal(t, 1, eng.buildCalled)
	assert.Equal(t, []string{"osmia-plugins:build", "lang:go"}, eng.lastTask.Labels)
}
