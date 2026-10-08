package ghstore

import (
	"context"
	"fmt"
	"slices"
)

// parentObservations validates the complete proposed ancestor chain. The
// observations are checked again around the child write, without claiming an
// atomic transaction across issues. Clearing a parent needs no ancestor reads.
func (c *Client) parentObservations(ctx context.Context, child, parent string) ([]Record, error) {
	observations := []Record{}
	seen := map[string]bool{}
	if child != "" {
		seen[child] = true
	}
	for parent != "" {
		n, err := Number(parent)
		if err != nil || parent != fmt.Sprintf("gh-%d", n) {
			return nil, fmt.Errorf("parent %q must be a canonical gh-N ID", parent)
		}
		if seen[parent] {
			return nil, fmt.Errorf("parent relationship creates or enters a cycle at %s", parent)
		}
		seen[parent] = true
		record, err := c.Get(ctx, parent)
		if err != nil {
			return nil, fmt.Errorf("read parent %s: %w", parent, err)
		}
		observations = append(observations, *record)
		parent = record.ParentID
	}
	return observations, nil
}

func (c *Client) verifyParentObservations(ctx context.Context, child string, observations []Record, after bool) error {
	for _, previous := range observations {
		current, err := c.Get(ctx, previous.ID)
		if err != nil {
			return fmt.Errorf("verify parent %s for %s (child write attempted=%v): %w", previous.ID, child, after, err)
		}
		if current.revision != previous.revision {
			return &ConflictError{ID: child, AfterWrite: after}
		}
	}
	return nil
}

func descendantsFromRecords(records []Record, root string) ([]Record, error) {
	byID := map[string]Record{}
	children := map[string][]Record{}
	for _, record := range records {
		if _, exists := byID[record.ID]; exists {
			return nil, fmt.Errorf("hierarchy pagination repeated %s; retry the read", record.ID)
		}
		byID[record.ID] = record
		if record.ParentID != "" {
			children[record.ParentID] = append(children[record.ParentID], record)
		}
	}
	if _, exists := byID[root]; !exists {
		return nil, fmt.Errorf("hierarchy root %s is missing or deleted", root)
	}
	for parent := range children {
		slices.SortFunc(children[parent], func(a, b Record) int { return a.Number - b.Number })
	}
	result := []Record{}
	queue := []string{root}
	seen := map[string]bool{root: true}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if seen[child.ID] {
				return nil, fmt.Errorf("hierarchy cycle at %s", child.ID)
			}
			seen[child.ID] = true
			result = append(result, child)
			queue = append(queue, child.ID)
		}
	}
	return result, nil
}

func (c *Client) Descendants(ctx context.Context, id string) ([]Record, error) {
	n, err := Number(id)
	if err != nil {
		return nil, err
	}
	records, err := c.List(ctx, true)
	if err != nil {
		return nil, err
	}
	return descendantsFromRecords(records, fmt.Sprintf("gh-%d", n))
}
