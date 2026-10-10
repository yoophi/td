package ghstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Comment input has no allocated GitHub identity. Native identities and clocks
// belong only in the read-back state, never in caller-supplied activity data.
type ActionCommentInput struct {
	Body string `json:"body"`
}

type ActionCommentState struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issue_id"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Revision  string    `json:"revision"`
}

func (s ActionCommentState) checksum() (string, error) {
	s.Revision = ""
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (s ActionCommentState) validate() error {
	number, err := CommentNumber(s.ID)
	if err != nil || s.ID != fmt.Sprintf("ghc-%d", number) {
		return fmt.Errorf("comment state requires canonical comment identity")
	}
	n, err := Number(s.IssueID)
	if err != nil || s.IssueID != fmt.Sprintf("gh-%d", n) {
		return fmt.Errorf("comment state requires canonical issue membership")
	}
	if strings.TrimSpace(s.Author) == "" || s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return fmt.Errorf("comment state requires native author and ordered clocks")
	}
	expected, err := s.checksum()
	if err != nil || expected != s.Revision {
		return fmt.Errorf("comment state checksum mismatch")
	}
	return nil
}

func (a ActionJournal) validateCommentAction() error {
	if a.Action != "comment_create" {
		if a.CommentInput != nil || a.CommentAfter != nil {
			return fmt.Errorf("issue actions cannot carry comment state")
		}
		return nil
	}
	if a.CarrierID == "" || a.CommentInput == nil || a.CreateInput != nil {
		return fmt.Errorf("comment creation requires a separate carrier and recorded input")
	}
	if strings.Contains(a.CommentInput.Body, actionJournalPrefix) || (a.Phase == "prepared" && a.After != nil) {
		return fmt.Errorf("comment intent cannot contain a nested journal or an unverified result")
	}
	// Decode the exact raw activity, including its canonical visible text. A
	// journal cannot fabricate attribution through a detached JSON field.
	activity, err := decodeActivity(apiComment{ID: 1, Body: a.CommentInput.Body}, a.IssueID)
	if err != nil || activity.Native || activity.Kind == "action_journal" || activity.OperationID != a.OperationID || activity.SessionID != a.SessionID {
		return fmt.Errorf("comment input requires canonical activity with matching operation/session")
	}
	if (a.Phase != "prepared") != (a.CommentAfter != nil) {
		return fmt.Errorf("verified comment creation requires a native comment result")
	}
	if a.CommentAfter != nil {
		if err := a.CommentAfter.validate(); err != nil {
			return err
		}
		if a.CommentAfter.IssueID != a.IssueID || a.CommentAfter.Body != a.CommentInput.Body {
			return fmt.Errorf("comment result differs from target or recorded input")
		}
		if !sameActionContents(*a.Before, *a.After) || a.After.UpdatedAt.Before(a.Before.UpdatedAt) {
			return fmt.Errorf("comment creation cannot change issue fields or reverse its clock")
		}
	}
	if a.Restored != nil && (!sameActionContents(*a.Before, *a.Restored) || a.Restored.UpdatedAt.Before(a.After.UpdatedAt)) {
		return fmt.Errorf("comment inverse cannot change issue fields or reverse its clock")
	}
	return nil
}
