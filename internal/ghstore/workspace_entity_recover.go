package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type EntityOperationRecovery struct {
	ID        string `json:"id"`
	JournalID string `json:"journal_id"`
	Proof     string `json:"proof"`
}

// RecoverEntityOperation binds the original durable mutation intent to a native
// note/board journal. It may repair only that journal, never POST an entity or
// replay its PATCH. Absence returns an error, not permission to allocate again.
func (c *Client) RecoverEntityOperation(ctx context.Context, request MutationRequest) (*EntityOperationRecovery, error) {
	if c == nil || c.journal == nil || c.journal.Group != nil || request.OperationID == "" || request.EntityKind != "note" && request.EntityKind != "board" || !importDigestPattern.MatchString(request.PayloadSHA256) {
		return nil, fmt.Errorf("entity recovery requires its original request and independent actor; no write attempted")
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct{ Login string }
	if err := json.Unmarshal(profile, &actor); err != nil {
		return nil, err
	}
	if actor.Login == "" {
		return nil, fmt.Errorf("entity recovery requires the actual GitHub author")
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	var original *ActionJournalRecord
	for i := range history {
		root := &history[i]
		if root.OperationID != request.OperationID {
			continue
		}
		if original != nil {
			return nil, fmt.Errorf("entity recovery operation has duplicate roots; no target write attempted")
		}
		if root.SessionID != c.journal.SessionID || !strings.EqualFold(root.Author, actor.Login) || root.Phase != "prepared" && root.Phase != "applied" || root.CreationCommand != nil || root.Group != nil {
			return nil, fmt.Errorf("entity recovery root differs from original actor/phase; no target write attempted")
		}
		var title, body, method, path string
		if root.Action == "create" && root.CreateInput != nil {
			title, body, method, path = root.CreateInput.Title, root.CreateInput.Body, "POST", "repos/"+c.repo+"/issues"
		} else if root.Action == "board" && root.UpdateIntent != nil && root.Before != nil {
			title, body, method, path = root.UpdateIntent.Target.Title, root.UpdateIntent.Target.Body, "PATCH", "repos/"+c.repo+"/issues/"+strings.TrimPrefix(root.IssueID, "gh-")
		} else {
			return nil, fmt.Errorf("entity recovery action is unsupported; no target write attempted")
		}
		payload, err := json.Marshal(map[string]any{"title": title, "body": body})
		if err != nil {
			return nil, err
		}
		if expected := mutationRequest(method, path, payload); expected != request {
			return nil, fmt.Errorf("entity recovery journal differs from recorded request bytes/kind/operation; no target write attempted")
		}
		original = root
	}
	if original == nil {
		return nil, fmt.Errorf("original entity operation not observed; no POST or PATCH replayed")
	}
	proof := "verified"
	if original.Phase == "prepared" {
		result, err := c.ReconcileAction(ctx, original.ID, c.journal.SessionID, true)
		if err != nil {
			return nil, err
		}
		if result.TargetWriteReplayed || result.Partial {
			return nil, fmt.Errorf("entity recovery did not observe a complete no-replay result")
		}
		original = result.Journal
	}
	if original.Reconciliation != nil || original.UpdateObservation != nil {
		proof = "observed"
	}
	ids, err := c.ObservedEntityOperationMappings(ctx, map[string]string{request.OperationID: request.EntityKind})
	if err != nil {
		return nil, err
	}
	return &EntityOperationRecovery{ID: ids[request.OperationID], JournalID: original.ID, Proof: proof}, nil
}
