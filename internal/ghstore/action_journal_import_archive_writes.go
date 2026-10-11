package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

func selectImportArchiveParts(p *importArchivePublication, collection *journalPartCollection, actor string) ([]apiComment, error) {
	selected := make([]apiComment, len(p.parts))
	nativeByTransfer := make(map[string]apiComment, len(collection.parts))
	for id, part := range collection.parts {
		nativeByTransfer[part.Transfer] = collection.byID[id]
	}
	for n, expected := range p.parts {
		matches := collection.byTransfer[expected.Transfer]
		if len(matches) == 0 {
			continue
		}
		if len(matches) != 1 || *matches[0] != expected {
			return nil, fmt.Errorf("import archive upload has duplicate or changed chunks")
		}
		native := nativeByTransfer[expected.Transfer]
		if !validJournalPartNative(native) || !journalNativeCarrierMatches(native, expected.CarrierID) || !strings.EqualFold(native.User.Login, actor) || native.Body != p.bodies[n] {
			return nil, fmt.Errorf("import archive upload has changed or foreign native evidence")
		}
		selected[n] = native
	}
	return selected, nil
}

// Publication shares the existing native journal parts, collection reader and
// root publisher. It only stages immutable documents; it performs no import
// target allocation, mutation, resume or compensation.
func (c *Client) publishImportArchiveJournal(ctx context.Context, path string, plan *journalPublication, old *ActionJournalRecord, fence func() error) (*ActionJournalRecord, error) {
	if plan.importArchive.manifest.Journal.ImportCommand == nil || !strings.EqualFold(plan.importArchive.manifest.Journal.ImportCommand.Repository, c.repo) {
		return nil, fmt.Errorf("import archive publication requires its original repository")
	}
	carrier, err := Number(plan.carrierID)
	if err != nil {
		return nil, err
	}
	expectedPath := fmt.Sprintf("/issues/%d/comments", carrier)
	if old != nil {
		number, err := CommentNumber(old.ID)
		if err != nil {
			return nil, err
		}
		expectedPath = fmt.Sprintf("/issues/comments/%d", number)
		if old.OperationID != plan.operationID || old.CarrierID != plan.carrierID {
			return nil, fmt.Errorf("import archive publication cannot change operation or carrier")
		}
		digest, err := importOriginalDigest(old.ImportCommand)
		if err != nil || old.ImportCommand == nil || old.SessionID != plan.importArchive.manifest.Journal.SessionID || digest != plan.importArchive.originalDigest {
			return nil, fmt.Errorf("import archive rewrite cannot replace the original session, input, mapping or before-state")
		}
	}
	if path != expectedPath {
		return nil, fmt.Errorf("import archive publication endpoint differs from its native identity")
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct{ Login string }
	if err := json.Unmarshal(profile, &actor); err != nil || actor.Login == "" {
		return nil, fmt.Errorf("import archive publisher is unverified")
	}
	if old != nil && !strings.EqualFold(old.Author, actor.Login) {
		return nil, fmt.Errorf("import archive publication requires its original native author")
	}
	collection, err := c.readJournalParts(ctx, plan.carrierID)
	if err != nil {
		return nil, err
	}
	if err := verifyJournalBeforePublication(old, plan, collection); err != nil {
		return nil, err
	}
	p := plan.importArchive
	parts, err := selectImportArchiveParts(p, collection, actor.Login)
	if err != nil {
		return nil, err
	}
	for n := range parts {
		if parts[n].ID != 0 {
			continue
		}
		data, err := c.request(ctx, "POST", fmt.Sprintf("/issues/%d/comments", carrier), map[string]string{"body": p.bodies[n]}, false)
		if err != nil {
			return nil, fmt.Errorf("import archive %s chunk %d upload is uncertain; no automatic retry or import target write: %w", plan.operationID, n+1, err)
		}
		var part apiComment
		if err := json.Unmarshal(data, &part); err != nil {
			return nil, fmt.Errorf("import archive response is unreadable: %w", err)
		}
		owner, err := c.commentIssueNumber(part.IssueURL)
		if err != nil || owner != carrier || !validJournalPartNative(part) || !strings.EqualFold(part.User.Login, actor.Login) || part.Body != p.bodies[n] {
			return nil, fmt.Errorf("import archive %s chunk %d response is unverified", plan.operationID, n+1)
		}
		parts[n] = part
	}
	collection, err = c.readJournalParts(withReadbackCost(ctx), plan.carrierID)
	if err != nil {
		return nil, err
	}
	if err := verifyJournalBeforePublication(old, plan, collection); err != nil {
		return nil, err
	}
	verified, err := selectImportArchiveParts(p, collection, actor.Login)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(parts, verified) {
		return nil, fmt.Errorf("import archive chunks changed or disappeared during verification")
	}
	root, err := p.bindParts(verified)
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
	return c.publishJournal(ctx, path, root, old, fence)
}
