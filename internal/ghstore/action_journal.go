package ghstore

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

const actionJournalPrefix = "<!-- td:action:"
const actionJournalStart = actionJournalPrefix + "v1\n"
const actionCommentJournalStart = actionJournalPrefix + "v2\n"
const actionEntityJournalStart = actionJournalPrefix + "v3\n"
const actionDeletedCommentJournalStart = actionJournalPrefix + "v4\n"
const actionReconciledJournalStart = actionJournalPrefix + "v5\n"
const actionUndoObservedJournalStart = actionJournalPrefix + "v6\n"

// ActionIssueState preserves native values, including the complete metadata
// body. UpdatedAt detects later edits even when content has returned to the
// same value. Revision is a consistency checksum, not a signature or CAS token.
type ActionIssueState struct {
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	Labels    []string  `json:"labels"`
	UpdatedAt time.Time `json:"updated_at"`
	Revision  string    `json:"revision"`
}

func (s ActionIssueState) checksum() (string, error) {
	s.Revision = ""
	s.Labels = slices.Clone(s.Labels)
	slices.Sort(s.Labels)
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func actionIssueState(record *Record) (ActionIssueState, error) {
	if record == nil || (record.nativeState != "open" && record.nativeState != "closed") {
		return ActionIssueState{}, fmt.Errorf("native issue observation is required for action history")
	}
	state := ActionIssueState{Title: record.Title, Body: record.nativeBody, State: record.nativeState,
		Labels: append([]string{}, record.Labels...), UpdatedAt: record.UpdatedAt}
	var err error
	state.Revision, err = state.checksum()
	if err == nil {
		err = state.validate()
	}
	return state, err
}

func (s ActionIssueState) validate() error {
	if s.UpdatedAt.IsZero() {
		return fmt.Errorf("action history requires an observed GitHub update time")
	}
	if s.State != "open" && s.State != "closed" {
		return fmt.Errorf("invalid action native state")
	}
	if s.Labels == nil {
		return fmt.Errorf("action labels must be an array")
	}
	labels := map[string]bool{}
	for _, label := range s.Labels {
		if label == "" || labels[label] {
			return fmt.Errorf("invalid or repeated action label")
		}
		labels[label] = true
	}
	expected, err := s.checksum()
	if err != nil || s.Revision != expected {
		return fmt.Errorf("action state checksum mismatch")
	}
	return nil
}

// ActionJournal is a shared intent/result record. SessionID is attribution,
// not authentication. GitHub supplies the comment author and timestamps.
// Prepared records are never treated as successful or automatically replayed.
type ActionJournal struct {
	NativeUndo      *ActionNativeUndo      `json:"native_undo,omitempty"`
	OperationID     string                 `json:"operation_id"`
	SessionID       string                 `json:"session_id"`
	IssueID         string                 `json:"issue_id"`
	CarrierID       string                 `json:"carrier_id,omitempty"`
	Action          string                 `json:"action"`
	Phase           string                 `json:"phase"`
	Before          *ActionIssueState      `json:"before,omitempty"`
	After           *ActionIssueState      `json:"after,omitempty"`
	Restored        *ActionIssueState      `json:"restored,omitempty"`
	UndoOperationID string                 `json:"undo_operation_id,omitempty"`
	CreateInput     *ActionCreateInput     `json:"create_input,omitempty"`
	CommentInput    *ActionCommentInput    `json:"comment_input,omitempty"`
	CommentAfter    *ActionCommentState    `json:"comment_after,omitempty"`
	CommentBefore   *ActionCommentState    `json:"comment_before,omitempty"`
	UndoObservation *ActionUndoObservation `json:"undo_observation,omitempty"`
	Reconciliation  *ActionReconciliation  `json:"reconciliation,omitempty"`
}

// Creation intent has no allocated issue number or observed server clock yet.
// Its input is separate from verified After state and cannot authorize undo.
type ActionCreateInput struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

type ActionJournalRecord struct {
	ActionJournal
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (a ActionJournal) validate() error {
	identity := func(value string) bool {
		if value == "" {
			return false
		}
		for _, c := range value {
			allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
			if !allowed {
				return false
			}
		}
		return true
	}
	if !identity(a.OperationID) || !identity(a.SessionID) {
		return fmt.Errorf("action history requires operation and session identity")
	}
	pendingCreate := a.Action == "create" && a.Phase == "prepared" && a.IssueID == "" && a.CarrierID != "" && a.CreateInput != nil && a.After == nil
	if !pendingCreate {
		n, err := Number(a.IssueID)
		if err != nil || a.IssueID != fmt.Sprintf("gh-%d", n) {
			return fmt.Errorf("action requires canonical issue identity")
		}
	}
	if a.CreateInput != nil {
		if a.Action != "create" || strings.TrimSpace(a.CreateInput.Title) == "" || a.CreateInput.Labels == nil {
			return fmt.Errorf("creation input requires a create action, title and label array")
		}
		_, meta, managed, err := decodeBody(a.CreateInput.Body)
		if err != nil || !managed || meta.OperationID != a.OperationID || (!slices.Contains([]string{"", "issue", "note", "board"}, meta.EntityKind)) {
			return fmt.Errorf("creation input requires issue metadata with the same operation identity")
		}
		if a.After != nil {
			if a.After.Title != a.CreateInput.Title || a.After.Body != a.CreateInput.Body || a.After.State != "open" {
				return fmt.Errorf("creation after-state differs from its recorded input")
			}
			for _, label := range a.CreateInput.Labels {
				if !slices.ContainsFunc(a.After.Labels, func(actual string) bool { return strings.EqualFold(actual, label) }) {
					return fmt.Errorf("creation after-state omits a requested label")
				}
			}
		}
	}
	if a.CarrierID != "" {
		carrier, err := Number(a.CarrierID)
		if err != nil || a.CarrierID != fmt.Sprintf("gh-%d", carrier) || a.CarrierID == a.IssueID {
			return fmt.Errorf("action requires a distinct canonical journal carrier")
		}
	}
	if !slices.Contains([]string{"create", "update", "delete", "restore", "transition", "relation", "note", "board", "comment_create", "comment_delete"}, a.Action) {
		return fmt.Errorf("unknown journal action %q", a.Action)
	}
	if !slices.Contains([]string{"prepared", "applied", "undo_prepared", "undone"}, a.Phase) {
		return fmt.Errorf("unknown action phase %q", a.Phase)
	}
	if a.Action != "create" && a.Before == nil {
		return fmt.Errorf("action requires before-state")
	}
	if a.Action == "create" && a.Before != nil {
		return fmt.Errorf("creation has no before-state")
	}
	if a.Phase != "prepared" && a.After == nil {
		return fmt.Errorf("verified action requires after-state")
	}
	undo := a.Phase == "undo_prepared" || a.Phase == "undone"
	if undo != (a.UndoOperationID != "") || (undo && !identity(a.UndoOperationID)) || a.UndoOperationID == a.OperationID {
		return fmt.Errorf("undo phase requires a distinct undo identity")
	}
	if (a.Phase == "undone") != (a.Restored != nil) {
		return fmt.Errorf("undone action requires verified restored-state")
	}
	for _, state := range []*ActionIssueState{a.Before, a.After, a.Restored} {
		if state != nil {
			if err := state.validate(); err != nil {
				return err
			}
		}
	}
	if err := a.validateEntityAction(); err != nil {
		return err
	}
	if err := a.validateCommentAction(); err != nil {
		return err
	}
	if err := a.validateUndoObservation(); err != nil {
		return err
	}
	if err := a.validateNativeUndo(); err != nil {
		return err
	}
	return a.validateReconciliation()
}

func renderActionJournal(a ActionJournal) (string, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	target := a.IssueID
	if target == "" {
		target = "new issue"
	}
	start := actionJournalStart
	if a.NativeUndo != nil {
		start = actionNativeUndoJournalStart
	} else if a.UndoObservation != nil {
		start = actionUndoObservedJournalStart
	} else if a.Reconciliation != nil {
		start = actionReconciledJournalStart
	} else if a.Action == "comment_delete" {
		start = actionDeletedCommentJournalStart
	} else if a.Action == "comment_create" {
		start = actionCommentJournalStart
	} else if a.entityKind() != "" {
		start = actionEntityJournalStart
	}
	return fmt.Sprintf("td action %s: %s %s (%s)\n\n%s%s%s", a.OperationID, a.Action, target, a.Phase, start, data, markerEnd), nil
}

func decodeActionJournal(comment apiComment, issueID string) (*ActionJournalRecord, error) {
	index := strings.Index(comment.Body, actionJournalPrefix)
	if index < 0 {
		return nil, nil
	}
	block := strings.TrimSpace(comment.Body[index:])
	start := actionJournalStart
	if strings.HasPrefix(block, actionNativeUndoJournalStart) {
		start = actionNativeUndoJournalStart
	} else if strings.HasPrefix(block, actionUndoObservedJournalStart) {
		start = actionUndoObservedJournalStart
	} else if strings.HasPrefix(block, actionReconciledJournalStart) {
		start = actionReconciledJournalStart
	} else if strings.HasPrefix(block, actionDeletedCommentJournalStart) {
		start = actionDeletedCommentJournalStart
	} else if strings.HasPrefix(block, actionCommentJournalStart) {
		start = actionCommentJournalStart
	} else if strings.HasPrefix(block, actionEntityJournalStart) {
		start = actionEntityJournalStart
	}
	if comment.ID <= 0 || !strings.HasPrefix(block, start) || !strings.HasSuffix(block, markerEnd) {
		return nil, fmt.Errorf("comment %d has unknown or malformed action journal", comment.ID)
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(block, start), markerEnd)
	var a ActionJournal
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&a); err != nil {
		return nil, fmt.Errorf("invalid action journal: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("trailing action journal data")
	}
	if err := a.validate(); err != nil {
		return nil, err
	}
	if (a.NativeUndo != nil) != (start == actionNativeUndoJournalStart) || a.NativeUndo != nil && !strings.EqualFold(a.NativeUndo.Plan.Actor, comment.User.Login) {
		return nil, fmt.Errorf("native undo version or actor differs from native receipt")
	}
	if (a.UndoObservation != nil) != (start == actionUndoObservedJournalStart) || a.UndoObservation != nil && !strings.EqualFold(a.UndoObservation.Actor, comment.User.Login) {
		return nil, fmt.Errorf("undo observation version or native author does not match its payload")
	}
	if (a.Reconciliation != nil) != (start == actionReconciledJournalStart) {
		return nil, fmt.Errorf("reconciliation journal version does not match its payload")
	}
	if a.Reconciliation == nil && a.UndoObservation == nil && a.NativeUndo == nil && (a.entityKind() != "") != (start == actionEntityJournalStart) {
		return nil, fmt.Errorf("entity journal version does not match its payload")
	}
	if a.Reconciliation == nil && a.UndoObservation == nil && a.NativeUndo == nil && (a.Action == "comment_delete") != (start == actionDeletedCommentJournalStart) {
		return nil, fmt.Errorf("deleted comment journal version does not match its payload")
	}
	if a.Reconciliation == nil && a.UndoObservation == nil && a.NativeUndo == nil && (a.Action == "comment_create") != (start == actionCommentJournalStart) {
		return nil, fmt.Errorf("action journal version does not match its payload")
	}
	if a.Reconciliation != nil && !strings.EqualFold(a.Reconciliation.Actor, comment.User.Login) {
		return nil, fmt.Errorf("reconciliation actor differs from native journal author")
	}
	if a.CommentAfter != nil && !strings.EqualFold(a.CommentAfter.Author, comment.User.Login) {
		return nil, fmt.Errorf("comment result author differs from native journal author")
	}
	carrier := a.CarrierID
	if carrier == "" {
		carrier = a.IssueID
	}
	if carrier != issueID {
		return nil, fmt.Errorf("action journal belongs to carrier %s, not %s", carrier, issueID)
	}
	expected, err := renderActionJournal(a)
	if err != nil || expected != comment.Body {
		return nil, fmt.Errorf("comment %d action journal was edited or is noncanonical", comment.ID)
	}
	return &ActionJournalRecord{ActionJournal: a, ID: fmt.Sprintf("ghc-%d", comment.ID), Author: comment.User.Login,
		URL: comment.URL, CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt}, nil
}

// ActionHistory reads all carriers' journals from the existing bulk comments,
// including deleted issues, notes and boards. It performs no network requests.
func (s *Snapshot) ActionHistory(sessionID string) ([]ActionJournalRecord, error) {
	if !s.fullHistory {
		return nil, fmt.Errorf("action history requires complete snapshot comments")
	}
	result := []ActionJournalRecord{}
	seen := map[string]bool{}
	for _, entry := range s.entries {
		for _, comment := range entry.Comments {
			a, err := decodeActionJournal(comment, fmt.Sprintf("gh-%d", entry.Issue.Number))
			if err != nil {
				return nil, err
			}
			if a == nil {
				continue
			}
			if seen[a.OperationID] {
				return nil, fmt.Errorf("duplicate action operation %s", a.OperationID)
			}
			seen[a.OperationID] = true
			if sessionID == "" || a.SessionID == sessionID {
				result = append(result, *a)
			}
		}
	}
	slices.SortFunc(result, func(a, b ActionJournalRecord) int {
		if comparison := b.CreatedAt.Compare(a.CreatedAt); comparison != 0 {
			return comparison
		}
		aNumber, _ := CommentNumber(a.ID)
		bNumber, _ := CommentNumber(b.ID)
		return cmp.Compare(bNumber, aNumber)
	})
	return result, nil
}

// IssueUndoPlan is only a checked inverse, not an executed write. The executor
// must recheck immediately before PATCH and verify the result/receipt afterward.
type IssueUndoPlan struct {
	AlreadyUndone bool
	Payload       map[string]any
}

func (a ActionJournalRecord) PlanIssueUndo(current *Record, sessionID, author string) (*IssueUndoPlan, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	if a.Action == "comment_delete" {
		return nil, &UndoScopeError{Reason: "deleted GitHub comments cannot recover their original ID, author or timestamps; the journal preserves comment_before.body for explicitly posting a new comment; no write attempted"}
	}
	if a.Action == "comment_create" {
		return nil, &UndoScopeError{Reason: "comment undo requires its own guarded executor; no issue PATCH attempted"}
	}
	if sessionID == "" || author == "" || a.SessionID != sessionID || !strings.EqualFold(a.Author, author) {
		return nil, fmt.Errorf("action is not attributed to the current session and GitHub author")
	}
	if current == nil || current.ID != a.IssueID {
		return nil, fmt.Errorf("undo target identity differs from action history")
	}
	state, err := actionIssueState(current)
	if err != nil {
		return nil, err
	}
	if a.Phase == "prepared" || a.Phase == "undo_prepared" {
		return nil, fmt.Errorf("action %s is %s with an unverified outcome; inspect GitHub before any retry", a.OperationID, a.Phase)
	}
	if a.Phase == "undone" {
		if state.Revision != a.Restored.Revision {
			return nil, &ConflictError{ID: a.IssueID}
		}
		return &IssueUndoPlan{AlreadyUndone: true}, nil
	}
	if state.Revision != a.After.Revision {
		return nil, &ConflictError{ID: a.IssueID}
	}
	if a.Action == "create" {
		return nil, fmt.Errorf("%s", a.CreationUndoLimit())
	}
	return &IssueUndoPlan{Payload: map[string]any{"title": a.Before.Title, "body": a.Before.Body,
		"state": a.Before.State, "labels": append([]string{}, a.Before.Labels...)}}, nil
}
