package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// All parts come from one fresh paginated collection, never N point reads or a
// reusable UI snapshot. A malformed or foreign page cannot authorize a write.
func (c *Client) readJournalParts(ctx context.Context, carrier string) (*journalPartCollection, error) {
	number, err := Number(carrier)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/%d/comments?per_page=100", number), nil, true)
	if err != nil {
		return nil, err
	}
	var pages [][]apiComment
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid journal part collection: %w", err)
	}
	if pages == nil {
		return nil, fmt.Errorf("journal part collection is not a paginated array")
	}
	comments := []apiComment{}
	for _, page := range pages {
		if page == nil {
			return nil, fmt.Errorf("journal part page is null")
		}
		for _, comment := range page {
			owner, err := c.commentIssueNumber(comment.IssueURL)
			if err != nil || owner != number || comment.ID <= 0 {
				return nil, fmt.Errorf("journal part collection has invalid native ownership")
			}
			comments = append(comments, comment)
		}
	}
	return collectJournalParts(comments)
}

func (c *Client) readJournalReceipt(ctx context.Context, path string, expected *ActionJournalRecord) (*ActionJournalRecord, error) {
	body, err := renderExistingActionJournal(expected)
	if err != nil {
		return nil, err
	}
	return c.journalComment(ctx, path, nil, body, expected.CarrierID)
}

func verifyJournalBeforePublication(old *ActionJournalRecord, plan *journalPublication, collection *journalPartCollection) error {
	if old != nil {
		number, err := CommentNumber(old.ID)
		if err != nil {
			return err
		}
		native, found := collection.byID[number]
		if !found {
			return fmt.Errorf("original journal disappeared during multipart publication")
		}
		current, err := decodeJournalFromCollection(native, plan.carrierID, collection)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, old) {
			return fmt.Errorf("original journal changed during multipart publication")
		}
		return nil
	}
	for _, native := range collection.byID {
		record, err := decodeJournalFromCollection(native, plan.carrierID, collection)
		if err != nil {
			return err
		}
		if record != nil && record.OperationID == plan.operationID {
			return fmt.Errorf("journal operation %s already exists as %s; inspect/reconcile it before retrying", plan.operationID, record.ID)
		}
	}
	return nil
}

func selectJournalUploadParts(plan *journalPublication, collection *journalPartCollection, actor string) ([]apiComment, error) {
	selected := make([]apiComment, len(plan.partBodies))
	for id, part := range collection.parts {
		if part.Transfer != plan.manifest.Transfer {
			continue
		}
		native := collection.byID[id]
		if part.Index < 0 || part.Index >= len(selected) || selected[part.Index].ID != 0 || part.Count != len(selected) || native.Body != plan.partBodies[part.Index] || !validJournalPartNative(native) || !journalNativeCarrierMatches(native, plan.carrierID) || !strings.EqualFold(native.User.Login, actor) {
			return nil, fmt.Errorf("multipart upload %s has duplicate, changed or foreign parts; inspect before retrying", plan.operationID)
		}
		selected[part.Index] = native
	}
	return selected, nil
}

// publishJournal is the sole native journal publication seam. Large records
// upload immutable parts first and publish the manifest last. Re-entering after
// an explicit recovery may reuse exact observed parts; no failed POST is retried
// in this call and no target issue/comment is replayed here.
// Success returns the receipt verified by a fresh native readback, not just the
// write response. Callers may consume it immediately within this publication
// boundary; subsequent writes must still reobserve their original receipts.
func (c *Client) publishJournal(ctx context.Context, path string, plan *journalPublication, old *ActionJournalRecord, fence func() error) (*ActionJournalRecord, error) {
	if active := creationContext(ctx); active != nil && active.journal != nil {
		if err := c.verifyCreationReceipt(ctx, active.journal); err != nil {
			return nil, err
		}
	}
	if plan == nil {
		return nil, fmt.Errorf("journal publication plan is required")
	}
	if plan.importArchive != nil {
		return c.publishImportArchiveJournal(ctx, path, plan, old, fence)
	}
	carrier, err := Number(plan.carrierID)
	if err != nil {
		return nil, err
	}
	expectedPath := fmt.Sprintf("/issues/%d/comments", carrier)
	method := "POST"
	if old != nil {
		number, err := CommentNumber(old.ID)
		if err != nil {
			return nil, err
		}
		expectedPath = fmt.Sprintf("/issues/comments/%d", number)
		method = "PATCH"
		if old.OperationID != plan.operationID || old.CarrierID != plan.carrierID {
			return nil, fmt.Errorf("journal publication cannot change operation or carrier")
		}
	}
	if path != expectedPath {
		return nil, fmt.Errorf("journal publication endpoint differs from its native identity")
	}
	body := plan.body
	if len(plan.partBodies) > 0 {
		profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
		if err != nil {
			return nil, err
		}
		var actor struct{ Login string }
		if err := json.Unmarshal(profile, &actor); err != nil || actor.Login == "" {
			return nil, fmt.Errorf("multipart publication actor is unverified")
		}
		if old != nil && !strings.EqualFold(old.Author, actor.Login) {
			return nil, fmt.Errorf("multipart journal publication requires its original native author")
		}
		collection, err := c.readJournalParts(ctx, plan.carrierID)
		if err != nil {
			return nil, err
		}
		if err := verifyJournalBeforePublication(old, plan, collection); err != nil {
			return nil, err
		}
		parts, err := selectJournalUploadParts(plan, collection, actor.Login)
		if err != nil {
			return nil, err
		}
		carrier, _ := Number(plan.carrierID)
		for i := range parts {
			if parts[i].ID != 0 {
				continue
			}
			data, err := c.request(ctx, "POST", fmt.Sprintf("/issues/%d/comments", carrier), map[string]string{"body": plan.partBodies[i]}, false)
			if err != nil {
				return nil, fmt.Errorf("multipart journal %s part %d upload is uncertain; no automatic retry or target write from this publication: %w", plan.operationID, i+1, err)
			}
			var part apiComment
			if err := json.Unmarshal(data, &part); err != nil {
				return nil, fmt.Errorf("multipart part response is unverified: %w", err)
			}
			owner, err := c.commentIssueNumber(part.IssueURL)
			if err != nil || owner != carrier || !validJournalPartNative(part) || !strings.EqualFold(part.User.Login, actor.Login) || part.Body != plan.partBodies[i] {
				return nil, fmt.Errorf("multipart journal %s part %d response identity, body or author is unverified", plan.operationID, i+1)
			}
			parts[i] = part
		}
		// This single collection verifies every POST and detects concurrent edits,
		// duplicate uploads and deletion. Root publication is forbidden until then.
		collection, err = c.readJournalParts(withReadbackCost(ctx), plan.carrierID)
		if err != nil {
			return nil, err
		}
		if err := verifyJournalBeforePublication(old, plan, collection); err != nil {
			return nil, err
		}
		verified, err := selectJournalUploadParts(plan, collection, actor.Login)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(parts, verified) {
			return nil, fmt.Errorf("multipart journal uploaded parts changed or are absent from readback")
		}
		body, err = plan.bindParts(verified)
		if err != nil {
			return nil, err
		}
		if _, err := c.journalCarrier(ctx, plan.carrierID); err != nil {
			return nil, err
		}
		if fence != nil {
			if err := fence(); err != nil {
				return nil, err
			}
		}
	}
	data, err := c.request(ctx, method, path, map[string]string{"body": body}, false)
	if err != nil {
		return nil, err
	}
	written, err := c.decodeJournalResponse(withReadbackCost(ctx), data, body, plan.carrierID)
	if err != nil {
		return nil, err
	}
	if journalNeedsCollection(body) {
		// Decoding a reference manifest already reads the complete native carrier
		// collection and compares its root with the response before resolving all
		// referenced parts. That verified collection is the readback for this
		// publication; it is not retained as permission for a subsequent write.
		return written, nil
	}
	number, err := CommentNumber(written.ID)
	if err != nil {
		return nil, err
	}
	verified, err := c.readJournalReceipt(withReadbackCost(ctx), fmt.Sprintf("/issues/comments/%d", number), written)
	if err != nil {
		return nil, fmt.Errorf("journal publication readback failed: %w", err)
	}
	if !reflect.DeepEqual(written, verified) {
		return nil, fmt.Errorf("journal publication changed during readback")
	}
	return verified, nil
}
