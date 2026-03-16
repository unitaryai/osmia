package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// MemoryContext holds the retrieved knowledge to be injected into a prompt.
type MemoryContext struct {
	// RelevantFacts contains facts relevant to the current task.
	RelevantFacts []Fact `json:"relevant_facts"`
	// EngineInsights contains engine-specific observations.
	EngineInsights []string `json:"engine_insights"`
	// KnownIssues contains previously observed failure patterns.
	KnownIssues []string `json:"known_issues"`
	// FormattedSection is the pre-rendered markdown section ready for
	// injection into a prompt.
	FormattedSection string `json:"formatted_section"`
}

// QueryEngine provides smart retrieval of memory context for prompt injection.
type QueryEngine struct {
	graph  *Graph
	logger *slog.Logger
}

// NewQueryEngine creates a new QueryEngine for the given graph.
func NewQueryEngine(graph *Graph, logger *slog.Logger) *QueryEngine {
	return &QueryEngine{
		graph:  graph,
		logger: logger,
	}
}

// QueryForTask retrieves relevant memory context for a task, applying
// temporal weighting and tenant isolation. The returned MemoryContext
// includes a pre-formatted markdown section suitable for prompt injection.
func (qe *QueryEngine) QueryForTask(
	ctx context.Context,
	taskDescription string,
	repoURL string,
	engineName string,
	tenantID string,
) (*MemoryContext, error) {
	query := GraphQuery{
		TaskDescription: taskDescription,
		RepoURL:         repoURL,
		Engine:          engineName,
		TenantID:        tenantID,
		MaxResults:      20,
	}

	nodes, err := qe.graph.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("querying memory graph: %w", err)
	}

	mc := &MemoryContext{}

	for _, node := range nodes {
		switch n := node.(type) {
		case *Fact:
			mc.RelevantFacts = append(mc.RelevantFacts, *n)
			if n.FactKind == FactTypeFailurePattern {
				mc.KnownIssues = append(mc.KnownIssues, n.Content)
			}
		case *EngineProfile:
			for taskType, rate := range n.SuccessRate {
				mc.EngineInsights = append(mc.EngineInsights,
					fmt.Sprintf("%s has %.0f%% success rate on %s tasks", n.EngineName, rate*100, taskType))
			}
			for _, s := range n.Strengths {
				mc.EngineInsights = append(mc.EngineInsights,
					fmt.Sprintf("%s strength: %s", n.EngineName, s))
			}
			for _, w := range n.Weaknesses {
				mc.EngineInsights = append(mc.EngineInsights,
					fmt.Sprintf("%s weakness: %s", n.EngineName, w))
			}
		case *Pattern:
			mc.EngineInsights = append(mc.EngineInsights, n.Description)
		}
	}

	mc.FormattedSection = qe.formatSection(mc)

	qe.logger.Debug("queried memory for task",
		"facts", len(mc.RelevantFacts),
		"insights", len(mc.EngineInsights),
		"issues", len(mc.KnownIssues),
	)

	return mc, nil
}

// formatSection renders the MemoryContext as a markdown section suitable
// for injection into a prompt.
func (qe *QueryEngine) formatSection(mc *MemoryContext) string {
	if len(mc.RelevantFacts) == 0 && len(mc.EngineInsights) == 0 && len(mc.KnownIssues) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Prior Knowledge\n\n")
	b.WriteString("The following context has been retrieved from previous task executions.\n\n")

	if len(mc.RelevantFacts) > 0 {
		b.WriteString("### Relevant Facts\n\n")
		for _, fact := range mc.RelevantFacts {
			b.WriteString(fmt.Sprintf("- %s (confidence: %.0f%%, source: %s)\n",
				fact.Content, fact.Confidence*100, fact.Source))
		}
		b.WriteString("\n")
	}

	if len(mc.EngineInsights) > 0 {
		b.WriteString("### Engine Insights\n\n")
		for _, insight := range mc.EngineInsights {
			b.WriteString(fmt.Sprintf("- %s\n", insight))
		}
		b.WriteString("\n")
	}

	if len(mc.KnownIssues) > 0 {
		b.WriteString("### Known Issues\n\n")
		b.WriteString("Be aware of the following previously observed problems:\n\n")
		for _, issue := range mc.KnownIssues {
			b.WriteString(fmt.Sprintf("- %s\n", issue))
		}
		b.WriteString("\n")
	}

	return b.String()
}
