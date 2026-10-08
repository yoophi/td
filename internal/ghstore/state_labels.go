package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/marcus/td/internal/models"
)

var stateLabelColors = map[string]string{
	"td:open": "d4c5f9", "td:in_progress": "1d76db", "td:blocked": "b60205",
	"td:in_review": "fbca04", "td:closed": "0e8a16",
}

func isStateLabel(label string) bool {
	_, reserved := stateLabelColors[strings.ToLower(label)]
	return reserved
}

// Only the five exact reserved labels are owned by td. Other td:* labels, and
// all ordinary labels, remain user data. Labels never determine issue status.
func mirroredLabels(labels []string, status models.Status) []string {
	result := make([]string, 0, len(labels)+1)
	for _, label := range labels {
		if !isStateLabel(label) {
			result = append(result, label)
		}
	}
	return append(result, "td:"+string(status))
}

func (r *Record) StateLabelWarning() string {
	if !r.managed {
		return ""
	}
	expected := "td:" + string(r.Status)
	found := []string{}
	for _, label := range r.Labels {
		if isStateLabel(label) {
			found = append(found, label)
		}
	}
	if len(found) == 1 && strings.EqualFold(found[0], expected) {
		return ""
	}
	return fmt.Sprintf("%s: state label differs from authoritative td/native state %s (labels: %v); run 'td config sync-state-labels' to repair", r.ID, r.Status, found)
}

func (c *Client) loadStateLabels(ctx context.Context) error {
	data, err := c.request(ctx, "GET", "/labels?per_page=100", nil, true)
	if err != nil {
		return err
	}
	var pages [][]struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &pages); err != nil {
		return fmt.Errorf("invalid repository labels: %w", err)
	}
	c.stateLabels = map[string]bool{}
	for _, page := range pages {
		for _, label := range page {
			c.stateLabels[strings.ToLower(label.Name)] = true
		}
	}
	return nil
}

func (c *Client) ensureStateLabel(ctx context.Context, label string) error {
	if c.stateLabels == nil {
		if err := c.loadStateLabels(ctx); err != nil {
			return err
		}
	}
	if c.stateLabels[label] {
		return nil
	}
	color, ok := stateLabelColors[label]
	if !ok {
		return fmt.Errorf("invalid td state label %q", label)
	}
	_, err := c.request(ctx, "POST", "/labels", map[string]string{"name": label, "color": color, "description": "td status mirror; metadata is authoritative. Change status using td."}, false)
	// Verify even after an uncertain result: another process may have created
	// it. Never retry the POST, and never write the issue without a usable label.
	if readErr := c.loadStateLabels(ctx); readErr != nil {
		return fmt.Errorf("verify state label %s (issue write not attempted): %w", label, readErr)
	}
	if !c.stateLabels[label] {
		return fmt.Errorf("state label %s is missing (issue write not attempted); check repository label permissions: %v", label, err)
	}
	return nil
}

// SyncStateLabels repairs only td-managed issues, including closed/deleted ones.
// Each repair uses the normal revision checks; earlier repairs remain on error.
func (c *Client) SyncStateLabels(ctx context.Context) (int, error) {
	records, err := c.ListIncludingDeleted(ctx, true)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, record := range records {
		if record.StateLabelWarning() == "" {
			continue
		}
		labels := mirroredLabels(record.Labels, record.Status)
		if _, err := c.UpdateObserved(ctx, &record, Changes{Labels: &labels}); err != nil {
			return count, fmt.Errorf("%d state label repairs saved; %s failed: %w", count, record.ID, err)
		}
		count++
	}
	return count, nil
}

func sameLabels(a, b []string) bool {
	left, right := slices.Clone(a), slices.Clone(b)
	for i := range left {
		left[i] = strings.ToLower(left[i])
	}
	for i := range right {
		right[i] = strings.ToLower(right[i])
	}
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}
