// Package memory implements cross-task episodic memory with a temporal
// knowledge graph. It enables the controller to accumulate knowledge from
// task executions—success patterns, failure modes, engine capabilities—and
// inject relevant context into future prompts.
package memory

import (
	"time"
)

// NodeType identifies the kind of knowledge node stored in the graph.
type NodeType string

const (
	// NodeTypeFact represents an atomic piece of knowledge extracted
	// from a task execution (e.g. a failure reason, engine capability).
	NodeTypeFact NodeType = "fact"
	// NodeTypePattern represents a recurring behavioural pattern
	// observed across multiple task executions.
	NodeTypePattern NodeType = "pattern"
	// NodeTypeEngineProfile represents aggregated performance data
	// for a specific execution engine.
	NodeTypeEngineProfile NodeType = "engine_profile"
)

// FactType classifies the nature of a Fact node.
type FactType string

const (
	// FactTypeRepoBehaviour describes observed repository-specific behaviour.
	FactTypeRepoBehaviour FactType = "repo_behaviour"
	// FactTypeEngineCapability describes what an engine can or cannot do.
	FactTypeEngineCapability FactType = "engine_capability"
	// FactTypeFailurePattern captures a known failure mode.
	FactTypeFailurePattern FactType = "failure_pattern"
	// FactTypeSuccessPattern captures conditions that led to success.
	FactTypeSuccessPattern FactType = "success_pattern"
)

// Relation describes the semantic relationship between two nodes.
type Relation string

const (
	// RelationRelatesTo indicates a general association between nodes.
	RelationRelatesTo Relation = "relates_to"
	// RelationContradicts indicates conflicting information between nodes.
	RelationContradicts Relation = "contradicts"
	// RelationSupersedes indicates that one node replaces another.
	RelationSupersedes Relation = "supersedes"
)

// Node is the interface implemented by all knowledge graph node types.
type Node interface {
	// NodeID returns the unique identifier for this node.
	NodeID() string
	// NodeType returns the type discriminator for this node.
	NodeType() NodeType
	// GetConfidence returns the current confidence score (0.0–1.0).
	GetConfidence() float64
	// SetConfidence updates the confidence score.
	SetConfidence(c float64)
	// GetDecayRate returns the per-interval decay multiplier.
	GetDecayRate() float64
	// GetTenantID returns the tenant isolation identifier.
	GetTenantID() string
	// GetValidFrom returns when this node became valid.
	GetValidFrom() time.Time
}

// Fact represents an atomic piece of knowledge extracted from a task execution.
type Fact struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`
	Source     string    `json:"source"` // task_run_id
	FactKind   FactType  `json:"fact_type"`
	ValidFrom  time.Time `json:"valid_from"`
	Confidence float64   `json:"confidence"`
	DecayRate  float64   `json:"decay_rate"`
	TenantID   string    `json:"tenant_id"`
}

// NodeID returns the unique identifier for this Fact.
func (f *Fact) NodeID() string { return f.ID }

// NodeType returns NodeTypeFact.
func (f *Fact) NodeType() NodeType { return NodeTypeFact }

// GetConfidence returns the current confidence score.
func (f *Fact) GetConfidence() float64 { return f.Confidence }

// SetConfidence updates the confidence score.
func (f *Fact) SetConfidence(c float64) { f.Confidence = c }

// GetDecayRate returns the per-interval decay multiplier.
func (f *Fact) GetDecayRate() float64 { return f.DecayRate }

// GetTenantID returns the tenant isolation identifier.
func (f *Fact) GetTenantID() string { return f.TenantID }

// GetValidFrom returns when this fact became valid.
func (f *Fact) GetValidFrom() time.Time { return f.ValidFrom }

// Pattern represents a recurring behavioural pattern observed across tasks.
type Pattern struct {
	ID           string    `json:"id"`
	Description  string    `json:"description"`
	Occurrences  int       `json:"occurrences"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	RelatedFacts []string  `json:"related_facts"`
	Confidence   float64   `json:"confidence"`
	DecayRate    float64   `json:"decay_rate"`
	TenantID     string    `json:"tenant_id"`
}

// NodeID returns the unique identifier for this Pattern.
func (p *Pattern) NodeID() string { return p.ID }

// NodeType returns NodeTypePattern.
func (p *Pattern) NodeType() NodeType { return NodeTypePattern }

// GetConfidence returns the current confidence score.
func (p *Pattern) GetConfidence() float64 { return p.Confidence }

// SetConfidence updates the confidence score.
func (p *Pattern) SetConfidence(c float64) { p.Confidence = c }

// GetDecayRate returns the per-interval decay multiplier.
func (p *Pattern) GetDecayRate() float64 { return p.DecayRate }

// GetTenantID returns the tenant isolation identifier.
func (p *Pattern) GetTenantID() string { return p.TenantID }

// GetValidFrom returns when this pattern was first seen.
func (p *Pattern) GetValidFrom() time.Time { return p.FirstSeen }

// EngineProfile represents aggregated performance data for an engine.
type EngineProfile struct {
	ID          string             `json:"id"`
	EngineName  string             `json:"engine_name"`
	SuccessRate map[string]float64 `json:"success_rate"` // keyed by task type
	Strengths   []string           `json:"strengths"`
	Weaknesses  []string           `json:"weaknesses"`
	Confidence  float64            `json:"confidence"`
	DecayRate   float64            `json:"decay_rate"`
	TenantID    string             `json:"tenant_id"`
	ValidFrom   time.Time          `json:"valid_from"`
}

// NodeID returns the unique identifier for this EngineProfile.
func (ep *EngineProfile) NodeID() string { return ep.ID }

// NodeType returns NodeTypeEngineProfile.
func (ep *EngineProfile) NodeType() NodeType { return NodeTypeEngineProfile }

// GetConfidence returns the current confidence score.
func (ep *EngineProfile) GetConfidence() float64 { return ep.Confidence }

// SetConfidence updates the confidence score.
func (ep *EngineProfile) SetConfidence(c float64) { ep.Confidence = c }

// GetDecayRate returns the per-interval decay multiplier.
func (ep *EngineProfile) GetDecayRate() float64 { return ep.DecayRate }

// GetTenantID returns the tenant isolation identifier.
func (ep *EngineProfile) GetTenantID() string { return ep.TenantID }

// GetValidFrom returns when this profile became valid.
func (ep *EngineProfile) GetValidFrom() time.Time { return ep.ValidFrom }

// Edge represents a directed relationship between two nodes in the graph.
type Edge struct {
	FromID    string    `json:"from_id"`
	ToID      string    `json:"to_id"`
	Relation  Relation  `json:"relation"`
	Weight    float64   `json:"weight"`
	CreatedAt time.Time `json:"created_at"`
}

// GraphQuery describes the parameters for querying the knowledge graph.
type GraphQuery struct {
	TaskDescription string `json:"task_description,omitempty"`
	RepoURL         string `json:"repo_url,omitempty"`
	Engine          string `json:"engine,omitempty"`
	TenantID        string `json:"tenant_id"`
	MaxResults      int    `json:"max_results,omitempty"`
}
