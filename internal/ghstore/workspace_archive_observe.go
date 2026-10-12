package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// WorkspaceArchiveObservation is fresh passive transport evidence. Missing
// parts are not permission to replay an uncertain POST: the controller must
// compare its durable write intent before deciding which writes were unattempted.
type WorkspaceArchiveObservation struct {
	CarrierID    string                            `json:"carrier_id,omitempty"`
	ObservedAt   time.Time                         `json:"observed_at"`
	Archive      *WorkspaceArchiveEvidence         `json:"archive,omitempty"`
	Parts        []WorkspaceArchivePartObservation `json:"parts"`
	MissingParts []int                             `json:"missing_parts"`
}

type WorkspaceArchivePartObservation struct {
	Index     int       `json:"index"`
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	SHA256    string    `json:"sha256"`
}

// ObserveWorkspaceArchiveData never bootstraps a carrier, POSTs a part/root or
// modifies an entity. One fresh bulk issue/comment snapshot verifies all parts;
// one actor lookup fences native authorship, never source historical actors.
func (c *Client) ObserveWorkspaceArchiveData(ctx context.Context, operation string, source []byte, mappings map[string]string, maxBytes int64) (*WorkspaceArchiveObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.journal == nil || c.journal.Group != nil {
		return nil, fmt.Errorf("workspace archive observation requires an independent tracked actor")
	}
	probe, err := PlanWorkspaceArchive(c.repo, "gh-"+strconv.Itoa(int(^uint(0)>>1)), operation, source, mappings, maxBytes)
	if err != nil {
		return nil, err
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	result := &WorkspaceArchiveObservation{ObservedAt: snapshot.ObservedAt(), Parts: []WorkspaceArchivePartObservation{}, MissingParts: []int{}}
	carrier := 0
	for _, record := range snapshot.records {
		if record.meta.EntityKind != "journal" || record.DeletedAt != nil {
			continue
		}
		if carrier != 0 {
			return nil, fmt.Errorf("multiple journal carriers; inspect before archive resume")
		}
		carrier = record.Number
		result.CarrierID = record.ID
	}
	if carrier == 0 {
		for i := range probe.bodies {
			result.MissingParts = append(result.MissingParts, i)
		}
		return result, nil
	}
	if c.journal.Carrier.ID != "" && c.journal.Carrier.ID != result.CarrierID {
		return nil, fmt.Errorf("workspace journal carrier changed; inspect before resume")
	}
	plan, err := PlanWorkspaceArchive(c.repo, result.CarrierID, operation, source, mappings, maxBytes)
	if err != nil {
		return nil, err
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct{ Login string }
	if err := json.Unmarshal(profile, &actor); err != nil {
		return nil, fmt.Errorf("read archive observer: %w", err)
	}
	if actor.Login == "" {
		return nil, fmt.Errorf("workspace archive observer is unverified")
	}
	for _, entry := range snapshot.entries {
		if entry.Issue.Number != carrier {
			continue
		}
		collection, err := collectJournalParts(entry.Comments)
		if err != nil {
			return nil, err
		}
		if err := observeWorkspaceArchiveCollection(plan, collection, actor.Login, result, maxBytes); err != nil {
			return nil, err
		}
		return result, nil
	}
	return nil, fmt.Errorf("workspace journal carrier lacks a complete native observation")
}

func observeWorkspaceArchiveCollection(plan *WorkspaceArchivePlan, collection *journalPartCollection, actor string, result *WorkspaceArchiveObservation, maxBytes int64) error {
	uri := fmt.Sprintf("https://api.github.com/repos/%s/issues/%s", plan.manifest.Repository, strings.TrimPrefix(plan.manifest.CarrierID, "gh-"))
	byIndex := map[int]apiComment{}
	for id, part := range collection.parts {
		if part.Transfer != plan.manifest.Transfer {
			continue
		}
		native := collection.byID[id]
		if part.Index < 0 || part.Index >= len(plan.bodies) || part.Count != len(plan.bodies) || part.OperationID != plan.manifest.OperationID || part.CarrierID != plan.manifest.CarrierID || native.Body != plan.bodies[part.Index] || native.IssueURL != uri || !validJournalPartNative(native) || !strings.EqualFold(native.User.Login, actor) {
			return fmt.Errorf("workspace archive part %d changed or has a foreign native author; no write attempted", part.Index+1)
		}
		if _, duplicate := byIndex[part.Index]; duplicate {
			return fmt.Errorf("workspace archive part %d has duplicate native uploads; no write attempted", part.Index+1)
		}
		byIndex[part.Index] = native
	}
	for i := range plan.bodies {
		native, found := byIndex[i]
		if !found {
			result.MissingParts = append(result.MissingParts, i)
			continue
		}
		result.Parts = append(result.Parts, WorkspaceArchivePartObservation{Index: i, ID: native.ID, CreatedAt: native.CreatedAt, SHA256: journalTransportHash(native.Body)})
	}
	for _, root := range collection.byID {
		archive, err := decodeWorkspaceArchive(root, plan.manifest.Repository, plan.manifest.CarrierID, collection, maxBytes)
		if err != nil {
			return err
		}
		if archive == nil || archive.OperationID != plan.manifest.OperationID {
			continue
		}
		if result.Archive != nil {
			return fmt.Errorf("workspace archive has duplicate native roots; no write attempted")
		}
		if !strings.EqualFold(archive.Author, actor) || archive.SourceSHA256 != plan.manifest.SourceSHA256 || journalTransportHash(string(archive.SourceEnvelope)) != plan.manifest.DataSHA256 || !reflect.DeepEqual(archive.Mappings, plan.manifest.Mappings) {
			return fmt.Errorf("workspace archive root differs from original input/mappings/native author; no write attempted")
		}
		result.Archive = archive
	}
	return nil
}
