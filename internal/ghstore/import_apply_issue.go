package ghstore

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/marcus/td/internal/models"
)

type importRelations struct {
	parent       string
	dependencies []string
}

// Resolve after every destination ID has been allocated, before issue PATCHes.
// Reverse blocks edges are stored on their actual dependent issue.
func importRelationshipProjection(items []ImportItem, mapping map[string]string) (map[string]importRelations, error) {
	if err := ValidateImportItems(items); err != nil {
		return nil, err
	}
	relations := map[string]importRelations{}
	byTarget := map[string]string{}
	for _, source := range items {
		id := mapping[source.SourceKey()]
		n, err := Number(id)
		if err != nil || id != fmt.Sprintf("gh-%d", n) {
			return nil, fmt.Errorf("source %s has no canonical allocated destination ID", source.SourceKey())
		}
		if prior, ok := byTarget[id]; ok {
			return nil, fmt.Errorf("sources %s and %s map to the same destination %s", prior, source.SourceKey(), id)
		}
		byTarget[id] = source.SourceKey()
		relations[source.SourceKey()] = importRelations{}
	}
	for _, source := range items {
		key := source.SourceKey()
		if source.Issue.ParentID != "" {
			r := relations[key]
			r.parent = mapping[source.ReferenceKey(source.Issue.ParentID)]
			relations[key] = r
		}
		for _, edge := range source.Dependencies {
			owner, target := key, mapping[source.ReferenceKey(edge.DependsOnID)]
			if edge.RelationType == "blocks" {
				owner, target = source.ReferenceKey(edge.DependsOnID), mapping[key]
			}
			r := relations[owner]
			if !slices.Contains(r.dependencies, target) {
				r.dependencies = append(r.dependencies, target)
			}
			relations[owner] = r
		}
	}
	for key, r := range relations {
		sort.Strings(r.dependencies)
		relations[key] = r
	}
	return relations, nil
}

func importIssueProjection(source ImportItem, current apiIssue, observed *Record, relations importRelations) (Changes, apiIssue, error) {
	issue := source.Issue
	if issue.ParentID != "" && relations.parent == "" {
		return Changes{}, apiIssue{}, fmt.Errorf("source parent is not mapped")
	}
	details := detailsFromIssue(&issue)
	details.ParentID, details.Dependencies = relations.parent, slices.Clone(relations.dependencies)
	// Workflow actors and approvals remain historical provenance, not live
	// authority in the new repository. Files/dates/status are portable data.
	details.CreatorSession, details.ImplementerSession, details.ReviewerSession = "", "", ""
	details.ReviewRequestedBySession, details.ClosedBySession, details.ReviewedAt = "", "", nil
	details.Files = slices.Clone(source.Files)
	for i := range details.Files {
		details.Files[i].IssueID = observed.ID
	}
	if err := details.validate(); err != nil {
		return Changes{}, apiIssue{}, err
	}
	meta := observed.meta
	meta.Type, meta.Priority, meta.Points, meta.Acceptance = issue.Type, issue.Priority, issue.Points, issue.Acceptance
	meta.LastStateReason = ""
	if source.GitHub != nil {
		_, original, _, err := decodeBody(source.GitHub.Body)
		if err != nil {
			return Changes{}, apiIssue{}, err
		}
		meta.LastStateReason = original.LastStateReason
	}
	meta.Details = &details
	body, err := encodeBody(issue.Description, meta)
	if err != nil {
		return Changes{}, apiIssue{}, err
	}
	labels := mirroredPriorityLabels(mirroredLabels(issue.Labels, issue.Status), issue.Priority)
	status := nativeStatus(issue.Status)
	change := Changes{Title: &issue.Title, Description: &issue.Description, Type: &issue.Type, Priority: &issue.Priority, Points: &issue.Points, Acceptance: &issue.Acceptance, Labels: &labels, Status: &status, Details: &details, Reason: &meta.LastStateReason}
	current.Title, current.Body, current.State = issue.Title, body, string(status)
	current.Labels = nil
	for _, label := range labels {
		current.Labels = append(current.Labels, struct {
			Name string `json:"name"`
		}{Name: label})
	}
	return change, current, nil
}

// Apply only the task's fields/relations. History and completion publication
// follow in the full importer; this stage must never claim a completed import.
func (c *Client) applyImportIssue(ctx context.Context, source ImportItem, row ImportPlanRow, relations importRelations) (*Record, string, bool, error) {
	ctx = withImportRelationObservations(ctx)
	if row.Action == "skip" || row.Action == "resume_history" || row.TargetID == "" || row.SourceKey != source.SourceKey() {
		return nil, "", false, fmt.Errorf("import issue apply requires a mapped non-skipped plan row")
	}
	native, current, err := c.importNativeTarget(ctx, row.TargetID)
	if err != nil {
		return nil, "", false, err
	}
	basis, err := importTargetBasis(native)
	if err != nil {
		return nil, "", false, err
	}
	change, expected, err := importIssueProjection(source, native, current, relations)
	if err != nil {
		return nil, "", false, err
	}
	after, err := importTargetBasis(expected)
	if err != nil {
		return nil, "", false, err
	}
	finalBasis := after
	if row.detaching {
		detachedSource := source
		detachedSource.Issue.ParentID = ""
		change, expected, err = importIssueProjection(detachedSource, native, current, importRelations{})
		if err != nil {
			return nil, "", false, err
		}
		after, err = importTargetBasis(expected)
		if err != nil {
			return nil, "", false, err
		}
	}
	digest, err := source.Digest()
	if err != nil || digest != row.Digest {
		return nil, "", false, fmt.Errorf("source changed since import plan; no issue PATCH attempted")
	}
	if row.intent != nil {
		state, err := row.intent.resumeState(digest, basis)
		if err != nil {
			return nil, "", false, err
		}
		if finalBasis != row.intent.AfterBasis || (row.detaching && after != row.intent.DetachedBasis) {
			return nil, "", false, fmt.Errorf("import projection changed since preparation; inspect mappings before resuming")
		}
		if state == "after_apply" || (row.detaching && state == "after_detach") {
			return current, basis, false, nil
		}
		if !row.detaching && row.intent.DetachedBasis != "" && state == "before_apply" {
			return nil, "", false, fmt.Errorf("prepared import requires graph detachment before final apply")
		}
	} else {
		if row.basis == "" || basis != row.basis {
			return nil, "", false, &ConflictError{ID: row.TargetID}
		}
		if row.Action == "resume" {
			// Creation can finish before preparation exists. Only the exact
			// minimal created issue may enter this stage without an intent.
			skeleton := native
			skeleton.Title, skeleton.State = source.Issue.Title, "open"
			skeleton.Body, err = encodeBody(source.Issue.Description, metadata{OperationID: row.OperationID, Type: source.Issue.Type, Priority: source.Issue.Priority, Points: source.Issue.Points, Acceptance: source.Issue.Acceptance})
			if err != nil {
				return nil, "", false, err
			}
			skeleton.Labels = nil
			for _, label := range mirroredPriorityLabels(mirroredLabels(source.Issue.Labels, models.StatusOpen), source.Issue.Priority) {
				skeleton.Labels = append(skeleton.Labels, struct {
					Name string `json:"name"`
				}{Name: label})
			}
			expected, err := importTargetBasis(skeleton)
			if err != nil || expected != basis {
				return nil, "", false, fmt.Errorf("unprepared imported issue differs from its creation payload; resume with original input or inspect subsequent edits: %w", &ConflictError{ID: row.TargetID})
			}
		}
		intent := importIntent{Version: 1, SourceKey: row.SourceKey, SourceDigest: digest, TargetID: row.TargetID, BeforeBasis: basis, AfterBasis: finalBasis, DestinationArchiveDigest: row.destinationArchiveDigest}
		if row.detaching {
			intent.DetachedBasis = after
		}
		if err := c.prepareImport(ctx, intent); err != nil {
			return nil, "", false, err
		}
	}
	// Intent comments change native clocks. Obtain the checked revision after
	// preparation, while keeping the exact content decision from the plan.
	current, freshBasis, err := c.importTarget(ctx, row.TargetID)
	if err != nil {
		return nil, "", false, err
	}
	if freshBasis != basis {
		return nil, "", false, &ConflictError{ID: row.TargetID}
	}
	if basis == after {
		return current, after, false, nil
	}
	dependencyIDs := relations.dependencies
	if row.detaching {
		dependencyIDs = nil
	}
	dependencies, err := c.dependencyObservations(ctx, current.ID, dependencyIDs)
	if err != nil {
		return nil, "", false, err
	}
	verify := func(afterWrite bool) error {
		verifyCtx := ctx
		if afterWrite {
			verifyCtx = withReadbackCost(ctx)
		}
		for _, previous := range dependencies {
			now, err := c.getRelationObservation(verifyCtx, previous.ID)
			if err != nil {
				return err
			}
			if now.revision != previous.revision {
				return &ConflictError{ID: current.ID, AfterWrite: afterWrite}
			}
		}
		return nil
	}
	if err := verify(false); err != nil {
		return nil, "", false, err
	}
	updated, err := c.UpdateObserved(ctx, current, change)
	if err != nil {
		return nil, "", false, err
	}
	if err := verify(true); err != nil {
		return nil, "", true, err
	}
	_, actual, err := c.importTarget(withReadbackCost(ctx), updated.ID)
	if err != nil {
		return nil, "", true, err
	}
	if actual != after {
		return nil, "", true, &ConflictError{ID: updated.ID, AfterWrite: true}
	}
	return updated, after, true, nil
}
