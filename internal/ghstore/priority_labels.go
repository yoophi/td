package ghstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/models"
)

var priorityLabelColors = map[string]string{
	"td:priority:p0": "b60205", "td:priority:p1": "d93f0b",
	"td:priority:p2": "fbca04", "td:priority:p3": "0e8a16", "td:priority:p4": "c5def5",
}

func priorityLabel(priority models.Priority) string { return "td:priority:" + string(priority) }

func mirroredPriorityLabels(labels []string, priority models.Priority) []string {
	result := make([]string, 0, len(labels)+1)
	for _, label := range labels {
		if _, reserved := priorityLabelColors[strings.ToLower(label)]; !reserved {
			result = append(result, label)
		}
	}
	return append(result, priorityLabel(priority))
}

// Labels remain presentation: legacy writers and external label edits cannot
// override the priority encoded in td metadata.
func (r *Record) PriorityLabelWarning() string {
	if !r.managed {
		return ""
	}
	found := []string{}
	for _, label := range r.Labels {
		if _, reserved := priorityLabelColors[strings.ToLower(label)]; reserved {
			found = append(found, label)
		}
	}
	if len(found) == 1 && strings.EqualFold(found[0], priorityLabel(r.Priority)) {
		return ""
	}
	return fmt.Sprintf("%s: priority labels differ from authoritative metadata %s (labels: %v); preview 'td config sync-priority-labels', then use --apply to repair", r.ID, r.Priority, found)
}

func (c *Client) ensurePriorityLabel(ctx context.Context, priority models.Priority) error {
	label := priorityLabel(priority)
	key := strings.ToLower(label)
	color, valid := priorityLabelColors[key]
	if !valid {
		return fmt.Errorf("invalid td priority %q (issue write not attempted)", priority)
	}
	if c.stateLabels == nil {
		if err := c.loadStateLabels(ctx); err != nil {
			return err
		}
	}
	if c.stateLabels[key] {
		return nil
	}
	_, createErr := c.request(ctx, "POST", "/labels", map[string]string{"name": label, "color": color, "description": "td priority mirror; metadata is authoritative. Change priority using td."}, false)
	// Read back an uncertain creation, never retry the POST. A missing label
	// must stop the operation before writing the issue.
	if err := c.loadStateLabels(ctx); err != nil {
		return fmt.Errorf("verify priority label %s (issue write not attempted): %w", label, err)
	}
	if !c.stateLabels[key] {
		if createErr != nil {
			return fmt.Errorf("priority label %s missing (issue write not attempted); check label permissions: %w", label, createErr)
		}
		return fmt.Errorf("priority label %s missing (issue write not attempted); inspect repository labels", label)
	}
	return nil
}

type PriorityLabelRepair struct {
	ID       string          `json:"id"`
	Priority models.Priority `json:"priority"`
	Before   []string        `json:"before"`
	After    []string        `json:"after"`
}

type PriorityLabelSyncResult struct {
	Apply   bool                  `json:"apply"`
	Applied int                   `json:"applied"`
	Repairs []PriorityLabelRepair `json:"repairs"`
}

// SyncPriorityLabels previews by default. Apply uses the observations from its
// own fresh full listing, freezes every input in a tracked batch, then checks
// revisions before each PATCH. Earlier repairs remain if a later repair fails.
func (c *Client) SyncPriorityLabels(ctx context.Context, apply bool, ids ...string) (*PriorityLabelSyncResult, error) {
	selected := map[string]bool{}
	for _, id := range ids {
		number, err := Number(id)
		if err != nil {
			return nil, err
		}
		selected[fmt.Sprintf("gh-%d", number)] = false
	}
	records, err := c.ListIncludingDeleted(ctx, true)
	if err != nil {
		return nil, err
	}
	result := &PriorityLabelSyncResult{Apply: apply, Repairs: []PriorityLabelRepair{}}
	repairs := []Record{}
	for _, record := range records {
		if len(selected) > 0 {
			if _, exists := selected[record.ID]; !exists {
				continue
			}
			if !record.managed {
				return nil, fmt.Errorf("%s has no td metadata; no priority label repairs attempted", record.ID)
			}
			selected[record.ID] = true
		}
		if record.PriorityLabelWarning() == "" {
			continue
		}
		// Normal writes repair the state mirror too. Show that complete label
		// payload in the preview rather than hiding this existing side effect.
		labels := mirroredPriorityLabels(mirroredLabels(record.Labels, record.Status), record.Priority)
		result.Repairs = append(result.Repairs, PriorityLabelRepair{ID: record.ID, Priority: record.Priority, Before: record.Labels, After: labels})
		repairs = append(repairs, record)
	}
	for id, found := range selected {
		if !found {
			return nil, fmt.Errorf("%s was not found; no priority label repairs attempted", id)
		}
	}
	if !apply {
		return result, nil
	}
	result.Applied, err = c.repairLabels(ctx, repairs, "priority")
	return result, err
}
