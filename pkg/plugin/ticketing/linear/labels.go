package linear

import (
	"context"
	"encoding/json"
	"fmt"
)

// Linear's issueAddLabel takes a label ID, not a name, so a label is first
// looked up by name and created if it does not exist. A label may belong to
// the whole workspace or to one team, and only the issue's own team's labels
// and workspace labels can be applied to it.

const labelLookupQuery = `query($id: String!, $name: String!) {
  issue(id: $id) { team { id } }
  issueLabels(filter: { name: { eq: $name } }) {
    nodes { id team { id } }
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

// labelLookupResponse maps the data portion of labelLookupQuery.
type labelLookupResponse struct {
	Issue struct {
		Team struct {
			ID string `json:"id"`
		} `json:"team"`
	} `json:"issue"`
	IssueLabels struct {
		Nodes []struct {
			ID   string `json:"id"`
			Team *struct {
				ID string `json:"id"`
			} `json:"team"`
		} `json:"nodes"`
	} `json:"issueLabels"`
}

// addLabel applies the label called name to the issue, creating it on the
// issue's team if no usable label of that name exists.
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

// labelID returns the ID of a workspace label or a label of the issue's team
// called name, creating a team label if there is none.
func (b *LinearBackend) labelID(ctx context.Context, ticketID, name string) (string, error) {
	data, err := b.doGraphQL(ctx, labelLookupQuery, map[string]any{"id": ticketID, "name": name})
	if err != nil {
		return "", fmt.Errorf("looking up label %q: %w", name, err)
	}
	var resp labelLookupResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("decoding label lookup response: %w", err)
	}

	teamID := resp.Issue.Team.ID
	for _, l := range resp.IssueLabels.Nodes {
		if l.Team == nil || l.Team.ID == teamID {
			return l.ID, nil
		}
	}
	return b.createLabel(ctx, teamID, name)
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
