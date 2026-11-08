package linear

import (
	"context"
	"encoding/json"
	"fmt"
)

// Linear's issueAddLabel takes a label ID, so a label is looked up by name on
// the issue's team and the workspace, and created on the team if missing.

const labelLookupQuery = `query($id: String!, $name: String!) {
  issue(id: $id) {
    team {
      id
      labels(filter: { name: { eq: $name } }) { nodes { id isGroup } }
    }
  }
  issueLabels(filter: { name: { eq: $name }, team: { null: true } }) {
    nodes { id isGroup }
  }
}`

const labelCreateMutation = `mutation($teamId: String!, $name: String!) {
  issueLabelCreate(input: { teamId: $teamId, name: $name }) {
    success
    issueLabel { id }
  }
}`

const issueAddLabelMutation = `mutation($id: String!, $labelId: String!) {
  issueAddLabel(id: $id, labelId: $labelId) {
    success
  }
}`

// labelNodes maps a connection of labels.
type labelNodes struct {
	Nodes []struct {
		ID      string `json:"id"`
		IsGroup bool   `json:"isGroup"`
	} `json:"nodes"`
}

// firstApplicable returns the first label ID that is not a label group, or "".
func (l labelNodes) firstApplicable() string {
	for _, n := range l.Nodes {
		if !n.IsGroup {
			return n.ID
		}
	}
	return ""
}

// labelLookupResponse maps the data portion of labelLookupQuery.
type labelLookupResponse struct {
	Issue struct {
		Team struct {
			ID     string     `json:"id"`
			Labels labelNodes `json:"labels"`
		} `json:"team"`
	} `json:"issue"`
	IssueLabels labelNodes `json:"issueLabels"`
}

// addLabel applies the label called name to the issue.
func (b *LinearBackend) addLabel(ctx context.Context, ticketID, name string) error {
	labelID, err := b.labelID(ctx, ticketID, name)
	if err != nil {
		return err
	}

	data, err := b.doGraphQL(ctx, issueAddLabelMutation, map[string]any{"id": ticketID, "labelId": labelID})
	if err != nil {
		return err
	}
	var resp struct {
		IssueAddLabel struct {
			Success bool `json:"success"`
		} `json:"issueAddLabel"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("decoding add label response: %w", err)
	}
	if !resp.IssueAddLabel.Success {
		return fmt.Errorf("linear did not add label %q", name)
	}
	return nil
}

// labelID finds or creates the label called name.
func (b *LinearBackend) labelID(ctx context.Context, ticketID, name string) (string, error) {
	labelID, teamID, err := b.findLabel(ctx, ticketID, name)
	if err != nil || labelID != "" {
		return labelID, err
	}

	created, createErr := b.createLabel(ctx, teamID, name)
	if createErr == nil {
		return created, nil
	}
	// Another run may have just created it.
	if labelID, _, err := b.findLabel(ctx, ticketID, name); err == nil && labelID != "" {
		return labelID, nil
	}
	return "", createErr
}

// findLabel returns the label's ID ("" if none) and the issue's team ID.
func (b *LinearBackend) findLabel(ctx context.Context, ticketID, name string) (labelID, teamID string, err error) {
	data, err := b.doGraphQL(ctx, labelLookupQuery, map[string]any{"id": ticketID, "name": name})
	if err != nil {
		return "", "", fmt.Errorf("looking up label %q: %w", name, err)
	}
	var resp labelLookupResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", "", fmt.Errorf("decoding label lookup response: %w", err)
	}

	team := resp.Issue.Team
	if id := team.Labels.firstApplicable(); id != "" {
		return id, team.ID, nil
	}
	return resp.IssueLabels.firstApplicable(), team.ID, nil
}

// createLabel creates a label called name on the team and returns its ID.
func (b *LinearBackend) createLabel(ctx context.Context, teamID, name string) (string, error) {
	data, err := b.doGraphQL(ctx, labelCreateMutation, map[string]any{"teamId": teamID, "name": name})
	if err != nil {
		return "", fmt.Errorf("creating label %q: %w", name, err)
	}
	var resp struct {
		IssueLabelCreate struct {
			Success    bool `json:"success"`
			IssueLabel struct {
				ID string `json:"id"`
			} `json:"issueLabel"`
		} `json:"issueLabelCreate"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("decoding label create response: %w", err)
	}
	if !resp.IssueLabelCreate.Success || resp.IssueLabelCreate.IssueLabel.ID == "" {
		return "", fmt.Errorf("linear did not create label %q", name)
	}
	return resp.IssueLabelCreate.IssueLabel.ID, nil
}
