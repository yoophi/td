package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

type actionJournalScope struct {
	Group     *activeActionGroup
	SessionID string
	Carrier   Record
}

// TrackActions assigns the real session without making remote writes. Carrier
// bootstrap is deferred until a validated create/update is ready for its intent.
func (c *Client) TrackActions(sessionID string) error {
	probe := ActionJournal{OperationID: "td-op-check", SessionID: sessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil {
		return err
	}
	if c.journal != nil {
		if c.journal.SessionID != sessionID {
			return fmt.Errorf("journal client already belongs to a different session")
		}
		return nil
	}
	c.journal = &actionJournalScope{SessionID: sessionID}
	return nil
}

// EnableActionJournal opts a client into before/after tracking. A separate
// carrier avoids journal comment writes changing the target issue's clock.
// It never adopts an arbitrary task or chooses between duplicate carriers.
func (c *Client) EnableActionJournal(ctx context.Context, sessionID string) error {
	probe := ActionJournal{OperationID: "td-op-check", SessionID: sessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if err := probe.validate(); err != nil {
		return err
	}
	if c.journal != nil {
		if c.journal.SessionID != sessionID {
			return fmt.Errorf("journal client already belongs to a different session")
		}
		if c.journal.Carrier.ID != "" {
			return nil
		}
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return err
	}
	carriers := []Record{}
	for _, record := range snapshot.records {
		if record.meta.EntityKind == "journal" {
			carriers = append(carriers, record)
		}
	}
	if len(carriers) > 1 {
		return fmt.Errorf("multiple td journal carriers exist; inspect GitHub before enabling action tracking")
	}
	if len(carriers) == 0 {
		operation := "td-op-" + rand.Text()
		body, err := encodeBody("Shared td action intents and verified results. This is a storage carrier, not a task.", metadata{EntityKind: "journal", OperationID: operation, Type: models.TypeTask, Priority: models.PriorityP2})
		if err != nil {
			return err
		}
		data, err := c.request(ctx, "POST", "/issues", map[string]string{"title": "td action journal", "body": body}, false)
		if err != nil {
			return fmt.Errorf("journal carrier creation may be uncertain (operation %s); inspect GitHub before retrying: %w", operation, err)
		}
		created, err := decodeIssue(data)
		if err != nil || created.meta.EntityKind != "journal" || created.meta.OperationID != operation || created.nativeBody != body || created.Title != "td action journal" {
			return fmt.Errorf("journal carrier creation response is unverified for operation %s; inspect GitHub before retrying", operation)
		}
		verified, err := c.journalCarrier(withReadbackCost(ctx), created.ID)
		if err != nil {
			return fmt.Errorf("journal carrier %s readback failed; inspect before retrying: %w", created.ID, err)
		}
		if verified.revision != created.revision {
			return &ConflictError{ID: created.ID, AfterWrite: true}
		}
		// A concurrent bootstrap can create a second carrier. Preserve both
		// and fail explicitly rather than deleting someone else's history.
		snapshot, err = c.ReadSnapshot(WithFreshSnapshot(ctx), false)
		if err != nil {
			return fmt.Errorf("journal carrier was created but uniqueness verification failed: %w", err)
		}
		for _, record := range snapshot.records {
			if record.meta.EntityKind == "journal" {
				carriers = append(carriers, record)
			}
		}
		if len(carriers) != 1 || carriers[0].ID != created.ID {
			return fmt.Errorf("journal carrier bootstrap is ambiguous; inspect GitHub before proceeding")
		}
	}
	carrier, err := c.journalCarrier(ctx, carriers[0].ID)
	if err != nil {
		return err
	}
	if carrier.meta.EntityKind != "journal" {
		return fmt.Errorf("journal carrier identity changed; no task write attempted")
	}
	c.journal = &actionJournalScope{SessionID: sessionID, Carrier: *carrier}
	return nil
}

func (c *Client) journalCarrier(ctx context.Context, id string) (*Record, error) {
	number, err := Number(id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/%d", number), nil, false)
	if err != nil {
		return nil, err
	}
	record, err := decodeIssue(data)
	if err != nil {
		return nil, err
	}
	if record.Number != number || record.meta.EntityKind != "journal" {
		return nil, fmt.Errorf("journal carrier identity changed; no task write attempted")
	}
	record.repository = c.repo
	return record, nil
}

func (c *Client) journalComment(ctx context.Context, path string, payload any, expectedBody, carrierID string) (*ActionJournalRecord, error) {
	method := "GET"
	if payload != nil {
		method = "PATCH"
	}
	data, err := c.request(ctx, method, path, payload, false)
	if err != nil {
		return nil, err
	}
	return c.decodeJournalResponse(data, expectedBody, carrierID)
}

func (c *Client) decodeJournalResponse(data []byte, expectedBody, carrierID string) (*ActionJournalRecord, error) {
	var comment apiComment
	if err := json.Unmarshal(data, &comment); err != nil {
		return nil, fmt.Errorf("journal comment response unreadable: %w", err)
	}
	number, err := c.commentIssueNumber(comment.IssueURL)
	if err != nil || fmt.Sprintf("gh-%d", number) != carrierID || comment.Body != expectedBody || comment.User.Login == "" || comment.CreatedAt.IsZero() || comment.UpdatedAt.IsZero() {
		return nil, fmt.Errorf("journal comment response identity, content or author is unverified")
	}
	return decodeActionJournal(comment, carrierID)
}

func (c *Client) prepareActionJournal(ctx context.Context, before *Record, payload map[string]any, parents []Record) (*ActionJournalRecord, error) {
	if before == nil || before.repository != c.repo || before.ID == c.journal.Carrier.ID {
		return nil, fmt.Errorf("a target observation from this repository is required")
	}
	state, err := actionIssueState(before)
	if err != nil {
		return nil, err
	}
	intent, err := c.plannedUpdateIntent(ctx, before, payload, parents)
	if err != nil {
		return nil, err
	}
	a := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID, IssueID: before.ID,
		CarrierID: c.journal.Carrier.ID, Action: "update", Phase: "prepared", Before: &state, UpdateIntent: intent}
	if err := c.attachActionGroupMember(ctx, &a); err != nil {
		return nil, err
	}
	if err := a.validate(); err != nil {
		return nil, err
	}
	if err := checkUpdateArchiveCapacity(a); err != nil {
		return nil, fmt.Errorf("journal capacity preflight (no target write attempted): %w", err)
	}
	if err := c.EnableActionJournal(ctx, c.journal.SessionID); err != nil {
		return nil, err
	}
	a.CarrierID = c.journal.Carrier.ID
	return c.writePreparedJournal(ctx, a)
}

func (c *Client) prepareCreationJournal(ctx context.Context, operationID string, input ActionCreateInput) (*ActionJournalRecord, error) {
	if err := c.EnableActionJournal(ctx, c.journal.SessionID); err != nil {
		return nil, err
	}
	a := ActionJournal{OperationID: operationID, SessionID: c.journal.SessionID, CarrierID: c.journal.Carrier.ID,
		Action: "create", Phase: "prepared", CreateInput: &input}
	if err := a.validate(); err != nil {
		return nil, err
	}
	// A repeated deterministic operation must be reconciled, not POSTed again.
	// Regular create generates a new identity; manual retries must inspect the
	// identity printed by the failed attempt before issuing a new operation.
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	for _, existing := range history {
		if existing.OperationID == operationID {
			return nil, fmt.Errorf("creation operation %s already has a %s journal (%s); inspect and reconcile it before retrying; no new issue POST attempted", operationID, existing.Phase, existing.ID)
		}
	}
	for _, record := range snapshot.records {
		if record.meta.OperationID == operationID {
			return nil, fmt.Errorf("creation operation %s already identifies %s; inspect it before retrying; no new issue POST attempted", operationID, record.ID)
		}
	}
	return c.writePreparedJournal(ctx, a)
}

func (c *Client) writePreparedJournal(ctx context.Context, a ActionJournal) (*ActionJournalRecord, error) {
	if c.journal == nil || a.Phase != "prepared" || a.SessionID != c.journal.SessionID || a.CarrierID != c.journal.Carrier.ID {
		return nil, fmt.Errorf("prepared intent must belong to this journal scope")
	}
	if _, err := c.journalCarrier(ctx, c.journal.Carrier.ID); err != nil {
		return nil, fmt.Errorf("verify journal carrier before intent (no target write attempted): %w", err)
	}
	body, err := renderWriteActionJournal(a)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/issues/%d/comments", c.journal.Carrier.Number)
	data, err := c.request(ctx, "POST", path, map[string]string{"body": body}, false)
	if err != nil {
		return nil, fmt.Errorf("journal intent %s is uncertain; no target write attempted: %w", a.OperationID, err)
	}
	written, err := c.decodeJournalResponse(data, body, a.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("journal intent %s is unverified; no target write attempted: %w", a.OperationID, err)
	}
	number, err := CommentNumber(written.ID)
	if err != nil {
		return nil, err
	}
	verified, err := c.journalComment(withReadbackCost(ctx), fmt.Sprintf("/issues/comments/%d", number), nil, body, a.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("journal intent %s readback failed; no target write attempted: %w", a.OperationID, err)
	}
	if !sameJournalIdentity(written, verified) {
		return nil, fmt.Errorf("journal intent identity changed; no target write attempted")
	}
	if c.journal.Group != nil && a.GroupOperationID == c.journal.Group.Journal.OperationID {
		if err := validateGroupMember(c.journal.Group.Journal, verified); err != nil {
			return nil, fmt.Errorf("group member intent is unverified (no target write attempted): %w", err)
		}
		c.journal.Group.Attempted[a.IssueID] = true
	}
	return verified, nil
}

func sameJournalIdentity(a, b *ActionJournalRecord) bool {
	return a != nil && b != nil && a.ID == b.ID && strings.EqualFold(a.Author, b.Author) && a.CreatedAt.Equal(b.CreatedAt)
}

func (c *Client) completeActionJournal(ctx context.Context, prepared *ActionJournalRecord, after *Record) error {
	return c.completeActionJournalResult(ctx, prepared, after, nil)
}

func (c *Client) completeActionJournalResult(ctx context.Context, prepared *ActionJournalRecord, after *Record, comment *ActionCommentState) error {
	state, err := actionIssueState(after)
	if err != nil {
		return err
	}
	oldBody, err := renderExistingActionJournal(prepared)
	if err != nil {
		return err
	}
	number, err := CommentNumber(prepared.ID)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/issues/comments/%d", number)
	current, err := c.journalComment(ctx, path, nil, oldBody, prepared.CarrierID)
	if err != nil {
		return err
	}
	if !sameJournalIdentity(prepared, current) || !prepared.UpdatedAt.Equal(current.UpdatedAt) {
		return fmt.Errorf("journal intent changed before receipt write")
	}
	applied := prepared.ActionJournal
	applied.Phase = "applied"
	applied.After = &state
	applied.CommentAfter = comment
	if applied.Action == "create" {
		if prepared.IssueID != "" && prepared.IssueID != after.ID {
			return fmt.Errorf("creation receipt target identity changed")
		}
		if prepared.CreateInput == nil || after.meta.OperationID != prepared.OperationID || state.Title != prepared.CreateInput.Title || state.Body != prepared.CreateInput.Body || state.State != "open" {
			return fmt.Errorf("creation result does not match its recorded operation/input")
		}
		if err := checkLabels(after, prepared.CreateInput.Labels, "created"); err != nil {
			return err
		}
		if err := c.verifyCreationJournalResult(ctx, after, prepared.OperationID); err != nil {
			return err
		}
		applied.IssueID = after.ID
	}
	body, err := renderWriteActionJournal(applied)
	if err != nil {
		return err
	}
	written, err := c.journalComment(ctx, path, map[string]string{"body": body}, body, prepared.CarrierID)
	if err != nil {
		return err
	}
	if !sameJournalIdentity(prepared, written) {
		return fmt.Errorf("journal receipt identity changed")
	}
	verified, err := c.journalComment(withReadbackCost(ctx), path, nil, body, prepared.CarrierID)
	if err != nil {
		return err
	}
	if !sameJournalIdentity(written, verified) || !written.UpdatedAt.Equal(verified.UpdatedAt) {
		return fmt.Errorf("journal receipt changed during verification")
	}
	latest, err := c.readJournalTarget(withReadbackCost(ctx), after.ID)
	if err != nil {
		return err
	}
	if latest.revision != after.revision {
		return &ConflictError{ID: after.ID, AfterWrite: true}
	}
	if c.journal != nil && c.journal.Group != nil && prepared.GroupOperationID == c.journal.Group.Journal.OperationID {
		c.journal.Group.Expected[after.ID] = state
	}
	return nil
}

// A newly created issue can be visible to a point read before GitHub's
// repository collection observes it. Reobserve only absence, never replay POST.
func (c *Client) verifyCreationJournalResult(ctx context.Context, after *Record, operationID string) error {
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt*2) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("creation operation %s observation canceled: %w", operationID, ctx.Err())
			case <-timer.C:
			}
		}
		snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
		if err != nil {
			return fmt.Errorf("creation operation %s uniqueness read failed: %w", operationID, err)
		}
		if _, err := snapshot.ActionHistory(""); err != nil {
			return err
		}
		matches := 0
		for _, record := range snapshot.records {
			if record.meta.OperationID != operationID {
				continue
			}
			matches++
			if record.ID != after.ID || record.revision != after.revision {
				return fmt.Errorf("creation operation %s identifies a changed or duplicate result; inspect GitHub before retrying", operationID)
			}
		}
		if matches == 1 {
			return nil
		}
		if matches > 1 {
			return fmt.Errorf("creation operation %s identifies duplicate results; inspect GitHub before retrying", operationID)
		}
	}
	return fmt.Errorf("creation operation %s has no unique observed result after 3 fresh observations; inspect GitHub before retrying", operationID)
}
