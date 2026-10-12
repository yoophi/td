package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

type BoardChanges struct {
	Name  *string
	Query *string
}

func (c *Client) boardCarrier(ctx context.Context, number int) (*BoardRecord, error) {
	data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/%d", number), nil, false)
	if err != nil {
		return nil, err
	}
	record, err := decodeIssue(data)
	if err != nil {
		return nil, err
	}
	if record.Number != number {
		return nil, fmt.Errorf("board carrier identity mismatch")
	}
	record.repository = c.repo
	return boardRecord(record)
}
func (c *Client) checkBoardName(ctx context.Context, name, except string) error {
	boards, err := c.ListBoards(ctx)
	if err != nil {
		return err
	}
	for _, board := range boards {
		if board.ID != except && strings.EqualFold(board.Name, name) {
			return workflowStateError("board name %q already exists as %s; use a distinct name", name, board.ID)
		}
	}
	return nil
}
func (c *Client) CreateBoard(ctx context.Context, name, expression, actor string) (*BoardRecord, error) {
	return c.createBoard(ctx, name, expression, actor, false)
}

// BoardCreateOptions creates custom board settings and memberships in one
// journaled payload. Source position clocks are portable metadata; native
// authors/server clocks are assigned by GitHub, not copied from the source.
type BoardCreateOptions struct {
	ViewMode  string
	Positions []BoardPosition
	// OperationID pins controller-owned creation; explicit IDs require tracking.
	OperationID string
}

// ValidateBoardCreateOptions shares pure query/mode/membership validation with
// transfer preparation. It performs no repository access or native allocation.
func ValidateBoardCreateOptions(details BoardDetails) error { return details.validate() }

func (c *Client) CreateBoardWithOptions(ctx context.Context, name, expression, actor string, options BoardCreateOptions) (*BoardRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(actor) == "" {
		return nil, &WorkflowInputError{Reason: "board creation requires a name and actual actor"}
	}
	operation, err := c.entityCreationOperation(options.OperationID, "td-board-op-", actor)
	if err != nil {
		return nil, err
	}
	mode := options.ViewMode
	if mode == "" {
		mode = "swimlanes"
	}
	positions := slices.Clone(options.Positions)
	probe := BoardDetails{Version: 1, Query: expression, ViewMode: mode, Positions: positions}
	if err := probe.validate(); err != nil {
		return nil, &WorkflowInputError{Reason: err.Error()}
	}
	if len(positions) > 0 {
		snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
		if err != nil {
			return nil, err
		}
		tasks := map[string]bool{}
		for _, r := range snapshot.records {
			if r.meta.EntityKind == "" || r.meta.EntityKind == "issue" {
				tasks[r.ID] = true
			}
		}
		for _, p := range positions {
			if !tasks[p.IssueID] {
				return nil, &WorkflowInputError{Reason: fmt.Sprintf("board position target %s is not a task in this repository", p.IssueID)}
			}
		}
	}
	return c.createBoardConfigured(ctx, name, expression, actor, false, &boardConfigMutation{reason: "Transferred initial settings", apply: func(d *BoardDetails) error { d.ViewMode = mode; d.Positions = slices.Clone(positions); return nil }}, operation)
}

func (c *Client) createBoard(ctx context.Context, name, expression, actor string, builtin bool) (*BoardRecord, error) {
	return c.createBoardConfigured(ctx, name, expression, actor, builtin, nil, "")
}

// Initial builtin settings belong to the creation payload and its existing
// create journal. There is no intermediate empty carrier or second PATCH.
func (c *Client) createBoardConfigured(ctx context.Context, name, expression, actor string, builtin bool, initial *boardConfigMutation, operation string) (result *BoardRecord, err error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.TrimSpace(actor) == "" {
		return nil, &WorkflowInputError{Reason: "board creation requires a name and actual actor"}
	}
	now := time.Now().UTC()
	if operation == "" {
		operation = "td-board-op-" + rand.Text()
	}
	details := BoardDetails{Version: 1, Builtin: builtin, Query: expression, ViewMode: "swimlanes", History: []BoardEvent{{OperationID: operation, Action: "create", SessionID: actor, At: now}}}
	if initial != nil {
		if err := initial.apply(&details); err != nil {
			return nil, err
		}
		details.History[0].Reason = initial.reason
	}
	if err := details.validate(); err != nil {
		return nil, &WorkflowInputError{Reason: err.Error()}
	}
	except := ""
	if builtin {
		if name != "All Issues" {
			return nil, &WorkflowInputError{Reason: "builtin must be named All Issues"}
		}
		except = "bd-all-issues"
	}
	checkCreation := func() error {
		boards, err := c.ListBoards(ctx)
		if err != nil {
			return err
		}
		for _, b := range boards {
			if builtin && b.IsBuiltin && b.Number > 0 {
				return &ConflictError{ID: "bd-all-issues"}
			}
			if b.ID != except && strings.EqualFold(b.Name, name) {
				return workflowStateError("board name %q already exists as %s; use a distinct name", name, b.ID)
			}
		}
		return nil
	}
	if err := checkCreation(); err != nil {
		return nil, err
	}
	body, err := encodeBody("Board configuration managed by td.", metadata{EntityKind: "board", Board: &details, OperationID: operation, Type: models.TypeTask, Priority: models.PriorityP2})
	if err != nil {
		return nil, err
	}
	journal, err := c.prepareEntityCreationJournal(ctx, operation, name, body, actor)
	if err != nil {
		return nil, err
	}
	if journal != nil {
		defer func() {
			if err != nil {
				err = fmt.Errorf("board creation journal %s (operation %s): %w", journal.ID, operation, err)
			}
		}()
		if err := checkCreation(); err != nil {
			return nil, err
		}
	}
	data, err := c.request(ctx, "POST", "/issues", map[string]any{"title": name, "body": body}, false)
	if err != nil {
		return nil, fmt.Errorf("create board operation %s failed; the write outcome may be unknown, inspect GitHub before retrying: %w", operation, err)
	}
	record, err := decodeIssue(data)
	if err != nil {
		return nil, fmt.Errorf("board creation operation %s response unreadable; inspect GitHub before retrying: %w", operation, err)
	}
	record.repository = c.repo
	board, err := boardRecord(record)
	if err != nil {
		return nil, fmt.Errorf("board creation operation %s returned invalid carrier; inspect GitHub before retrying: %w", operation, err)
	}
	if err := verifyBoardPayload(data, name, body); err != nil {
		return nil, fmt.Errorf("board %s creation was accepted, but confirmation failed; inspect GitHub before retrying: %w", board.ID, err)
	}
	confirmed, err := c.boardCarrier(withReadbackCost(ctx), board.Number)
	if err != nil {
		return nil, fmt.Errorf("board %s was created but verification failed; inspect GitHub before retrying: %w", board.ID, err)
	}
	if confirmed.observed.revision != board.observed.revision {
		return nil, &ConflictError{ID: board.ID, AfterWrite: true}
	}
	if err := c.checkBoardName(ctx, name, board.ID); err != nil {
		return nil, fmt.Errorf("board %s was created, but catalog verification failed; reconcile duplicate carriers before retrying: %w", board.ID, err)
	}
	if err := c.completeEntityJournal(ctx, journal, &board.observed, data); err != nil {
		return nil, err
	}
	return board, nil
}
func verifyBoardPayload(data []byte, name, body string) error {
	var returned struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(data, &returned); err != nil {
		return err
	}
	if returned.Title != name || returned.Body != body {
		return fmt.Errorf("GitHub did not return the requested board title/body")
	}
	return nil
}
func (c *Client) UpdateBoardObserved(ctx context.Context, observed *BoardRecord, changes BoardChanges, actor string) (*BoardRecord, error) {
	if changes.Name == nil && changes.Query == nil {
		return nil, &WorkflowInputError{Reason: "board update requires name or query"}
	}
	return c.writeBoardObserved(ctx, observed, changes, false, actor, "", nil)
}
func (c *Client) DeleteBoardObserved(ctx context.Context, observed *BoardRecord, actor, reason string) (*BoardRecord, error) {
	return c.writeBoardObserved(ctx, observed, BoardChanges{}, true, actor, reason, nil)
}
func (c *Client) writeBoardObserved(ctx context.Context, observed *BoardRecord, changes BoardChanges, deleted bool, actor, reason string, mutation *boardConfigMutation) (*BoardRecord, error) {
	if observed == nil || strings.TrimSpace(actor) == "" {
		return nil, &WorkflowInputError{Reason: "board write requires an observation and actual actor"}
	}
	original := observed.observed
	if observed.Number == 0 {
		if original.repository != c.repo || original.Number != 0 || original.revision != ([32]byte{}) || observed.ID != "bd-all-issues" || !observed.IsBuiltin {
			return nil, &WorkflowInputError{Reason: "builtin configuration requires a virtual observation from this repository"}
		}
		if deleted || changes.Name != nil || changes.Query != nil || mutation == nil {
			return nil, &PolicyError{Reason: "cannot rename, filter or delete the builtin All Issues board"}
		}
		if mutation.action != "set_view_mode" && mutation.action != "set_position" && mutation.action != "move" {
			return nil, workflowStateError("virtual board has no saved configuration; no write attempted")
		}
		// Use canonical defaults, never mutable public observation fields.
		return c.createBoardConfigured(ctx, "All Issues", "", actor, true, mutation, "")
	}
	if original.repository != c.repo || original.revision == ([32]byte{}) || original.Number != observed.Number || original.Number <= 0 {
		return nil, &WorkflowInputError{Reason: "board write requires a persisted observation from this repository"}
	}
	before, err := boardRecord(&original)
	if err != nil {
		return nil, err
	}
	if before.ID != observed.ID {
		return nil, &WorkflowInputError{Reason: "board observation identity is inconsistent"}
	}
	if before.IsBuiltin && (mutation == nil || changes.Name != nil || changes.Query != nil || deleted) {
		return nil, &PolicyError{Reason: "cannot rename, filter or delete the builtin All Issues board"}
	}
	if before.Details.DeletedAt != nil {
		return nil, workflowStateError("board %s is deleted", before.ID)
	}
	name := before.Name
	details := before.Details
	if changes.Name != nil {
		name = strings.TrimSpace(*changes.Name)
		if name == "" {
			return nil, &WorkflowInputError{Reason: "board name must not be empty"}
		}
	}
	if changes.Query != nil {
		details.Query = *changes.Query
	}
	if mutation != nil {
		if err := mutation.apply(&details); err != nil {
			return nil, err
		}
	}
	if err := details.validate(); err != nil {
		return nil, &WorkflowInputError{Reason: err.Error()}
	}
	if name != before.Name {
		if err := c.checkBoardName(ctx, name, before.ID); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	operation := "td-board-op-" + rand.Text()
	action := "update"
	if mutation != nil {
		action = mutation.action
	}
	if deleted {
		details.DeletedAt = &now
		action = "delete"
	}
	details.History = append(details.History, BoardEvent{OperationID: operation, Action: action, SessionID: actor, At: now, Reason: strings.TrimSpace(reason)})
	meta := original.meta
	meta.Board = &details
	meta.OperationID = operation
	body, err := encodeBody(original.Description, meta)
	if err != nil {
		return nil, err
	}
	current, err := c.boardCarrier(ctx, original.Number)
	if err != nil {
		return nil, fmt.Errorf("verify board %s before write (no write attempted): %w", before.ID, err)
	}
	if current.observed.revision != original.revision {
		return nil, &ConflictError{ID: before.ID}
	}
	// Preserve native state, labels and clocks; this writes configuration only.
	payload := map[string]any{"body": body}
	if changes.Name != nil {
		payload["title"] = name
	}
	journal, err := c.prepareEntityJournal(ctx, &original, actor, payload)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "PATCH", fmt.Sprintf("/issues/%d", original.Number), payload, false)
	if err != nil {
		return nil, fmt.Errorf("board %s operation %s failed; the write outcome may be unknown, inspect GitHub before retrying: %w", before.ID, operation, err)
	}
	record, err := decodeIssue(data)
	if err != nil {
		return nil, fmt.Errorf("board %s write response unreadable; inspect GitHub before retrying: %w", before.ID, err)
	}
	if record.Number != original.Number {
		return nil, fmt.Errorf("board %s write returned a different carrier; inspect GitHub before retrying", before.ID)
	}
	record.repository = c.repo
	result, err := boardRecord(record)
	if err != nil {
		return nil, fmt.Errorf("board %s write returned invalid metadata; inspect GitHub before retrying: %w", before.ID, err)
	}
	if err := verifyBoardPayload(data, name, body); err != nil {
		return nil, fmt.Errorf("board %s write was accepted but values differed; inspect GitHub before retrying: %w", before.ID, err)
	}
	confirmed, err := c.boardCarrier(withReadbackCost(ctx), original.Number)
	if err != nil {
		return nil, fmt.Errorf("board %s write was accepted but verification failed; inspect GitHub before retrying: %w", before.ID, err)
	}
	if confirmed.observed.revision != result.observed.revision {
		return nil, &ConflictError{ID: before.ID, AfterWrite: true}
	}
	if !deleted && name != before.Name {
		if err := c.checkBoardName(ctx, name, before.ID); err != nil {
			return nil, fmt.Errorf("board %s rename was saved, but catalog verification failed; inspect before retrying: %w", before.ID, err)
		}
	}
	if err := c.completeEntityJournal(ctx, journal, &result.observed, data); err != nil {
		return nil, err
	}
	return result, nil
}
