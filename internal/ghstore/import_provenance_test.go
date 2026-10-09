package ghstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func largeImportSource(t *testing.T) ImportItem {
	t.Helper()
	source := importTestItem("td-source")
	source.SourceNamespace = "project"
	source.Issue.CreatorSession = "original-author"
	source.Issue.CreatedAt = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	source.Logs = []models.Log{{ID: "log-old", IssueID: source.Issue.ID, SessionID: "original-worker", Type: models.LogTypeSecurity, Message: strings.Repeat("한글 original <!-- td:activity:v1 -->\n", 3000), Timestamp: source.Issue.CreatedAt}}
	source.Handoffs = []models.Handoff{{ID: "handoff-old", IssueID: source.Issue.ID, SessionID: "original-worker", Done: []string{"Original work"}, Remaining: []string{"Original remaining"}, Timestamp: source.Issue.CreatedAt}}
	return source
}

func importSourceProvenanceComments(t *testing.T, source ImportItem, targetID string, firstID int64) []apiComment {
	t.Helper()
	parts, err := sourceImportProvenance(source, targetID)
	if err != nil {
		t.Fatal(err)
	}
	comments := []apiComment{}
	for index, part := range parts {
		body, err := renderImportProvenance(part)
		if err != nil {
			t.Fatal(err)
		}
		comments = append(comments, apiComment{ID: firstID + int64(index), IssueURL: "https://api.github.com/repos/owner/repo/issues/" + strings.TrimPrefix(targetID, "gh-"), Body: body})
	}
	return comments
}

func TestImportProvenanceLargeHistoryRoundTripAndPassiveComments(t *testing.T) {
	source := largeImportSource(t)
	parts, err := sourceImportProvenance(source, "gh-1")
	if err != nil || len(parts) < 3 {
		t.Fatal("history was not chunked", len(parts), err)
	}
	snapshot := &Snapshot{entries: []snapshotEntry{{Issue: apiIssue{Number: 1, State: "open"}}}}
	for i, part := range parts {
		body, err := renderImportProvenance(part)
		if err != nil || len(body) > 60000 || strings.Contains(body, activityPrefix) {
			t.Fatal("provenance exceeds comment bound or contains active metadata", err)
		}
		comment := apiComment{ID: int64(i + 1), Body: body}
		activity, err := decodeActivity(comment, "gh-1")
		if err != nil || !activity.Native || activity.Kind != "comment" || activity.SessionID != "" || activity.OperationID != "" {
			t.Fatal("historical content became active workflow history", activity, err)
		}
		snapshot.entries[0].Comments = append(snapshot.entries[0].Comments, comment)
	}
	inventory, err := snapshotImportProvenance(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := recoverImportSource(inventory[parts[0].key()])
	if err != nil || !reflect.DeepEqual(*recovered, source) {
		t.Fatal("original author/time/log/handoff data did not round-trip", err)
	}
}

func TestImportProvenanceRejectsMissingDuplicateAndEditedParts(t *testing.T) {
	parts, err := sourceImportProvenance(largeImportSource(t), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	group := map[int]importProvenancePart{}
	for _, part := range parts {
		group[part.Index] = part
	}
	delete(group, 1)
	if _, err := reassembleImportProvenance(group); err == nil {
		t.Fatal("incomplete history accepted")
	}
	group[1] = parts[1]
	modified := group[1]
	modified.Data = base64.StdEncoding.EncodeToString([]byte("different bytes"))
	group[1] = modified
	if _, err := reassembleImportProvenance(group); err == nil {
		t.Fatal("edited data accepted as original history")
	}
	body, _ := renderImportProvenance(parts[0])
	for _, edited := range []string{
		strings.Replace(body, "Imported history", "Edited history", 1),
		strings.Replace(body, "provenance:v1", "provenance:v2", 1),
		strings.Replace(body, `"version":1`, `"version":1,"future":true`, 1),
		body + " appended",
	} {
		if _, err := decodeImportProvenance(edited); err == nil {
			t.Fatal("edited/unknown provenance accepted")
		}
	}
	snapshot := &Snapshot{entries: []snapshotEntry{{Issue: apiIssue{Number: 1, State: "open"}, Comments: []apiComment{{ID: 1, Body: body}, {ID: 2, Body: body}}}}}
	if _, err := snapshotImportProvenance(snapshot); err == nil {
		t.Fatal("duplicate part accepted")
	}
	snapshot.entries[0].Issue.Number = 2
	snapshot.entries[0].Comments = snapshot.entries[0].Comments[:1]
	if _, err := snapshotImportProvenance(snapshot); err == nil {
		t.Fatal("part on another target accepted")
	}
}

func TestImportProvenanceLostPartResponseResumesWithoutDuplicatePosts(t *testing.T) {
	source := largeImportSource(t)
	parts, err := sourceImportProvenance(source, "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	item := apiIssue{Number: 1, State: "open", Title: "Imported"}
	basis, _ := importTargetBasis(item)
	comments := []apiComment{}
	posts, gets := 0, 0
	failure := errors.New("fixture response lost after provenance part saved")
	c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "POST") && args[5] == "repos/owner/repo/issues/1/comments" {
			posts++
			var fields map[string]string
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			comment := apiComment{ID: int64(len(comments) + 1), Body: fields["body"], IssueURL: "https://api.github.com/repos/owner/repo/issues/1"}
			comments = append(comments, comment)
			if posts == 2 {
				return nil, failure
			}
			return json.Marshal(comment)
		}
		if !slices.Contains(args, "GET") || payload != nil {
			t.Fatal("unexpected provenance mutation", args)
		}
		gets++
		switch args[5] {
		case "repos/owner/repo/issues/1":
			return json.Marshal(item)
		case "repos/owner/repo/issues?state=all&per_page=100":
			return json.Marshal([][]apiIssue{{item}})
		case "repos/owner/repo/issues/comments?per_page=100":
			return json.Marshal([][]apiComment{comments})
		default:
			t.Fatal("unexpected per-comment read", args)
			return nil, nil
		}
	}}
	saved, err := c.writeImportProvenance(context.Background(), parts, importProvenanceInventory{}, basis)
	if !errors.Is(err, failure) || saved != 1 || posts != 2 || len(comments) != 2 {
		t.Fatal("partial success or lost-response cause incorrect", saved, err, posts)
	}
	// One fresh I+C observation recovers the uncertain part; no comment GET
	// is issued for each expected chunk, and only missing chunks are posted.
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := snapshotImportProvenance(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	beforeGets := gets
	saved, err = c.writeImportProvenance(context.Background(), parts, inventory, basis)
	if err != nil || saved != len(parts)-2 || posts != len(parts) || gets-beforeGets != 2 {
		t.Fatal("resume duplicated parts or added per-part reads", saved, err, posts, gets-beforeGets)
	}
	snapshot, err = c.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err = snapshotImportProvenance(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := recoverImportSource(inventory[parts[0].key()])
	if err != nil || !reflect.DeepEqual(*recovered, source) {
		t.Fatal("resumed history did not round-trip", err)
	}
	saved, err = c.writeImportProvenance(context.Background(), parts, inventory, basis)
	if err != nil || saved != 0 || posts != len(parts) {
		t.Fatal("complete replay posted more comments", saved, err, posts)
	}
}

func TestImportProvenancePreservesGitHubAuthorAndHistoricalReviews(t *testing.T) {
	now := time.Date(2021, 2, 3, 4, 5, 6, 0, time.UTC)
	details := &IssueDetails{Status: models.StatusInReview, CreatorSession: "old-creator", ImplementerSession: "old-worker", Reviews: []models.IssueReview{{ID: "old-review", ReviewerSession: "old-reviewer", Decision: "approved", CreatedAt: now}}}
	body, err := encodeBody("Original description", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: details})
	if err != nil {
		t.Fatal(err)
	}
	native := apiIssue{Number: 10, Title: "Original", Body: body, State: "open", CreatedAt: now, UpdatedAt: now}
	native.User.Login = "original-github-author"
	record, err := native.record()
	if err != nil {
		t.Fatal(err)
	}
	source := ImportItem{Issue: record.Issue, GitHub: &ExportedIssue{Version: 1, Repository: "owner/source", Record: *record, Body: body, NativeState: "open", Author: native.User.Login, Activity: []models.Activity{{ID: "ghc-1", IssueID: record.ID, Kind: "handoff", SessionID: "old-worker", Author: "original-github-author", CreatedAt: now, UpdatedAt: now, Done: []string{"Original handoff"}}}}}
	if err := ValidateImportItems([]ImportItem{source}); err != nil {
		t.Fatal(err)
	}
	parts, err := sourceImportProvenance(source, "gh-99")
	if err != nil {
		t.Fatal(err)
	}
	group := map[int]importProvenancePart{}
	for _, part := range parts {
		group[part.Index] = part
		comment, _ := renderImportProvenance(part)
		activity, err := decodeActivity(apiComment{ID: int64(part.Index + 1), Body: comment}, "gh-99")
		if err != nil || activity.Kind != "comment" || activity.SessionID != "" {
			t.Fatal("historical review/handoff gained active privileges", activity, err)
		}
	}
	recovered, err := recoverImportSource(group)
	if err != nil {
		t.Fatal(err)
	}
	originalJSON, _ := json.Marshal(source)
	recoveredJSON, _ := json.Marshal(recovered)
	if string(originalJSON) != string(recoveredJSON) || recovered.GitHub.Author != native.User.Login || recovered.GitHub.Record.Details.Reviews[0].ReviewerSession != "old-reviewer" {
		t.Fatal("GitHub provenance lost author/review/body fields")
	}
}

func TestImportProvenanceExistingAlteredPartStopsBeforeRemoteCalls(t *testing.T) {
	parts, err := sourceImportProvenance(importTestItem("td-source"), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	changed := parts[0]
	changed.Data = base64.StdEncoding.EncodeToString([]byte("edited content"))
	inventory := importProvenanceInventory{parts[0].key(): {0: changed}}
	c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		t.Fatal("altered existing history reached remote calls", args)
		return nil, nil
	}}
	if saved, err := c.writeImportProvenance(context.Background(), parts, inventory, strings.Repeat("a", 64)); err == nil || saved != 0 {
		t.Fatal("altered existing history accepted", saved, err)
	}
	changed.Index, changed.Count = 100, 101
	inventory = importProvenanceInventory{parts[0].key(): {100: changed}}
	if saved, err := c.writeImportProvenance(context.Background(), parts, inventory, strings.Repeat("a", 64)); err == nil || saved != 0 {
		t.Fatal("unexpected extra history part accepted", saved, err)
	}
}
