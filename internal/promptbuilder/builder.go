// Package promptbuilder constructs prompts for agent execution, assembling
// task descriptions, guard rails, and engine-specific instructions using
// safe template rendering.
package promptbuilder

import (
	"bytes"
	"fmt"
	"os"
	"text/template"

	"github.com/unitaryai/robodev/pkg/engine"
)

// TaskProfile defines constraints and allowed operations for a task type.
type TaskProfile struct {
	AllowedFilePatterns    []string `yaml:"allowed_file_patterns" json:"allowed_file_patterns"`
	BlockedFilePatterns    []string `yaml:"blocked_file_patterns" json:"blocked_file_patterns"`
	BlockedCommands        []string `yaml:"blocked_commands" json:"blocked_commands"`
	MaxCostPerJob          float64  `yaml:"max_cost_per_job" json:"max_cost_per_job"`
	MaxJobDurationMinutes  int      `yaml:"max_job_duration_minutes" json:"max_job_duration_minutes"`
}

// promptData holds the data passed to the prompt template.
// All fields are pre-sanitised; the template does not perform raw
// interpolation of user content to prevent prompt injection.
type promptData struct {
	TaskID          string
	TaskTitle       string
	TaskDescription string
	RepoURL         string
	GuardRails      string
	EngineName      string
	TaskProfile     string
}

const promptTemplate = `# Task

**ID:** {{.TaskID}}
**Title:** {{.TaskTitle}}
**Repository:** {{.RepoURL}}

## Description

{{.TaskDescription}}
{{- if .GuardRails}}

## Guard Rails

{{.GuardRails}}
{{- end}}
{{- if .TaskProfile}}

## Task Profile Constraints

{{.TaskProfile}}
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
Maximum job duration: {{.MaxJobDurationMinutes}} minutes`

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
	}

	var buf bytes.Buffer
	if err := pb.tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing prompt template: %w", err)
	}

	return buf.String(), nil
}

// BuildPromptWithProfile assembles the prompt with an additional task profile
// section injected for the given task type.
func (pb *PromptBuilder) BuildPromptWithProfile(
	task engine.Task,
	guardRailsContent string,
	engineName string,
	taskType string,
	profiles map[string]TaskProfile,
) (string, error) {
	profileContent := ""
	if profile, ok := profiles[taskType]; ok {
		rendered, err := pb.renderProfile(profile)
		if err != nil {
			return "", fmt.Errorf("rendering task profile: %w", err)
		}
		profileContent = rendered
	}

	data := promptData{
		TaskID:          task.ID,
		TaskTitle:       task.Title,
		TaskDescription: task.Description,
		RepoURL:         task.RepoURL,
		GuardRails:      guardRailsContent,
		EngineName:      engineName,
		TaskProfile:     profileContent,
	}

	var buf bytes.Buffer
	if err := pb.tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing prompt template: %w", err)
	}

	return buf.String(), nil
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
