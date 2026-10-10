package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func (a ActionJournal) entityKind() string {
	var body string
	if a.CreateInput != nil {
		body = a.CreateInput.Body
	} else if a.Before != nil {
		body = a.Before.Body
	}
	_, meta, _, err := decodeBody(body)
	if err == nil && (meta.EntityKind == "note" || meta.EntityKind == "board") {
		return meta.EntityKind
	}
	return ""
}

func (a ActionJournal) validateEntityAction() error {
	kind := a.entityKind()
	if kind == "" {
		if a.Action == "note" || a.Action == "board" {
			return fmt.Errorf("entity action requires matching native entity metadata")
		}
		return nil
	}
	if a.Action != "create" && a.Action != kind {
		return fmt.Errorf("entity action differs from recorded metadata")
	}
	for _, state := range []*ActionIssueState{a.After, a.Restored} {
		if state == nil {
			continue
		}
		_, meta, _, err := decodeBody(state.Body)
		if err != nil || meta.EntityKind != kind {
			return fmt.Errorf("entity after/restored state changes its kind")
		}
		if a.Before != nil {
			labelsA, labelsB := slices.Clone(a.Before.Labels), slices.Clone(state.Labels)
			slices.Sort(labelsA)
			slices.Sort(labelsB)
			if state.State != a.Before.State || !slices.Equal(labelsA, labelsB) || state.UpdatedAt.Before(a.Before.UpdatedAt) {
				return fmt.Errorf("entity configuration write changed native state/labels or reversed its clock")
			}
		}
	}
	return nil
}

// Journal execution reads auxiliary native carriers without making them tasks.
func (c *Client) readJournalTarget(ctx context.Context, id string) (*Record, error) {
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
	if record.Number != number || !slices.Contains([]string{"", "issue", "note", "board"}, record.meta.EntityKind) {
		return nil, fmt.Errorf("journal target identity or kind is invalid")
	}
	record.repository = c.repo
	return record, nil
}

func (c *Client) prepareEntityJournal(ctx context.Context, before *Record, actor string, payload map[string]any) (*ActionJournalRecord, error) {
	if c.journal == nil {
		return nil, nil
	}
	if before == nil || before.repository != c.repo || !slices.Contains([]string{"note", "board"}, before.meta.EntityKind) || actor != c.journal.SessionID {
		return nil, fmt.Errorf("entity observation or actor differs from journal scope; no target write attempted")
	}
	state, err := actionIssueState(before)
	if err != nil {
		return nil, err
	}
	input, err := c.plannedUpdateIntent(ctx, before, payload, nil)
	if err != nil {
		return nil, err
	}
	intent := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: actor, IssueID: before.ID, CarrierID: c.journal.Carrier.ID, Action: before.meta.EntityKind, Phase: "prepared", Before: &state, UpdateIntent: input}
	if err := intent.validate(); err != nil {
		return nil, err
	}
	if err := checkUpdateArchiveCapacity(intent); err != nil {
		return nil, fmt.Errorf("journal capacity preflight (no target write attempted): %w", err)
	}
	if err := c.EnableActionJournal(ctx, actor); err != nil {
		return nil, err
	}
	intent.CarrierID = c.journal.Carrier.ID
	prepared, err := c.writePreparedJournal(ctx, intent)
	if err != nil {
		return nil, err
	}
	latest, err := c.readJournalTarget(ctx, before.ID)
	if err != nil {
		return nil, err
	}
	if latest.revision != before.revision {
		return nil, &ConflictError{ID: before.ID}
	}
	return prepared, nil
}

func (c *Client) prepareEntityCreationJournal(ctx context.Context, operation, title, body, actor string) (*ActionJournalRecord, error) {
	if c.journal == nil {
		return nil, nil
	}
	if actor != c.journal.SessionID {
		return nil, fmt.Errorf("entity actor differs from journal scope; no target POST attempted")
	}
	return c.prepareCreationJournal(ctx, operation, ActionCreateInput{Title: title, Body: body, Labels: []string{}})
}

func (c *Client) completeEntityJournal(ctx context.Context, prepared *ActionJournalRecord, after *Record, data []byte) error {
	if prepared == nil {
		return nil
	}
	if prepared.Action == "create" {
		var returned apiIssue
		if err := json.Unmarshal(data, &returned); err != nil {
			return err
		}
		if !strings.EqualFold(returned.User.Login, prepared.Author) {
			return fmt.Errorf("native entity creator differs from intent author")
		}
	}
	if err := c.completeActionJournal(ctx, prepared, after); err != nil {
		return fmt.Errorf("entity %s receipt %s is unverified; inspect journal %s before retrying: %w", after.ID, prepared.OperationID, prepared.ID, err)
	}
	return nil
}

// EntityIdentity keeps auxiliary carriers distinct from tasks in history output.
func (a ActionJournal) EntityIdentity() (string, string) {
	kind := a.entityKind()
	if kind == "" {
		return "issue", a.IssueID
	}
	if a.IssueID == "" {
		return kind, "new " + kind
	}
	number, err := Number(a.IssueID)
	if err != nil {
		return kind, a.IssueID
	}
	if kind == "note" {
		return kind, fmt.Sprintf("nt-gh-%d", number)
	}
	body := ""
	if a.Before != nil {
		body = a.Before.Body
	} else if a.CreateInput != nil {
		body = a.CreateInput.Body
	}
	_, meta, _, _ := decodeBody(body)
	if meta.Board != nil && meta.Board.Builtin {
		return kind, "bd-all-issues"
	}
	return kind, fmt.Sprintf("bd-gh-%d", number)
}

// CreationUndoLimit preserves native identity; compensation is explicit.
func (a ActionJournal) CreationUndoLimit() string {
	kind, id := a.EntityIdentity()
	if kind == "board" && id == "bd-all-issues" {
		return "builtin board materialization cannot be erased safely; select another board or explicitly adjust its settings while retaining the carrier identity/history"
	}
	command := "td delete " + id
	if kind == "note" {
		command = "td note delete " + id
	}
	if kind == "board" {
		command = "td board delete " + id
	}
	return fmt.Sprintf("creation cannot be erased safely; use explicit logical delete (%s) to preserve GitHub identity and history", command)
}
