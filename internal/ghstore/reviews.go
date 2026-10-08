package ghstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"github.com/marcus/td/internal/workflow"
)

// PolicyError distinguishes a denied workflow from a GitHub transport failure.
type PolicyError struct{ Reason string }

func (e *PolicyError) Error() string { return e.Reason }

func ValidateReviewOptions(action string, o TransitionOptions) error {
	for _, field := range []struct{ name, value string }{{"reviewed-by", o.ReviewedBy}, {"admin", o.AdminReason}, {"self-close-exception", o.SelfCloseException}} {
		if field.value != "" && strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("--%s requires a nonblank value", field.name)
		}
	}
	if action != "close" && (o.AdminReason != "" || o.SelfCloseException != "") {
		return fmt.Errorf("close exception flags require close")
	}
	if _, err := reviewpolicy.ParseMode(string(o.Mode)); err != nil {
		return err
	}
	if o.SelfReview && o.Mode != reviewpolicy.ModeTrusted {
		return fmt.Errorf("--self-review requires review_policy_mode=trusted")
	}
	if o.SelfReview && o.ReviewedBy != "" {
		return fmt.Errorf("--self-review and --reviewed-by are mutually exclusive")
	}
	if o.SelfReview && strings.TrimSpace(o.Reason) == "" {
		return fmt.Errorf("--self-review requires --reason")
	}
	if utf8.RuneCountInString(o.ReviewedBy) > reviewpolicy.MaxReviewedByLen || strings.ContainsFunc(o.ReviewedBy, func(r rune) bool { return r == '\n' || r == '\r' || unicode.IsControl(r) && r != '\t' }) {
		return fmt.Errorf("invalid --reviewed-by attribution")
	}
	if o.RecordOnly && o.Mode != reviewpolicy.ModeTrusted && o.Mode != reviewpolicy.ModeDelegated {
		return fmt.Errorf("--record-only requires review_policy_mode=trusted or delegated")
	}
	if o.RecordOnly && strings.TrimSpace(o.Reason) == "" {
		return fmt.Errorf("--record-only requires --reason")
	}
	if o.Decision != "" && o.Decision != reviewpolicy.DecisionApproved && o.Decision != reviewpolicy.DecisionChangesRequested {
		return fmt.Errorf("invalid review decision %q", o.Decision)
	}
	if o.Decision == reviewpolicy.DecisionChangesRequested && !o.RecordOnly {
		return fmt.Errorf("changes_requested requires --record-only")
	}
	if action != "approve" && (o.RecordOnly || o.SelfReview || o.ReviewedBy != "" || o.Decision != "") {
		return fmt.Errorf("review attribution flags require approve")
	}
	return nil
}

func reviewBasis(record *Record, details IssueDetails) string {
	data, _ := json.Marshal(struct {
		Title, Description, Acceptance string
		Type                           models.Type
		Priority                       models.Priority
		Minor                          bool
		Parent, Implementer, Creator   string
		Dependencies                   []string
		Files                          []models.IssueFile
	}{record.Title, record.Description, record.Acceptance, record.Type, record.Priority, details.Minor, details.ParentID, details.ImplementerSession, details.CreatorSession, details.Dependencies, details.Files})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

type nativeStateEvent struct {
	ID    int64  `json:"id"`
	Event string `json:"event"`
}

func stateEventDigest(events []nativeStateEvent) string {
	encoded, _ := json.Marshal(events)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

// Digest native close/reopen events, not clocks or pagination ordering.
func (c *Client) stateEvents(ctx context.Context, id string) (string, error) {
	events, err := c.readStateEvents(ctx, id)
	if err != nil {
		return "", err
	}
	return stateEventDigest(events), nil
}

func (c *Client) readStateEvents(ctx context.Context, id string) ([]nativeStateEvent, error) {
	n, err := Number(id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/%d/events?per_page=100", n), nil, true)
	if err != nil {
		return nil, err
	}
	var pages [][]nativeStateEvent
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid issue events: %w", err)
	}
	events := make([]nativeStateEvent, 0)
	seen := map[int64]bool{}
	for _, page := range pages {
		for _, e := range page {
			if e.Event != "closed" && e.Event != "reopened" {
				continue
			}
			if e.ID <= 0 || seen[e.ID] {
				return nil, fmt.Errorf("invalid or repeated native state event; retry the read")
			}
			seen[e.ID] = true
			events = append(events, e)
		}
	}
	slices.SortFunc(events, func(a, b nativeStateEvent) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return events, nil
}

// A completed approval adds exactly one native close event to the review basis.
// Additional native state changes invalidate repeated approval, even when the
// issue has returned to closed and its body still contains the old reviewer.
func (c *Client) verifyClosedApproval(ctx context.Context, record *Record, details IssueDetails) error {
	if details.ReviewBasis == "" || details.ReviewBasis != reviewBasis(record, details) {
		return workflowStateError("closed approval is stale for %s; reopen and submit a new review cycle", record.ID)
	}
	events, err := c.readStateEvents(ctx, record.ID)
	if err != nil {
		return err
	}
	for i, event := range events {
		if event.Event != "closed" {
			continue
		}
		before := make([]nativeStateEvent, 0, len(events)-1)
		before = append(before, events[:i]...)
		before = append(before, events[i+1:]...)
		if stateEventDigest(before) == details.ReviewEvents {
			return nil
		}
	}
	return workflowStateError("cannot verify completed approval for %s: native close/reopen history changed or is not yet visible; retry the read, or reopen and submit a new review cycle", record.ID)
}

func participation(record *Record, details IssueDetails, session string) (any, implementation, issueImplementation bool) {
	any = record.CreatorSession == session || record.ImplementerSession == session || record.ReviewerSession == session
	implementation = record.ImplementerSession == session
	issueImplementation = record.ImplementerSession != ""
	for _, h := range details.Sessions {
		if h.SessionID == session {
			any = true
		}
		if h.Action == models.ActionSessionStarted || h.Action == models.ActionSessionUnstarted {
			issueImplementation = true
			if h.SessionID == session {
				implementation = true
			}
		}
	}
	return
}

func reviewerEligibility(record *Record, details IssueDetails, o TransitionOptions) reviewpolicy.ReviewerEligibility {
	any, impl, _ := participation(record, details, o.SessionID)
	return reviewpolicy.EvaluateReviewerEligibility(reviewpolicy.ReviewerEligibilityInput{Mode: o.Mode, Issue: &record.Issue, SessionID: o.SessionID, SessionIsCreator: record.CreatorSession == o.SessionID, SessionIsImplementer: record.ImplementerSession == o.SessionID, HasImplementationHistory: impl, WasAnyInvolved: any, SelfReviewAcknowledged: o.SelfReview, AttributedTo: o.ReviewedBy})
}

func supersedeReviews(details *IssueDetails, now time.Time) {
	for i := range details.Reviews {
		if details.Reviews[i].SupersededAt == nil {
			details.Reviews[i].SupersededAt = &now
		}
	}
	details.ReviewerSession = ""
	details.ReviewedAt = nil
}

func (c *Client) reviewHandoff(ctx context.Context, observed *Record, o TransitionOptions) (*Record, *models.Activity, error) {
	entries, err := c.ListActivity(ctx, observed.ID)
	if err != nil {
		return nil, nil, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == "handoff" {
			return observed, &entries[i], nil
		}
	}
	done := []string{}
	decisions := []string{}
	for _, a := range entries {
		if a.Kind == "log" {
			if a.LogType == models.LogTypeDecision {
				decisions = append(decisions, a.Message)
			} else {
				done = append(done, a.Message)
			}
		}
	}
	if len(done) > 8 {
		done = done[len(done)-8:]
	}
	if len(done) == 0 {
		done = []string{"Auto-generated for review submission"}
	}
	created, err := c.AppendActivity(ctx, observed.ID, models.Activity{Kind: "handoff", SessionID: o.SessionID, Done: done, Decisions: decisions})
	if err != nil {
		return nil, nil, err
	}
	refreshed, err := c.Get(ctx, observed.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("handoff %s saved, but re-read failed: %w", created.ID, err)
	}
	// Our comment updates GitHub's timestamp. Permit only that difference.
	left, right := observed.Issue, refreshed.Issue
	left.UpdatedAt = time.Time{}
	right.UpdatedAt = time.Time{}
	before, _ := json.Marshal([]any{left, observed.Details})
	after, _ := json.Marshal([]any{right, refreshed.Details})
	if string(before) != string(after) {
		return nil, nil, fmt.Errorf("handoff %s saved, but issue changed before review: %w", created.ID, &ConflictError{ID: observed.ID})
	}
	return refreshed, created, nil
}

func (c *Client) verifyReview(ctx context.Context, record *Record, details IssueDetails) (string, error) {
	if details.ReviewBasis == "" || details.ReviewBasis != reviewBasis(record, details) {
		return "", workflowStateError("review is stale or missing for %s; start and submit it for review again", record.ID)
	}
	events, err := c.stateEvents(ctx, record.ID)
	if err != nil {
		return "", err
	}
	if events != details.ReviewEvents {
		return "", workflowStateError("native close/reopen history changed for %s; submit a new review cycle", record.ID)
	}
	if details.ReviewHandoffID != "" {
		entries, err := c.ListActivity(ctx, record.ID)
		if err != nil {
			return "", err
		}
		found := false
		for _, a := range entries {
			if a.ID == details.ReviewHandoffID && a.Kind == "handoff" && a.UpdatedAt.Equal(details.ReviewHandoffUpdatedAt) {
				found = true
			}
		}
		if !found {
			return "", workflowStateError("review handoff was edited or deleted for %s; submit a new review cycle", record.ID)
		}
	}
	return events, nil
}

func (c *Client) reviewTransition(ctx context.Context, id, action string, o TransitionOptions) (*Record, bool, error) {
	if err := ValidateReviewOptions(action, o); err != nil {
		return nil, false, err
	}
	observed, err := c.Get(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if o.expectedRevision != nil && observed.revision != *o.expectedRevision {
		return nil, false, &ConflictError{ID: observed.ID}
	}
	d, err := observed.CopyDetails()
	if err != nil {
		return nil, false, err
	}
	from := observed.Status
	target := from
	now := time.Now().UTC()
	checkedEvents := ""
	if action == "approve" || action == "close" {
		if from == models.StatusClosed {
			if action == "approve" && (observed.ReviewerSession == "" || observed.ReviewedAt == nil) {
				return nil, false, workflowStateError("%s is closed without a td approval; native close is not a review", id)
			}
			if action == "approve" {
				if err := c.verifyClosedApproval(ctx, observed, d); err != nil {
					return nil, false, err
				}
			}
			return observed, true, nil
		}
	}
	history := func(session string, action models.IssueSessionAction) {
		if session != "" {
			d.Sessions = append(d.Sessions, models.IssueSessionHistory{ID: "hist-" + rand.Text(), IssueID: observed.ID, SessionID: session, Action: action, CreatedAt: now})
		}
	}
	switch action {
	case "review":
		target = models.StatusInReview
		if !workflow.DefaultMachine().IsValidTransition(from, target) {
			return nil, false, workflowStateError("cannot review %s from %s; start a new review cycle explicitly", id, from)
		}
		if o.Minor {
			d.Minor = true
		}
		if !d.Minor {
			var handoff *models.Activity
			observed, handoff, err = c.reviewHandoff(ctx, observed, o)
			if err != nil {
				return nil, false, err
			}
			d.ReviewHandoffID = handoff.ID
			d.ReviewHandoffUpdatedAt = handoff.UpdatedAt
		} else {
			d.ReviewHandoffID = ""
			d.ReviewHandoffUpdatedAt = time.Time{}
		}
		supersedeReviews(&d, now)
		if observed.ImplementerSession == "" {
			d.ImplementerSession = o.SessionID
			history(o.SessionID, models.ActionSessionStarted)
		} else {
			d.ImplementerSession = observed.ImplementerSession
		}
		d.ReviewRequestedBySession = o.SessionID
		d.ClosedBySession = ""
		checkedEvents, err = c.stateEvents(ctx, id)
		if err != nil {
			return nil, false, err
		}
		d.ReviewBasis = reviewBasis(observed, d)
		d.ReviewEvents = checkedEvents
	case "approve":
		if from != models.StatusInReview {
			return nil, false, workflowStateError("cannot approve %s: status is %s", id, from)
		}
		checkedEvents, err = c.verifyReview(ctx, observed, d)
		if err != nil {
			return nil, false, err
		}
		var active *models.IssueReview
		for i := range d.Reviews {
			if d.Reviews[i].SupersededAt == nil && d.Reviews[i].Decision == reviewpolicy.DecisionApproved {
				active = &d.Reviews[i]
			}
		}
		if active != nil && !o.RecordOnly && (o.Mode == reviewpolicy.ModeTrusted || o.Mode == reviewpolicy.ModeDelegated) {
			if o.ReviewedBy != "" || o.SelfReview {
				return nil, false, fmt.Errorf("existing approval already names a reviewer; omit review attribution when closing on it")
			}
			if active.ReviewerSession != o.SessionID && strings.TrimSpace(o.Reason) == "" {
				return nil, false, fmt.Errorf("closing on another session's approval requires --reason")
			}
			target = models.StatusClosed
			d.ClosedBySession = o.SessionID
			history(o.SessionID, models.ActionSessionClosed)
		} else {
			decision := reviewerEligibility(observed, d, o)
			if !decision.Allowed {
				return nil, false, &PolicyError{Reason: decision.RejectionMessage}
			}
			if decision.RequiresReason && strings.TrimSpace(o.Reason) == "" {
				return nil, false, fmt.Errorf("review approval requires --reason")
			}
			verdict := o.Decision
			if verdict == "" {
				verdict = reviewpolicy.DecisionApproved
			}
			supersedeReviews(&d, now)
			d.Reviews = append(d.Reviews, models.IssueReview{ID: "rv-" + rand.Text(), IssueID: observed.ID, ReviewerSession: o.SessionID, Decision: verdict, Summary: o.Reason, RequestedBySession: d.ReviewRequestedBySession, CreatedAt: now, SelfReview: decision.SelfReview, ReviewedBy: decision.AttributedTo})
			if verdict == reviewpolicy.DecisionApproved {
				d.ReviewerSession = o.SessionID
				d.ReviewedAt = &now
				history(o.SessionID, models.ActionSessionReviewApproved)
			} else {
				history(o.SessionID, models.ActionSessionReviewChangesRequested)
			}
			if !o.RecordOnly {
				target = models.StatusClosed
				d.ClosedBySession = o.SessionID
				history(o.SessionID, models.ActionSessionClosed)
			}
		}
	case "reject":
		if from == models.StatusOpen {
			return observed, true, nil
		}
		if from != models.StatusInReview {
			return nil, false, workflowStateError("cannot reject %s: status is %s", id, from)
		}
		target = models.StatusOpen
		supersedeReviews(&d, now)
		d.ImplementerSession = ""
		d.ReviewRequestedBySession = ""
		d.ClosedBySession = ""
		d.ReviewBasis = ""
		d.Reviews = append(d.Reviews, models.IssueReview{ID: "rv-" + rand.Text(), IssueID: observed.ID, ReviewerSession: o.SessionID, Decision: reviewpolicy.DecisionChangesRequested, Summary: o.Reason, CreatedAt: now})
		history(o.SessionID, models.ActionSessionReviewChangesRequested)
	case "close":
		if from == models.StatusInReview && !observed.Minor {
			return nil, false, workflowStateError("cannot close %s while in review; use approve", id)
		}
		involved, implemented, hasHistory := participation(observed, d, o.SessionID)
		decision := reviewpolicy.EvaluateCloseEligibility(reviewpolicy.CloseEligibilityInput{Mode: reviewpolicy.ModeStrict, Issue: &observed.Issue, SessionID: o.SessionID, SessionIsCreator: observed.CreatorSession == o.SessionID, SessionIsImplementer: observed.ImplementerSession == o.SessionID, HasImplementationHistory: implemented, WasAnyInvolved: involved})
		allowed := decision.Allowed && (!decision.CreatorOpenBypass || !hasHistory)
		if o.Mode == reviewpolicy.ModeDelegated && hasHistory && !observed.Minor {
			allowed = false
		}
		if !allowed && o.AdminReason == "" && o.SelfCloseException == "" {
			return nil, false, &PolicyError{Reason: fmt.Sprintf("cannot close %s without review; use review/approve or an explicit --admin/--self-close-exception reason", id)}
		}
		target = models.StatusClosed
		d.ClosedBySession = o.SessionID
		supersedeReviews(&d, now)
		d.ReviewBasis = ""
		history(o.SessionID, models.ActionSessionClosed)
	case "reopen":
		if from == models.StatusOpen {
			return observed, true, nil
		}
		if from != models.StatusClosed {
			return nil, false, workflowStateError("cannot reopen %s: status is %s", id, from)
		}
		target = models.StatusOpen
		supersedeReviews(&d, now)
		d.ImplementerSession = ""
		d.ReviewRequestedBySession = ""
		d.ClosedBySession = ""
		d.ReviewBasis = ""
	default:
		return nil, false, fmt.Errorf("unsupported review transition %q", action)
	}
	if from != target && !workflow.DefaultMachine().IsValidTransition(from, target) {
		return nil, false, workflowStateError("invalid %s transition from %s to %s", action, from, target)
	}
	d.Status = target
	operation := "td-op-" + rand.Text()
	d.Transitions = append(d.Transitions, TransitionRecord{OperationID: operation, Action: action, From: from, To: target, SessionID: o.SessionID, Reason: o.Reason, AdminReason: o.AdminReason, SelfCloseException: o.SelfCloseException, At: now})
	if checkedEvents != "" {
		current, err := c.stateEvents(ctx, id)
		if err != nil {
			return nil, false, err
		}
		if current != checkedEvents {
			return nil, false, &ConflictError{ID: observed.ID}
		}
	}
	native := nativeStatus(target)
	result, err := c.UpdateObserved(ctx, observed, Changes{Details: &d, Status: &native, Reason: &o.Reason})
	if err != nil {
		return nil, false, fmt.Errorf("%s %s (operation %s): %w", action, id, operation, err)
	}
	return result, false, nil
}
