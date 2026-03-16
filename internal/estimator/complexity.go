// Package estimator provides predictive cost and duration estimation for
// tasks based on multi-dimensional complexity scoring and k-nearest-neighbour
// lookup from historical outcomes.
package estimator

import (
	"context"
	"strings"
)

// ComplexityInput holds the raw attributes of a task from which complexity
// is computed.
type ComplexityInput struct {
	TaskDescription string   `json:"task_description"`
	TaskType        string   `json:"task_type"`
	RepoURL         string   `json:"repo_url"`
	RepoSize        int      `json:"repo_size"` // number of files
	Labels          []string `json:"labels"`
}

// ComplexityScore is the computed multi-dimensional complexity assessment
// of a task. Overall is a weighted average of the individual dimensions,
// all normalised to the range [0, 1].
type ComplexityScore struct {
	Overall    float64            `json:"overall"`
	Dimensions map[string]float64 `json:"dimensions"`
}

// Dimension names used in complexity scoring.
const (
	DimDescriptionComplexity = "description_complexity"
	DimLabelComplexity       = "label_complexity"
	DimRepoSize              = "repo_size"
	DimTaskTypeComplexity    = "task_type_complexity"
)

// Default dimension weights used when computing the overall score.
var defaultWeights = map[string]float64{
	DimDescriptionComplexity: 0.3,
	DimLabelComplexity:       0.15,
	DimRepoSize:              0.25,
	DimTaskTypeComplexity:    0.3,
}

// ComplexityScorer computes multi-dimensional complexity scores for tasks.
type ComplexityScorer struct{}

// NewComplexityScorer creates a new scorer.
func NewComplexityScorer() *ComplexityScorer {
	return &ComplexityScorer{}
}

// Score computes the complexity of a task across multiple dimensions and
// returns a combined score.
func (cs *ComplexityScorer) Score(_ context.Context, input ComplexityInput) (*ComplexityScore, error) {
	dims := map[string]float64{
		DimDescriptionComplexity: scoreDescriptionComplexity(input.TaskDescription),
		DimLabelComplexity:       scoreLabelComplexity(input.Labels),
		DimRepoSize:              scoreRepoSize(input.RepoSize),
		DimTaskTypeComplexity:    scoreTaskType(input.TaskType),
	}

	overall := 0.0
	totalWeight := 0.0
	for dim, val := range dims {
		w := defaultWeights[dim]
		overall += val * w
		totalWeight += w
	}
	if totalWeight > 0 {
		overall /= totalWeight
	}

	return &ComplexityScore{
		Overall:    overall,
		Dimensions: dims,
	}, nil
}

// scoreDescriptionComplexity estimates complexity from the task description
// based on length, number of requirements (bullet points), and presence of
// technical terms.
func scoreDescriptionComplexity(desc string) float64 {
	if desc == "" {
		return 0.2
	}

	score := 0.0

	// Length factor: longer descriptions tend to indicate more complex tasks.
	wordCount := len(strings.Fields(desc))
	switch {
	case wordCount < 20:
		score += 0.1
	case wordCount < 50:
		score += 0.3
	case wordCount < 150:
		score += 0.6
	default:
		score += 0.9
	}

	// Bullet/requirement count: lines starting with - or * suggest multiple requirements.
	requirements := 0
	for _, line := range strings.Split(desc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "• ") {
			requirements++
		}
	}
	switch {
	case requirements > 5:
		score += 0.1
	case requirements > 2:
		score += 0.05
	}

	if score > 1.0 {
		score = 1.0
	}
	return score
}

// scoreLabelComplexity maps known labels to complexity levels.
func scoreLabelComplexity(labels []string) float64 {
	labelScores := map[string]float64{
		"typo-fix":     0.1,
		"docs":         0.15,
		"bug":          0.3,
		"bug_fix":      0.3,
		"enhancement":  0.5,
		"feature":      0.6,
		"refactor":     0.65,
		"security":     0.7,
		"architecture": 0.85,
		"migration":    0.8,
	}

	maxScore := 0.3 // default if no known labels match
	for _, l := range labels {
		lower := strings.ToLower(l)
		if s, ok := labelScores[lower]; ok && s > maxScore {
			maxScore = s
		}
	}
	return maxScore
}

// scoreRepoSize normalises file count into a 0-1 complexity factor.
func scoreRepoSize(fileCount int) float64 {
	switch {
	case fileCount <= 100:
		return 0.2
	case fileCount <= 500:
		return 0.4
	case fileCount <= 1000:
		return 0.5
	case fileCount <= 5000:
		return 0.8
	default:
		return 1.0
	}
}

// scoreTaskType maps task types to base complexity.
func scoreTaskType(taskType string) float64 {
	typeScores := map[string]float64{
		"typo_fix":     0.1,
		"docs":         0.15,
		"bug_fix":      0.35,
		"test":         0.3,
		"enhancement":  0.5,
		"new_feature":  0.6,
		"refactor":     0.65,
		"security_fix": 0.7,
		"migration":    0.8,
		"architecture": 0.9,
	}

	if s, ok := typeScores[strings.ToLower(taskType)]; ok {
		return s
	}
	return 0.4 // default for unknown types
}
