package local

import (
	"context"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/unitaryai/osmia/pkg/plugin/ticketing"
)

// SeedTask represents a single task loaded from an optional seed file.
type SeedTask struct {
	ID          string         `yaml:"id"`
	Title       string         `yaml:"title"`
	Description string         `yaml:"description,omitempty"`
	TicketType  string         `yaml:"ticket_type,omitempty"`
	Labels      []string       `yaml:"labels,omitempty"`
	RepoURL     string         `yaml:"repo_url,omitempty"`
	ExternalURL string         `yaml:"external_url,omitempty"`
	Raw         map[string]any `yaml:"raw,omitempty"`
}

func (b *Backend) importSeedFile(ctx context.Context, path string) error {
	if path == "" {
		return nil
	}

	tickets, err := loadSeedTickets(path)
	if err != nil {
		return err
	}

	return b.runInTx(ctx, func(txContext context.Context, tx txRunner) error {
		for _, ticket := range tickets {
			if err := insertTicketIfMissing(txContext, tx, ticket, eventImported); err != nil {
				return err
			}
		}
		return nil
	})
}

func loadSeedTickets(path string) ([]ticketing.Ticket, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading seed file %q: %w", path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}

	var tasks []SeedTask
	if err := yaml.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("parsing seed file %q: %w", path, err)
	}

	seen := make(map[string]struct{}, len(tasks))
	tickets := make([]ticketing.Ticket, 0, len(tasks))
	for _, task := range tasks {
		if task.ID == "" {
			return nil, fmt.Errorf("seed file %q contains a task with an empty id", path)
		}
		if task.Title == "" {
			return nil, fmt.Errorf("seed file %q contains task %q with an empty title", path, task.ID)
		}
		if _, exists := seen[task.ID]; exists {
			return nil, fmt.Errorf("seed file %q contains duplicate ticket id %q", path, task.ID)
		}
		seen[task.ID] = struct{}{}
		tickets = append(tickets, ticketing.Ticket{
			ID:          task.ID,
			Title:       task.Title,
			Description: task.Description,
			TicketType:  task.TicketType,
			Labels:      task.Labels,
			RepoURL:     task.RepoURL,
			ExternalURL: task.ExternalURL,
			Raw:         task.Raw,
		})
	}
	return tickets, nil
}
