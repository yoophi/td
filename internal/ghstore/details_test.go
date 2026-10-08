package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestDetailedMetadataRoundtrip(t *testing.T) {
	now := time.Now().UTC()
	date := "2026-10-09"
	details := IssueDetails{Status: models.StatusInReview, Minor: true, Sprint: "Sprint 1", ParentID: "gh-8", CreatorSession: "creator", ImplementerSession: "impl", ReviewerSession: "reviewer", ReviewRequestedBySession: "requester", CreatedBranch: "feature", DueDate: &date, DeferUntil: &date, DeferCount: 2, ReviewedAt: &now, Dependencies: []string{"gh-2"}, Files: []models.IssueFile{{FilePath: "a.go", Role: models.FileRoleImplementation}}, Reviews: []models.IssueReview{{ID: "rv1", ReviewerSession: "reviewer", Decision: "approved", CreatedAt: now}}, Sessions: []models.IssueSessionHistory{{SessionID: "impl", Action: models.ActionSessionStarted, CreatedAt: now}}}
	body, err := encodeBody("Description", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &details})
	if err != nil {
		t.Fatal(err)
	}
	record, err := (apiIssue{Number: 1, State: "open", Title: "Title", Body: body}).record()
	if err != nil || !reflect.DeepEqual(record.Details, &details) || record.Status != models.StatusInReview || record.ParentID != "gh-8" || !record.Minor || record.DueDate == nil {
		t.Fatalf("%+v %v", record, err)
	}
	record, err = (apiIssue{Number: 1, State: "closed", Title: "Title", Body: body}).record()
	if err != nil || record.Status != models.StatusClosed || record.ReviewerSession != "" || record.ReviewedAt != nil || record.ImplementerSession != "" {
		t.Fatalf("native close became approved: %+v %v", record, err)
	}
	// Historical review evidence remains visible, but is not an active grant.
	if len(record.Details.Reviews) != 1 {
		t.Fatal("discarded history")
	}
}

func TestMetadataRejectsInvalidDetails(t *testing.T) {
	badDate := "2026-02-30"
	for _, details := range []IssueDetails{
		{Status: "future"}, {DeferCount: -1}, {DueDate: &badDate}, {ParentID: "#2"},
		{Dependencies: []string{"gh-2", "gh-2"}}, {Dependencies: []string{""}},
		{Files: []models.IssueFile{{FilePath: "", Role: models.FileRoleTest}}},
		{Reviews: []models.IssueReview{{ID: "forged"}}},
		{Sessions: []models.IssueSessionHistory{{SessionID: "session", Action: "unknown"}}},
	} {
		if _, err := encodeBody("", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &details}); err == nil {
			t.Errorf("accepted %+v", details)
		}
	}
	for _, body := range []string{
		markerStart + `{"type":"task","priority":"P2","details":{"future":true}}` + markerEnd,
		markerStart + `{"type":"task","priority":"P2","entity_kind":"future"}` + markerEnd,
	} {
		if _, _, _, err := decodeBody(body); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestIssueListsSeparateEntitiesAndDeletedRecords(t *testing.T) {
	now := time.Now().UTC()
	issues := []apiIssue{{Number: 1, State: "open", Title: "Native"}}
	for i, kind := range []string{"issue", "board", "note", "issue"} {
		meta := metadata{Type: models.TypeTask, Priority: models.PriorityP2, EntityKind: kind}
		if i == 3 {
			meta.Details = &IssueDetails{DeletedAt: &now}
		}
		body, err := encodeBody("Body", meta)
		if err != nil {
			t.Fatal(err)
		}
		issues = append(issues, apiIssue{Number: i + 2, State: "open", Title: kind, Body: body})
	}
	client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "--paginate") {
			return json.Marshal([][]apiIssue{issues[:2], issues[2:]})
		}
		for _, item := range issues {
			if slices.Contains(args, "repos/owner/repo/issues/"+strconv.Itoa(item.Number)) {
				return json.Marshal(item)
			}
		}
		return nil, errors.New("unexpected request")
	}}
	records, err := client.List(context.Background(), true)
	if err != nil || len(records) != 2 {
		t.Fatalf("%+v %v", records, err)
	}
	records, err = client.ListIncludingDeleted(context.Background(), true)
	if err != nil || len(records) != 3 || records[2].DeletedAt == nil {
		t.Fatalf("%+v %v", records, err)
	}
	if _, err := client.Get(context.Background(), "3"); err == nil || !strings.Contains(err.Error(), "board entity") {
		t.Fatalf("internal entity leaked: %v", err)
	}
	if _, err := client.Get(context.Background(), "5"); err == nil || !strings.Contains(err.Error(), "is deleted") {
		t.Fatalf("deleted issue leaked: %v", err)
	}
	if _, err := client.GetIncludingDeleted(context.Background(), "5"); err != nil {
		t.Fatal(err)
	}
}

func TestIndividualDetailsUpdatePreservesOtherMetadata(t *testing.T) {
	due := "2026-12-01"
	body, err := encodeBody("Description", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Minor: true, Sprint: "before", DueDate: &due, ParentID: "gh-8", Dependencies: []string{"gh-2"}}})
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"number": 1, "state": "open", "title": "Title", "body": body}
	client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") {
			var fields map[string]any
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			for key, value := range fields {
				state[key] = value
			}
		}
		return json.Marshal(state)
	}}
	for _, sprint := range []string{"next", ""} {
		updated, err := client.Update(context.Background(), "gh-1", Changes{Sprint: &sprint})
		if err != nil || updated.Sprint != sprint || !updated.Minor || updated.DueDate == nil || *updated.DueDate != due || updated.ParentID != "gh-8" || len(updated.Details.Dependencies) != 1 {
			t.Fatalf("%+v %v", updated, err)
		}
	}
}

func TestCreateDetailsAndRejectedLoss(t *testing.T) {
	for _, drop := range []bool{false, true} {
		writes := 0
		client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
			writes++
			var fields map[string]any
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			fields["number"] = 1
			fields["state"] = "open"
			if drop {
				fields["body"] = ""
			}
			return json.Marshal(fields)
		}}
		result, err := client.Create(context.Background(), &models.Issue{Title: "Minor fixture", Type: models.TypeTask, Priority: models.PriorityP2, Status: models.StatusOpen, Minor: true})
		if drop {
			if err == nil || !strings.Contains(err.Error(), "was created, but returned fields differ") {
				t.Fatalf("%+v %v", result, err)
			}
		} else if err != nil || !result.Minor {
			t.Fatalf("%+v %v", result, err)
		}
		if writes != 1 {
			t.Fatalf("write retried: %d", writes)
		}
	}
}

func TestDeletedObservationCanRestoreWithoutChangingNativeState(t *testing.T) {
	now := time.Now().UTC()
	body, err := encodeBody("Description", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Status: models.StatusClosed, DeletedAt: &now, Minor: true}})
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"number": 1, "state": "closed", "title": "Title", "body": body}
	writes := 0
	client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") {
			writes++
			var patch map[string]any
			if err := json.Unmarshal(payload, &patch); err != nil {
				t.Fatal(err)
			}
			if _, exists := patch["state"]; exists {
				t.Fatal("restore changed native open/closed state")
			}
			for key, value := range patch {
				state[key] = value
			}
		}
		return json.Marshal(state)
	}}
	title := "Changed"
	if _, err := client.Update(context.Background(), "1", Changes{Title: &title}); err == nil || writes != 0 {
		t.Fatal("ordinary update mutated deleted issue")
	}
	observed, err := client.GetIncludingDeleted(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	details := *observed.Details
	details.DeletedAt = nil
	restored, err := client.UpdateObserved(context.Background(), observed, Changes{Details: &details})
	if err != nil || restored.DeletedAt != nil || restored.Status != models.StatusClosed || !restored.Minor || writes != 1 {
		t.Fatalf("%+v %v", restored, err)
	}
	if observed.DeletedAt == nil {
		t.Fatal("mutated caller's observation")
	}
}
