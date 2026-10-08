package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestActivityRoundTrip(t *testing.T) {
	for _, input := range []activityData{
		{Kind: "comment", OperationID: "op1", SessionID: "ses1", Message: "한글 $(whoami) `id`\n-->"},
		{Kind: "log", OperationID: "op2", SessionID: "ses1", WorkSessionID: "ws1", Message: "Decision", LogType: models.LogTypeDecision},
		{Kind: "handoff", OperationID: "op3", SessionID: "ses1", Done: []string{"one", "two\nlines"}, Remaining: []string{"next"}, Decisions: []string{"choice"}, Uncertain: []string{"unknown"}, Snapshot: &models.GitSnapshot{CommitSHA: "abc", Branch: "main", DirtyFiles: 2}},
	} {
		body, err := renderActivity(input)
		if err != nil {
			t.Fatal(err)
		}
		comment := apiComment{ID: 42, Body: body, CreatedAt: time.Now(), URL: "https://example.com/comment/42"}
		comment.User.Login = "actual-author"
		comment.UpdatedAt = comment.CreatedAt.Add(time.Second)
		got, err := decodeActivity(comment, "gh-9")
		if err != nil {
			t.Fatal(err)
		}
		if got.Native || !got.Edited || got.Author != "actual-author" || got.ID != "ghc-42" || got.IssueID != "gh-9" || got.Kind != input.Kind || got.OperationID != input.OperationID || got.Message != input.Message || !reflect.DeepEqual(got.Done, input.Done) || !reflect.DeepEqual(got.Snapshot, input.Snapshot) {
			t.Fatalf("bad roundtrip: %+v", got)
		}
	}
	native, err := decodeActivity(apiComment{ID: 1, Body: "A normal GitHub comment"}, "gh-9")
	if err != nil || !native.Native || native.SessionID != "" || native.Message != "A normal GitHub comment" {
		t.Fatalf("native=%+v %v", native, err)
	}
}

func TestActivityRejectsCorruption(t *testing.T) {
	body, _ := renderActivity(activityData{Kind: "comment", OperationID: "op", SessionID: "ses", Message: "original"})
	for _, invalid := range []string{
		strings.Replace(body, "original", "edited", 1),
		strings.Replace(body, "v1\n", "v2\n", 1),
		strings.Replace(body, `"kind":`, `"future":true,"kind":`, 1),
		body + "\nextra", body + body,
		activityStart + "null" + markerEnd,
		strings.Replace(body, markerEnd, " {}"+markerEnd, 1),
	} {
		if _, err := decodeActivity(apiComment{ID: 42, Body: invalid}, "gh-1"); err == nil {
			t.Errorf("accepted %s", invalid)
		}
	}
	for _, input := range []activityData{
		{Kind: "comment", SessionID: "ses", OperationID: "op", Message: " "},
		{Kind: "handoff", SessionID: "ses", OperationID: "op", Done: []string{" "}},
		{Kind: "log", SessionID: "ses", OperationID: "op", Message: "x", LogType: "security"},
		{Kind: "comment", SessionID: "ses", OperationID: "op", Message: activityPrefix},
	} {
		if _, err := renderActivity(input); err == nil {
			t.Errorf("accepted %+v", input)
		}
	}
}

func TestActivityPaginationAndDuplicateDetection(t *testing.T) {
	body, _ := renderActivity(activityData{Kind: "log", SessionID: "ses", OperationID: "op", Message: "progress", LogType: models.LogTypeProgress})
	pages := [][]apiComment{{{ID: 1, Body: "native"}}, {{ID: 2, Body: body}}}
	client := &Client{stateLabels: fixtureStateLabels(), repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "repos/owner/repo/issues/9") {
			return []byte(`{"number":9,"state":"open"}`), nil
		}
		if !slices.Contains(args, "--paginate") || !slices.Contains(args, "--slurp") || !slices.Contains(args, "repos/owner/repo/issues/9/comments?per_page=100") {
			t.Fatalf("args %v", args)
		}
		return json.Marshal(pages)
	}}
	got, err := client.ListActivity(context.Background(), "#9")
	if err != nil || len(got) != 2 || !got[0].Native || got[1].LogType != models.LogTypeProgress {
		t.Fatalf("got %+v err %v", got, err)
	}
	pages = append(pages, []apiComment{{ID: 3, Body: body}})
	if _, err := client.ListActivity(context.Background(), "9"); err == nil || !strings.Contains(err.Error(), "duplicate activity operation") {
		t.Fatalf("duplicate operation: %v", err)
	}
	pages = [][]apiComment{{{ID: 1, Body: "native"}}, {{ID: 1, Body: "native"}}}
	if _, err := client.ListActivity(context.Background(), "9"); err == nil || !strings.Contains(err.Error(), "pagination repeated") {
		t.Fatalf("duplicate page: %v", err)
	}
}

func TestActivityWriteFailureNeverRetries(t *testing.T) {
	for _, mode := range []string{"success", "timeout", "invalid-json", "wrong-body", "pull-request"} {
		t.Run(mode, func(t *testing.T) {
			writes := 0
			client := &Client{stateLabels: fixtureStateLabels(), repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") {
					if mode == "pull-request" {
						return []byte(`{"number":9,"state":"open","pull_request":{}}`), nil
					}
					return []byte(`{"number":9,"state":"open"}`), nil
				}
				writes++
				if !slices.Contains(args, "POST") || !slices.Contains(args, "--input") {
					t.Fatalf("args %v", args)
				}
				if mode == "timeout" {
					return nil, errors.New("timed out")
				}
				if mode == "invalid-json" {
					return []byte("{"), nil
				}
				fields := map[string]string{}
				if err := json.Unmarshal(payload, &fields); err != nil {
					t.Fatal(err)
				}
				if mode == "wrong-body" {
					fields["body"] = "wrong"
				}
				return json.Marshal(apiComment{ID: 12, Body: fields["body"]})
			}}
			got, err := client.AppendActivity(context.Background(), "9", models.Activity{Kind: "comment", SessionID: "ses", OperationID: "known-op", Message: "message"})
			if mode == "success" {
				if err != nil || got.OperationID != "known-op" {
					t.Fatalf("%+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("expected error")
			} else if mode != "pull-request" && !strings.Contains(err.Error(), "known-op") {
				t.Fatalf("missing recovery identity: %v", err)
			}
			want := 1
			if mode == "pull-request" {
				want = 0
			}
			if writes != want {
				t.Fatalf("writes=%d", writes)
			}
		})
	}
}
