package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestBodyRoundTripAndNativeIssue(t *testing.T) {
	description := "## Description\n\nUnicode: 한글\n```sh\n$(echo unsafe); `whoami`\n```\n\n"
	meta := metadata{Type: models.TypeBug, Priority: models.PriorityP1, Points: 5, Acceptance: "Keep </script> --> intact\nLine two"}
	body, err := encodeBody(description, meta)
	if err != nil {
		t.Fatal(err)
	}
	got, decoded, managed, err := decodeBody(body)
	if err != nil || !managed || got != description || !reflect.DeepEqual(decoded, meta) {
		t.Fatalf("description=%q meta=%+v managed=%v err=%v", got, decoded, managed, err)
	}
	got, decoded, managed, err = decodeBody(description)
	if err != nil || managed || got != description || decoded.Type != models.TypeTask || decoded.Priority != models.PriorityP2 {
		t.Fatalf("native issue: %+v, %v", decoded, err)
	}
}

func TestUnknownMetadataIsNotOverwritten(t *testing.T) {
	for _, body := range []string{
		markerStart + `null` + markerEnd,
		markerPrefix + "v2\n{}" + markerEnd,
		markerStart + `{"future_field":1}` + markerEnd,
		markerStart + `{"type":"bug","points":4}` + markerEnd,
		markerStart + `{} {}` + markerEnd,
		markerStart + `{` + markerEnd,
		markerStart + `{}` + markerEnd + "\nUser appended content",
		markerStart + `{}` + markerEnd + markerStart + `{}` + markerEnd,
	} {
		if _, _, _, err := decodeBody(body); err == nil {
			t.Errorf("accepted unsafe metadata: %q", body)
		}
	}
}

func TestGitHubLabelsReportPartialSuccess(t *testing.T) {
	record := &Record{Issue: models.Issue{ID: "gh-9"}, URL: "https://github.com/owner/repo/issues/9"}
	err := checkLabels(record, []string{"bug"}, "created")
	if err == nil || !strings.Contains(err.Error(), "gh-9 was created") {
		t.Fatalf("missing partial-success diagnostic: %v", err)
	}
	record.Labels = []string{"BUG", "automation"}
	if err := checkLabels(record, []string{"bug"}, "created"); err != nil {
		t.Fatal(err)
	}
	if err := checkLabels(record, []string{}, "updated"); err == nil {
		t.Fatal("ignored label clearing")
	}
}

func TestGitHubListPaginatesAndExcludesPullRequests(t *testing.T) {
	client := &Client{repo: "owner/project", dir: "/project", run: func(_ context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if dir != "/project" || !slices.Contains(args, "--paginate") || !slices.Contains(args, "--slurp") || !slices.Contains(args, "repos/owner/project/issues?state=all&per_page=100") || payload != nil {
			t.Fatalf("unexpected request: %s %v %s", dir, args, payload)
		}
		return []byte(`[[{"number":1,"title":"One","state":"open"},{"number":2,"pull_request":{},"body":"<!-- td:issue:malformed"}],[{"number":3,"title":"Three","state":"closed"}]]`), nil
	}}
	issues, err := client.List(context.Background(), true)
	if err != nil || len(issues) != 2 || issues[0].ID != "gh-1" || issues[1].ID != "gh-3" {
		t.Fatalf("issues=%+v err=%v", issues, err)
	}
}

func TestGitHubCreateUsesJSONStdin(t *testing.T) {
	issue := &models.Issue{Title: "Title $(echo injected) `id`", Description: "Body\nwith quotes \" and Unicode 한글", Type: models.TypeFeature, Priority: models.PriorityP0, Points: 8}
	client := &Client{repo: "owner/project", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		if !slices.Contains(args, "POST") || !slices.Contains(args, "--input") || slices.Contains(args, issue.Title) {
			t.Fatalf("unsafe request: %v", args)
		}
		var fields map[string]any
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["title"] != issue.Title {
			t.Fatalf("title changed: %v", fields)
		}
		description, meta, _, err := decodeBody(fields["body"].(string))
		if err != nil || description != issue.Description || meta.Points != 8 {
			t.Fatalf("bad body: %v %v", fields, err)
		}
		fields["number"], fields["state"] = 1, "open"
		return json.Marshal(fields)
	}}
	result, err := client.Create(context.Background(), issue)
	if err != nil || result.ID != "gh-1" || result.Description != issue.Description {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGitHubPatchOnlyChangesSpecifiedFields(t *testing.T) {
	for _, action := range []string{"title", "body", "clear-labels", "close"} {
		t.Run(action, func(t *testing.T) {
			body, _ := encodeBody("Native description\n", metadata{Type: models.TypeBug, Priority: models.PriorityP1, Points: 3, Acceptance: "Keep acceptance"})
			response := map[string]any{"number": 12, "state": "open", "title": "Original", "body": body, "labels": []map[string]string{{"name": "keep-me"}}}
			change := Changes{}
			switch action {
			case "title":
				title := "Renamed"
				change.Title = &title
			case "body":
				description := "Appended"
				change.Description = &description
				change.Append = true
			case "clear-labels":
				labels := []string{}
				change.Labels = &labels
			case "close":
				status := models.StatusClosed
				change.Status = &status
			}
			calls := 0
			client := &Client{repo: "owner/project", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
				calls++
				if slices.Contains(args, "GET") {
					if !slices.Contains(args, "GET") {
						t.Fatal(args)
					}
					return json.Marshal(response)
				}
				var patch map[string]any
				if err := json.Unmarshal(payload, &patch); err != nil {
					t.Fatal(err)
				}
				if len(patch) != 1 || !slices.Contains(args, "PATCH") {
					t.Fatalf("unexpected fields: %v", patch)
				}
				switch action {
				case "title":
					if patch["title"] != "Renamed" {
						t.Fatal(patch)
					}
				case "body":
					desc, meta, _, err := decodeBody(patch["body"].(string))
					if err != nil || desc != "Native description\n\n\nAppended" || meta.Acceptance != "Keep acceptance" {
						t.Fatalf("%q %+v %v", desc, meta, err)
					}
				case "clear-labels":
					if len(patch["labels"].([]any)) != 0 {
						t.Fatal(patch)
					}
				case "close":
					if patch["state"] != "closed" {
						t.Fatal(patch)
					}
				}
				for k, v := range patch {
					response[k] = v
				}
				return json.Marshal(response)
			}}
			if _, err := client.Update(context.Background(), "gh-12", change); err != nil {
				t.Fatal(err)
			}
			if calls != 4 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestGitHubRefusesPRAndDoesNotRetryFailedWrites(t *testing.T) {
	calls := 0
	client := &Client{run: func(context.Context, string, []byte, ...string) ([]byte, error) {
		calls++
		return []byte(`{"number":5,"state":"open","pull_request":{}}`), nil
	}}
	title := "Rename"
	if _, err := client.Update(context.Background(), "5", Changes{Title: &title}); err == nil || !strings.Contains(err.Error(), "pull request") || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	calls = 0
	client.run = func(context.Context, string, []byte, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("network interrupted")
	}
	_, err := client.Create(context.Background(), &models.Issue{Title: "Example title", Type: models.TypeTask, Priority: models.PriorityP2})
	if err == nil || calls != 1 || !strings.Contains(err.Error(), "inspect the issue before retrying") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
