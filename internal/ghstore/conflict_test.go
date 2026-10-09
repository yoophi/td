package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/td/internal/models"
	"slices"
	"strings"
	"testing"
)

func TestUpdateDetectsObservedConcurrentChanges(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			reads, writes := 0, 0
			current := map[string]any{"number": 1, "state": "open", "title": "Original", "body": "User text"}
			client := &Client{stateLabels: fixtureStateLabels(), run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") {
					reads++
					if (!after && reads == 2) || (after && reads == 3) {
						current["body"] = "Concurrent user edit"
					}
				} else {
					writes++
					var patch map[string]any
					if err := json.Unmarshal(payload, &patch); err != nil {
						t.Fatal(err)
					}
					for key, value := range patch {
						current[key] = value
					}
				}
				return fixtureIssueJSON(current)
			}}
			description := "Replacement"
			_, err := client.Update(context.Background(), "1", Changes{Description: &description})
			var conflict *ConflictError
			if !errors.As(err, &conflict) || conflict.AfterWrite != after {
				t.Fatalf("unexpected error %v", err)
			}
			if (!after && writes != 0) || (after && writes != 1) {
				t.Fatalf("writes=%d", writes)
			}
			if current["body"] != "Concurrent user edit" {
				t.Fatal("overwrote observed edit")
			}
		})
	}
}

func TestRevisionIgnoresLabelOrderButDetectsContentChanges(t *testing.T) {
	base := map[string]any{"number": 1, "state": "open", "title": "Fixture", "body": "text", "labels": []map[string]string{{"name": "topic"}, {"name": "td:open"}}, "updated_at": "2026-10-09T12:00:00Z"}
	decode := func(v map[string]any) *Record {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		r, err := decodeIssue(b)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	original := decode(base)
	copy := func() map[string]any {
		r := map[string]any{}
		for k, v := range base {
			r[k] = v
		}
		return r
	}
	reordered := copy()
	reordered["labels"] = []map[string]string{{"name": "td:open"}, {"name": "topic"}}
	r := decode(reordered)
	if original.revision != r.revision || !slices.Equal(r.Labels, []string{"td:open", "topic"}) {
		t.Fatal("order changed revision or presentation", r.Labels)
	}
	for _, change := range []map[string]any{{"title": "Changed"}, {"body": "Changed"}, {"state": "closed"}, {"updated_at": "2026-10-09T12:00:01Z"}, {"labels": []map[string]string{{"name": "td:open"}}}, {"labels": []map[string]string{{"name": "td:open"}, {"name": "renamed"}}}} {
		v := copy()
		for k, value := range change {
			v[k] = value
		}
		if decode(v).revision == original.revision {
			t.Fatal("actual change ignored", change)
		}
	}
}

func TestUpdateAcceptsLabelPermutationInPreAndPostReadback(t *testing.T) {
	reads, writes := 0, 0
	current := map[string]any{"number": 1, "state": "open", "title": "Original", "body": "Text"}
	client := &Client{stateLabels: fixtureStateLabels(), run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "GET") {
			reads++
		} else {
			writes++
			var patch map[string]any
			if err := json.Unmarshal(payload, &patch); err != nil {
				t.Fatal(err)
			}
			for k, v := range patch {
				current[k] = v
			}
		}
		labels := []map[string]string{{"name": "topic"}, {"name": "td:open"}}
		if reads%2 == 0 {
			labels[0], labels[1] = labels[1], labels[0]
		}
		current["labels"] = labels
		return fixtureIssueJSON(current)
	}}
	title := "Changed"
	r, err := client.Update(context.Background(), "1", Changes{Title: &title})
	if err != nil || r.Title != title || reads != 3 || writes != 1 {
		t.Fatal(r, err, reads, writes)
	}
}

func TestUpdateRejectsIgnoredFieldsAndUnverifiedWrites(t *testing.T) {
	for _, mode := range []string{"ignored", "read-failed", "write-failed"} {
		t.Run(mode, func(t *testing.T) {
			reads, writes := 0, 0
			current := map[string]any{"number": 1, "state": "open", "title": "Original"}
			client := &Client{stateLabels: fixtureStateLabels(), run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") {
					reads++
					if mode == "read-failed" && reads == 3 {
						return nil, context.DeadlineExceeded
					}
				} else {
					writes++
					if mode == "write-failed" {
						return nil, context.DeadlineExceeded
					}
					if mode != "ignored" {
						current["title"] = "Changed"
					}
				}
				return fixtureIssueJSON(current)
			}}
			title := "Changed"
			_, err := client.Update(context.Background(), "1", Changes{Title: &title})
			if err == nil || !strings.Contains(err.Error(), "before retrying") || writes != 1 {
				t.Fatalf("writes=%d err=%v", writes, err)
			}
		})
	}
}

func TestCreateUncertainResultIncludesOperationIdentity(t *testing.T) {
	for _, failure := range []string{"HTTP 403: Resource not accessible", "HTTP 429: rate limit exceeded", "context deadline exceeded"} {
		calls := 0
		operation := ""
		client := &Client{stateLabels: fixtureStateLabels(), run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
			calls++
			var fields struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			_, meta, _, err := decodeBody(fields.Body)
			if err != nil {
				t.Fatal(err)
			}
			operation = meta.OperationID
			return nil, errors.New(failure)
		}}
		_, err := client.Create(context.Background(), &models.Issue{Title: "Recoverable fixture", Type: models.TypeTask, Priority: models.PriorityP2})
		if calls != 1 || operation == "" || err == nil || !strings.Contains(err.Error(), operation) || !strings.Contains(err.Error(), failure) {
			t.Fatalf("calls=%d op=%s err=%v", calls, operation, err)
		}
	}
}

func TestObservedUpdateRetainsPolicyReadRevision(t *testing.T) {
	state := map[string]any{"number": 1, "state": "open", "title": "Original"}
	writes := 0
	client := &Client{stateLabels: fixtureStateLabels(), repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") {
			writes++
			var fields map[string]any
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			for key, value := range fields {
				state[key] = value
			}
		}
		return fixtureIssueJSON(state)
	}}
	observed, err := client.Get(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	state["title"] = "Concurrent edit after policy read"
	title := "Replacement"
	_, err = client.UpdateObserved(context.Background(), observed, Changes{Title: &title})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || writes != 0 || observed.Title != "Original" {
		t.Fatalf("writes=%d observed=%+v err=%v", writes, observed, err)
	}
	current, err := client.Get(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	details := IssueDetails{Minor: true, Sprint: "current"}
	updated, err := client.UpdateObserved(context.Background(), current, Changes{Details: &details})
	if err != nil || writes != 1 || !updated.Minor || updated.Sprint != "current" || current.Minor {
		t.Fatalf("writes=%d updated=%+v err=%v", writes, updated, err)
	}
	foreign := *current
	foreign.repository = "other/repo"
	if _, err := client.UpdateObserved(context.Background(), &foreign, Changes{Title: &title}); err == nil || writes != 1 {
		t.Fatal("foreign observation accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.UpdateObserved(ctx, updated, Changes{Title: &title}); !errors.Is(err, context.Canceled) || writes != 1 {
		t.Fatalf("cancelled update: %v", err)
	}
}
