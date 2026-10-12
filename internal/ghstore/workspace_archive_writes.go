package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PublishWorkspaceArchive publishes only passive provenance, not task/note/board
// changes. It requires an enabled, verified journal carrier matching the pure
// plan. It never retries a POST, replays a partial upload, or adopts prior chunks.
func (c *Client) PublishWorkspaceArchive(ctx context.Context, plan *WorkspaceArchivePlan) (*WorkspaceArchiveEvidence, error) {
	return c.publishWorkspaceArchive(ctx, plan, false)
}

// ResumeWorkspaceArchive reuses only freshly verified immutable chunks/root.
// A durable transfer recorder must reject every previously attempted POST;
// missing native data alone never authorizes replay of an uncertain upload.
func (c *Client) ResumeWorkspaceArchive(ctx context.Context, plan *WorkspaceArchivePlan) (*WorkspaceArchiveEvidence, error) {
	if recorder, _ := ctx.Value(mutationRecorderKey{}).(MutationResumeRecorder); recorder == nil || !recorder.ReplayGuardEnabled() {
		return nil, fmt.Errorf("archive resume requires its durable mutation replay guard; no write attempted")
	}
	return c.publishWorkspaceArchive(ctx, plan, true)
}

func (c *Client) publishWorkspaceArchive(ctx context.Context, plan *WorkspaceArchivePlan, resume bool) (*WorkspaceArchiveEvidence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if plan == nil || len(plan.bodies) == 0 || c.journal == nil || c.journal.Carrier.ID != plan.manifest.CarrierID || c.repo != plan.manifest.Repository {
		return nil, fmt.Errorf("workspace archive publication requires its verified repository/journal carrier and prepared plan")
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct{ Login string }
	if err := json.Unmarshal(profile, &actor); err != nil {
		return nil, fmt.Errorf("read workspace archive publisher: %w", err)
	}
	if actor.Login == "" {
		return nil, fmt.Errorf("workspace archive publisher is unverified")
	}
	collection, err := c.readJournalParts(ctx, plan.manifest.CarrierID)
	if err != nil {
		return nil, err
	}
	parts := make([]apiComment, len(plan.bodies))
	if resume {
		if _, err := c.journalCarrier(ctx, plan.manifest.CarrierID); err != nil {
			return nil, err
		}
		observation := &WorkspaceArchiveObservation{Parts: []WorkspaceArchivePartObservation{}, MissingParts: []int{}}
		if err := observeWorkspaceArchiveCollection(plan, collection, actor.Login, observation, 0); err != nil {
			return nil, err
		}
		if observation.Archive != nil {
			return observation.Archive, nil
		}
		for _, part := range observation.Parts {
			parts[part.Index] = collection.byID[part.ID]
		}
	}
	for _, root := range collection.byID {
		archive, err := decodeWorkspaceArchive(root, c.repo, plan.manifest.CarrierID, collection, 0)
		if err != nil {
			return nil, err
		}
		if archive != nil && archive.OperationID == plan.manifest.OperationID {
			return nil, fmt.Errorf("workspace archive operation %s already has a native root; inspect it before explicit resume", archive.OperationID)
		}
	}
	if !resume && len(collection.byTransfer[plan.manifest.Transfer]) != 0 {
		return nil, fmt.Errorf("workspace archive operation %s has a partial upload; no automatic replay; inspect before explicit resume", plan.manifest.OperationID)
	}
	if _, err := c.journalCarrier(ctx, plan.manifest.CarrierID); err != nil {
		return nil, err
	}
	number, _ := Number(plan.manifest.CarrierID)
	path := fmt.Sprintf("/issues/%d/comments", number)
	for i, body := range plan.bodies {
		if parts[i].ID != 0 {
			continue
		}
		data, err := c.request(ctx, "POST", path, map[string]string{"body": body}, false)
		if err != nil {
			return nil, fmt.Errorf("workspace archive %s part %d upload uncertain; no automatic retry or entity write: %w", plan.manifest.OperationID, i+1, err)
		}
		if err := json.Unmarshal(data, &parts[i]); err != nil {
			return nil, fmt.Errorf("workspace archive %s part response unreadable; inspect before retry: %w", plan.manifest.OperationID, err)
		}
		if !validJournalPartNative(parts[i]) || parts[i].Body != body || parts[i].IssueURL != fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", c.repo, number) || !strings.EqualFold(parts[i].User.Login, actor.Login) {
			return nil, fmt.Errorf("workspace archive %s part publisher differs; inspect before retry", plan.manifest.OperationID)
		}
	}
	body, err := plan.bindParts(parts)
	if err != nil {
		return nil, err
	}
	collection, err = c.readJournalParts(withReadbackCost(ctx), plan.manifest.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("workspace archive parts uploaded but readback failed; no root or entity write: %w", err)
	}
	if len(collection.byTransfer[plan.manifest.Transfer]) != len(parts) {
		return nil, fmt.Errorf("workspace archive upload has missing/duplicate parts; no root or entity write")
	}
	for _, native := range collection.byID {
		archive, err := decodeWorkspaceArchive(native, c.repo, plan.manifest.CarrierID, collection, 0)
		if err != nil {
			return nil, err
		}
		if archive != nil && archive.OperationID == plan.manifest.OperationID {
			return nil, fmt.Errorf("workspace archive root was published concurrently; inspect before retry; no entity write")
		}
	}
	for _, part := range parts {
		observed, ok := collection.byID[part.ID]
		if !ok || observed.Body != part.Body || observed.IssueURL != part.IssueURL || !validJournalPartNative(observed) || !observed.CreatedAt.Equal(part.CreatedAt) || !strings.EqualFold(observed.User.Login, actor.Login) {
			return nil, fmt.Errorf("workspace archive uploaded part changed; no root or entity write")
		}
	}
	if _, err := c.journalCarrier(ctx, plan.manifest.CarrierID); err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "POST", path, map[string]string{"body": body}, false)
	if err != nil {
		return nil, fmt.Errorf("workspace archive %s root publication uncertain; inspect before retry; no entity write: %w", plan.manifest.OperationID, err)
	}
	var root apiComment
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("workspace archive root response unreadable; inspect before retry: %w", err)
	}
	if root.Body != body || !strings.EqualFold(root.User.Login, actor.Login) {
		return nil, fmt.Errorf("workspace archive root response differs; inspect before retry")
	}
	collection, err = c.readJournalParts(withReadbackCost(ctx), plan.manifest.CarrierID)
	if err != nil {
		return nil, fmt.Errorf("workspace archive root published but readback failed; inspect before retry: %w", err)
	}
	observed, ok := collection.byID[root.ID]
	if !ok || observed.Body != root.Body || !observed.CreatedAt.Equal(root.CreatedAt) || !strings.EqualFold(observed.User.Login, actor.Login) {
		return nil, fmt.Errorf("workspace archive root disappeared/changed; inspect before retry")
	}
	roots := 0
	for _, native := range collection.byID {
		archive, err := decodeWorkspaceArchive(native, c.repo, plan.manifest.CarrierID, collection, 0)
		if err != nil {
			return nil, err
		}
		if archive != nil && archive.OperationID == plan.manifest.OperationID {
			roots++
		}
	}
	if roots != 1 {
		return nil, fmt.Errorf("workspace archive root publication is ambiguous; inspect before retry; no entity write")
	}
	return decodeWorkspaceArchive(observed, c.repo, plan.manifest.CarrierID, collection, 0)
}
