package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

type journalWriteFixture struct {
	client                                                                 *Client
	issues                                                                 map[int]apiIssue
	comment                                                                *apiComment
	mode                                                                   string
	targetWrites, receiptWrites, intentWrites, carrierWrites, commentReads int
	creationWrites                                                         int
	creationCollectionReads                                                int
	targetNumber                                                           int
}

func newJournalWriteFixture(t *testing.T, mode string) *journalWriteFixture {
	t.Helper()
	body, err := encodeBody("Journal", metadata{EntityKind: "journal", Type: models.TypeTask, Priority: models.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	f := &journalWriteFixture{mode: mode, targetNumber: 1, issues: map[int]apiIssue{
		1: {Number: 1, Title: "Target", Body: "Before", State: "open", UpdatedAt: clock},
		9: {Number: 9, Title: "Journal", Body: body, State: "open", UpdatedAt: clock},
	}}
	f.client = &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		method, path := args[4], strings.TrimPrefix(args[5], "repos/owner/repo")
		encode := func(value any) ([]byte, error) { return json.Marshal(value) }
		switch {
		case path == "user":
			login := "github-author"
			if f.mode == "undo-actor" {
				login = "other-author"
			}
			return encode(map[string]string{"login": login})
		case path == "/issues?state=all&per_page=100":
			if f.creationWrites > 0 {
				f.creationCollectionReads++
			}
			if f.creationWrites > 0 && f.mode == "create-list-error" {
				return nil, errors.New("HTTP 403 collection denied")
			}
			page := []apiIssue{}
			for _, issue := range f.issues {
				if issue.Number == 2 && (f.mode == "create-list-missing" || (f.mode == "create-list-delayed" && f.creationCollectionReads == 1)) {
					continue
				}
				page = append(page, issue)
			}
			return encode([][]apiIssue{page})
		case path == "/issues/comments?per_page=100":
			page := []apiComment{}
			if f.comment != nil {
				page = append(page, *f.comment)
			}
			return encode([][]apiComment{page})
		case method == "POST" && path == "/issues":
			var value struct{ Title, Body string }
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, err
			}
			_, meta, _, err := decodeBody(value.Body)
			if err != nil {
				return nil, err
			}
			if meta.EntityKind != "journal" {
				f.creationWrites++
				if f.mode == "create-denied" {
					return nil, errors.New("HTTP 403 creation permission denied")
				}
				var full struct{ Labels []string }
				if err := json.Unmarshal(payload, &full); err != nil {
					return nil, err
				}
				created := apiIssue{Number: 2, Title: value.Title, Body: value.Body, State: "open", UpdatedAt: clock.Add(2 * time.Second)}
				created.User.Login = "github-author"
				if f.mode == "create-author" {
					created.User.Login = "different-author"
				}
				for _, label := range full.Labels {
					created.Labels = append(created.Labels, struct {
						Name string `json:"name"`
					}{label})
				}
				if f.mode == "create-labels" {
					created.Labels = nil
				}
				f.issues[2] = created
				if f.mode == "create-duplicate" {
					other := created
					other.Number = 3
					f.issues[3] = other
				}
				if f.mode == "create-uncertain" {
					return nil, errors.New("connection lost after creation")
				}
				if f.mode == "create-response" {
					copy := created
					copy.Body = "Unexpected response"
					return encode(copy)
				}
				return encode(created)
			}
			f.carrierWrites++
			created := apiIssue{Number: 9, Title: value.Title, Body: value.Body, State: "open", UpdatedAt: clock}
			f.issues[9] = created
			if f.mode == "duplicate-bootstrap" {
				other := created
				other.Number = 10
				f.issues[10] = other
			}
			return encode(created)
		case method == "POST" && path == "/issues/9/comments":
			f.intentWrites++
			if f.mode == "intent-denied" {
				return nil, errors.New("HTTP 403 intent permission denied")
			}
			var value struct{ Body string }
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, err
			}
			f.comment = &apiComment{ID: 101, IssueURL: "https://api.github.com/repos/owner/repo/issues/9", Body: value.Body,
				URL: "https://github.com/owner/repo/issues/9#issuecomment-101", CreatedAt: clock, UpdatedAt: clock}
			f.comment.User.Login = "github-author"
			carrier := f.issues[9]
			carrier.UpdatedAt = clock.Add(time.Second)
			f.issues[9] = carrier
			if f.mode == "intent-response" {
				copy := *f.comment
				copy.Body = "Unverified"
				return encode(copy)
			}
			if f.mode == "intent-uncertain" {
				return nil, errors.New("connection lost after intent write")
			}
			return encode(f.comment)
		case path == "/issues/comments/101":
			if method == "GET" {
				f.commentReads++
				if f.mode == "intent-read" {
					return nil, errors.New("readback unavailable")
				}
				if f.mode == "target-conflict" && f.commentReads == 1 {
					target := f.issues[f.targetNumber]
					target.Body = "Concurrent edit"
					target.UpdatedAt = clock.Add(time.Second)
					f.issues[f.targetNumber] = target
				}
				if f.mode == "journal-concurrent" && f.commentReads == 2 {
					f.comment.Body = "Edited journal"
				}
				return encode(f.comment)
			}
			f.receiptWrites++
			if f.mode == "receipt-denied" || (f.mode == "undo-receipt-denied" && f.receiptWrites == 3) {
				return nil, errors.New("HTTP 403 receipt denied")
			}
			var value struct{ Body string }
			if err := json.Unmarshal(payload, &value); err != nil {
				return nil, err
			}
			f.comment.Body = value.Body
			f.comment.UpdatedAt = clock.Add(3 * time.Second)
			carrier := f.issues[9]
			carrier.UpdatedAt = clock.Add(3 * time.Second)
			f.issues[9] = carrier
			if f.mode == "receipt-uncertain" || (f.mode == "undo-receipt-uncertain" && f.receiptWrites == 3) {
				return nil, errors.New("connection lost after receipt write")
			}
			return encode(f.comment)
		case path == "/issues/1" || path == "/issues/9" || path == "/issues/2":
			n := 1
			if path == "/issues/9" {
				n = 9
			}
			if path == "/issues/2" {
				n = 2
			}
			if f.mode == "create-readback" && n == 2 {
				return nil, errors.New("creation readback unavailable")
			}
			if f.mode == "create-concurrent" && n == 2 {
				target := f.issues[n]
				target.Body = "Concurrent creation edit"
				f.issues[n] = target
			}
			if method == "PATCH" {
				f.targetWrites++
				if f.mode == "target-denied" {
					return nil, errors.New("HTTP 403 target permission denied")
				}
				var values struct {
					Title, Body, State *string
					Labels             *[]string
				}
				if err := json.Unmarshal(payload, &values); err != nil {
					return nil, err
				}
				target := f.issues[n]
				if values.Title != nil {
					target.Title = *values.Title
				}
				if values.Body != nil {
					target.Body = *values.Body
				}
				if values.State != nil {
					target.State = *values.State
				}
				if values.Labels != nil {
					target.Labels = nil
					for _, label := range *values.Labels {
						target.Labels = append(target.Labels, struct {
							Name string `json:"name"`
						}{label})
					}
				}
				target.UpdatedAt = target.UpdatedAt.Add(2 * time.Second)
				f.issues[n] = target
				if f.mode == "target-uncertain" {
					return nil, errors.New("connection lost after target write")
				}
			}
			if n == f.targetNumber && ((f.mode == "target-later" && f.commentReads >= 3) || (f.mode == "undo-later" && f.commentReads >= 5)) {
				target := f.issues[f.targetNumber]
				target.Body = "Later edit"
				target.UpdatedAt = clock.Add(4 * time.Second)
				f.issues[f.targetNumber] = target
			}
			return encode(f.issues[n])
		default:
			return nil, fmt.Errorf("unexpected %s %s", method, path)
		}
	}}
	return f
}

func TestJournaledUpdateVerifiedIntentTargetAndResult(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
		t.Fatal(err)
	}
	body := "After"
	updated, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &body})
	if err != nil || updated.Description != body || f.targetWrites != 1 || f.intentWrites != 1 || f.receiptWrites != 1 {
		t.Fatal(updated, err, f)
	}
	snapshot, err := f.client.ReadSnapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := snapshot.ActionHistory("ses_actor")
	if err != nil || len(actions) != 1 || actions[0].Phase != "applied" || actions[0].Before.Body != "Before" || actions[0].After.Body != "After" || actions[0].CarrierID != "gh-9" {
		t.Fatal(actions, err)
	}
	if len(snapshot.Issues(true)) != 1 {
		t.Fatal("journal carrier appeared as a task")
	}
	if _, err := actions[0].PlanIssueUndo(updated, "ses_actor", "github-author"); err != nil {
		t.Fatal("journal comment clock invalidated target undo", err)
	}
}

func TestJournaledUpdateFailuresNeverRetryOrClaimSuccess(t *testing.T) {
	for _, tc := range []struct {
		mode         string
		targetWrites int
		phase        string
	}{
		{"intent-denied", 0, ""}, {"intent-response", 0, "prepared"}, {"intent-read", 0, "prepared"},
		{"target-conflict", 0, "prepared"}, {"target-denied", 1, "prepared"}, {"target-uncertain", 1, "prepared"},
		{"receipt-denied", 1, "prepared"}, {"receipt-uncertain", 1, "applied"}, {"journal-concurrent", 1, ""},
		{"target-later", 1, "applied"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newJournalWriteFixture(t, tc.mode)
			if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
				t.Fatal(err)
			}
			body := "After"
			result, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &body})
			if err == nil || result != nil || f.targetWrites != tc.targetWrites || f.intentWrites != 1 || f.receiptWrites > 1 {
				t.Fatal(result, err, f)
			}
			if tc.phase != "" {
				a, e := decodeActionJournal(*f.comment, "gh-9")
				if e != nil || a.Phase != tc.phase {
					t.Fatal(a, e)
				}
			}
			if tc.mode == "target-conflict" && f.issues[1].Body != "Concurrent edit" {
				t.Fatal("overwrote concurrent target edit")
			}
			if tc.mode == "target-later" {
				var conflict *ConflictError
				if !errors.As(err, &conflict) || !conflict.AfterWrite || f.issues[1].Body != "Later edit" {
					t.Fatal("later change not reported", err)
				}
			}
		})
	}
}

func TestJournalBootstrapDoesNotChooseDuplicateOrInvalidCarriers(t *testing.T) {
	for _, mode := range []string{"create", "duplicate", "duplicate-bootstrap", "invalid-session"} {
		t.Run(mode, func(t *testing.T) {
			f := newJournalWriteFixture(t, mode)
			sessionID := "ses_actor"
			if mode == "create" || mode == "duplicate-bootstrap" {
				delete(f.issues, 9)
			}
			if mode == "duplicate" {
				other := f.issues[9]
				other.Number = 10
				f.issues[10] = other
			}
			if mode == "invalid-session" {
				sessionID = "bad session"
			}
			err := f.client.EnableActionJournal(context.Background(), sessionID)
			if mode == "create" {
				if err != nil || f.client.journal == nil || f.carrierWrites != 1 {
					t.Fatal(err, f)
				}
			} else if err == nil || f.client.journal != nil {
				t.Fatal("ambiguous journal enabled", err)
			}
			if f.targetWrites != 0 || f.intentWrites != 0 {
				t.Fatal("bootstrap wrote a task", f)
			}
		})
	}
}

func TestJournaledUpdateRejectsRepurposedCarrierBeforeIntent(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
		t.Fatal(err)
	}
	carrier := f.issues[9]
	carrier.Body = "Repurposed as a normal task"
	f.issues[9] = carrier
	body := "After"
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &body}); err == nil {
		t.Fatal("repurposed carrier accepted")
	}
	if f.intentWrites != 0 || f.targetWrites != 0 {
		t.Fatal("wrote through changed carrier identity")
	}
}
