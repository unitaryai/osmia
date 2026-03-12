// Package promptbuilder constructs prompts for agent execution, assembling
// task descriptions, guard rails, and engine-specific instructions using
// safe template rendering.
package promptbuilder

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/template"

	"github.com/unitaryai/osmia/pkg/engine"
)

// TaskProfile defines constraints and allowed operations for a task type.
type TaskProfile struct {
	AllowedFilePatterns   []string `yaml:"allowed_file_patterns" json:"allowed_file_patterns"`
	BlockedFilePatterns   []string `yaml:"blocked_file_patterns" json:"blocked_file_patterns"`
	BlockedCommands       []string `yaml:"blocked_commands" json:"blocked_commands"`
	MaxCostPerJob         float64  `yaml:"max_cost_per_job" json:"max_cost_per_job"`
	MaxJobDurationMinutes int      `yaml:"max_job_duration_minutes" json:"max_job_duration_minutes"`
	ToolWhitelist         []string `yaml:"tool_whitelist" json:"tool_whitelist"`
	ToolBlacklist         []string `yaml:"tool_blacklist" json:"tool_blacklist"`
	Workflow              string   `yaml:"workflow" json:"workflow"`
}

// TeamAgent describes an agent participating in team coordination.
type TeamAgent struct {
	Name string
	Role string
}

// promptData holds the data passed to the prompt template.
// All fields are pre-sanitised; the template does not perform raw
// interpolation of user content to prevent prompt injection.
type promptData struct {
	TaskID               string
	TaskTitle            string
	TaskDescription      string
	RepoURL              string
	WorkflowInstructions string
	GuardRails           string
	EngineName           string
	TaskProfile          string
	TeamCoordination     string
	MemoryContext        string
}

const promptTemplate = `# Task

**ID:** {{.TaskID}}
**Title:** {{.TaskTitle}}
**Repository:** {{.RepoURL}}

## Description

{{.TaskDescription}}
{{- if .WorkflowInstructions}}

{{.WorkflowInstructions}}
{{- end}}
{{- if .GuardRails}}

## Guard Rails

{{.GuardRails}}
{{- end}}
{{- if .TaskProfile}}

## Task Profile Constraints

{{.TaskProfile}}
{{- end}}
{{- if .TeamCoordination}}

## Team Coordination

{{.TeamCoordination}}
{{- end}}
{{- if .MemoryContext}}

{{.MemoryContext}}
{{- end}}
{{- if .EngineName}}

## Engine

Running on engine: {{.EngineName}}
{{- end}}
`

const taskProfileTemplate = `Allowed file patterns: {{range .AllowedFilePatterns}}{{.}}, {{end}}
Blocked file patterns: {{range .BlockedFilePatterns}}{{.}}, {{end}}
Blocked commands: {{range .BlockedCommands}}{{.}}, {{end}}
Maximum cost per job: ${{printf "%.2f" .MaxCostPerJob}}
Maximum job duration: {{.MaxJobDurationMinutes}} minutes
{{- if .ToolWhitelist}}
Allowed tools: {{range .ToolWhitelist}}{{.}}, {{end}}
{{- end}}
{{- if .ToolBlacklist}}
Blocked tools: {{range .ToolBlacklist}}{{.}}, {{end}}
{{- end}}`

// PromptBuilder assembles prompts from task data, guard rails content,
// and engine-specific instructions.
type PromptBuilder struct {
	tmpl        *template.Template
	profileTmpl *template.Template
}

// New creates a new PromptBuilder. Templates are parsed once at construction
// time and reused for each prompt build.
func New() (*PromptBuilder, error) {
	tmpl, err := template.New("prompt").Parse(promptTemplate)
	if err != nil {
		return nil, fmt.Errorf("parsing prompt template: %w", err)
	}

	profileTmpl, err := template.New("profile").Parse(taskProfileTemplate)
	if err != nil {
		return nil, fmt.Errorf("parsing task profile template: %w", err)
	}

	return &PromptBuilder{
		tmpl:        tmpl,
		profileTmpl: profileTmpl,
	}, nil
}

// BuildPrompt assembles the final prompt string from task data, guard rails
// content, and engine name. The template uses structured fields rather than
// raw string interpolation to prevent prompt injection from adversarial
// content in task descriptions or file names.
func (pb *PromptBuilder) BuildPrompt(task engine.Task, guardRailsContent string, engineName string) (string, error) {
	data := promptData{
		TaskID:          task.ID,
		TaskTitle:       task.Title,
		TaskDescription: task.Description,
		RepoURL:         task.RepoURL,
		GuardRails:      guardRailsContent,
		EngineName:      engineName,
		MemoryContext:   task.MemoryContext,
	}

	var buf bytes.Buffer
	if err := pb.tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing prompt template: %w", err)
	}

	return buf.String(), nil
}

// BuildPromptWithProfile assembles the prompt with an additional task profile
// section injected for the given task type. If the profile specifies a
// workflow mode, the corresponding workflow instructions are included
// after the task description but before guard rails.
func (pb *PromptBuilder) BuildPromptWithProfile(
	task engine.Task,
	guardRailsContent string,
	engineName string,
	taskType string,
	profiles map[string]TaskProfile,
) (string, error) {
	profileContent := ""
	workflowContent := ""
	if profile, ok := profiles[taskType]; ok {
		rendered, err := pb.renderProfile(profile)
		if err != nil {
			return "", fmt.Errorf("rendering task profile: %w", err)
		}
		profileContent = rendered
		workflowContent = WorkflowInstructions(profile.Workflow)
	}

	data := promptData{
		TaskID:               task.ID,
		TaskTitle:            task.Title,
		TaskDescription:      task.Description,
		RepoURL:              task.RepoURL,
		WorkflowInstructions: workflowContent,
		GuardRails:           guardRailsContent,
		EngineName:           engineName,
		TaskProfile:          profileContent,
		MemoryContext:        task.MemoryContext,
	}

	var buf bytes.Buffer
	if err := pb.tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing prompt template: %w", err)
	}

	return buf.String(), nil
}

const tddWorkflowInstructions = `## Workflow: Test-Driven Development

Follow this strict workflow order:
1. Run the existing test suite to establish a baseline. Note any failing tests.
2. Write a failing test that captures the requirements of this task.
3. Implement the minimum code needed to make the new test pass.
4. Run the full test suite to verify no regressions.
5. Refactor if needed, keeping all tests green.
6. Report the final test results in your output.`

const reviewFirstWorkflowInstructions = `## Workflow: Review First

Follow this workflow order:
1. Read and understand all relevant code before making any changes.
2. Identify the root cause or the correct location for changes.
3. Write a summary of your findings and proposed approach.
4. Implement the changes.
5. Verify the changes work as expected.`

// WorkflowInstructions returns structured workflow instructions for the given
// workflow mode. Recognised values are "tdd" and "review-first". An empty
// string or unrecognised value returns an empty string, allowing callers to
// treat unknown workflows gracefully.
func WorkflowInstructions(workflow string) string {
	switch workflow {
	case "tdd":
		return tddWorkflowInstructions
	case "review-first":
		return reviewFirstWorkflowInstructions
	default:
		return ""
	}
}

// BuildPromptWithTeams assembles the prompt with task profile and team
// coordination sections. When agents are provided, a "## Team Coordination"
// section is appended describing the available agents and their roles.
func (pb *PromptBuilder) BuildPromptWithTeams(
	task engine.Task,
	guardRailsContent string,
	engineName string,
	taskType string,
	profiles map[string]TaskProfile,
	agents []TeamAgent,
) (string, error) {
	profileContent := ""
	workflowContent := ""
	if profile, ok := profiles[taskType]; ok {
		rendered, err := pb.renderProfile(profile)
		if err != nil {
			return "", fmt.Errorf("rendering task profile: %w", err)
		}
		profileContent = rendered
		workflowContent = WorkflowInstructions(profile.Workflow)
	}

	data := promptData{
		TaskID:               task.ID,
		TaskTitle:            task.Title,
		TaskDescription:      task.Description,
		RepoURL:              task.RepoURL,
		WorkflowInstructions: workflowContent,
		GuardRails:           guardRailsContent,
		EngineName:           engineName,
		TaskProfile:          profileContent,
		TeamCoordination:     TeamCoordinationSection(agents),
		MemoryContext:        task.MemoryContext,
	}

	var buf bytes.Buffer
	if err := pb.tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing prompt template: %w", err)
	}

	return buf.String(), nil
}

// TeamCoordinationSection generates the team coordination prompt section
// describing available agents and their roles. Returns an empty string
// when no agents are provided.
func TeamCoordinationSection(agents []TeamAgent) string {
	if len(agents) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("This task will be executed by a team of specialised agents. ")
	b.WriteString("Decompose the work across the following agents:\n\n")

	// Sort agents by name for deterministic output.
	sorted := make([]TeamAgent, len(agents))
	copy(sorted, agents)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	for _, agent := range sorted {
		b.WriteString("- **")
		b.WriteString(agent.Name)
		b.WriteString("**: ")
		b.WriteString(agent.Role)
		b.WriteString("\n")
	}

	b.WriteString("\nCoordinate between agents to ensure the task is completed efficiently. ")
	b.WriteString("Each agent should focus on its designated role.")

	return b.String()
}

// LoadGuardRails reads guard rails content from the given file path.
func LoadGuardRails(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading guard rails file: %w", err)
	}
	return string(data), nil
}

// renderProfile renders a TaskProfile into a human-readable string using
// a safe template.
func (pb *PromptBuilder) renderProfile(profile TaskProfile) (string, error) {
	var buf bytes.Buffer
	if err := pb.profileTmpl.Execute(&buf, profile); err != nil {
		return "", fmt.Errorf("executing profile template: %w", err)
	}
	return buf.String(), nil
}
