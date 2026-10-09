package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestImportPreparedIntentRecoversBothSidesOfInterruptedApply(t *testing.T) {
	source := importTestItem("td-source")
	source.SourceNamespace = "project"
	digest, _ := source.Digest()
	body, err := encodeBody("", metadata{OperationID: importOperationID(source.SourceKey()), Type: source.Issue.Type, Priority: source.Issue.Priority})
	if err != nil {
		t.Fatal(err)
	}
	before := apiIssue{Number: 1, State: "open", Title: "Before", Body: body}
	after := before
	after.Title = source.Issue.Title
	beforeBasis, _ := importTargetBasis(before)
	afterBasis, _ := importTargetBasis(after)
	intent := importIntent{Version: 1, SourceKey: source.SourceKey(), SourceDigest: digest, TargetID: "gh-1", BeforeBasis: beforeBasis, AfterBasis: afterBasis}
	current := before
	comments := []apiComment{}
	posts := 0
	lost := errors.New("HTTP response lost after intent creation")
	c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		endpoint := args[5]
		if slices.Contains(args, "POST") && endpoint == "repos/owner/repo/issues/1/comments" {
			posts++
			var fields map[string]string
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			comments = append(comments, apiComment{ID: 1, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: fields["body"]})
			return nil, lost
		}
		if !slices.Contains(args, "GET") || payload != nil {
			t.Fatalf("unexpected issue mutation or retry: %v", args)
		}
		switch endpoint {
		case "repos/owner/repo/issues/1":
			return json.Marshal(current)
		case "repos/owner/repo/issues?state=all&per_page=100":
			return json.Marshal([][]apiIssue{{current}})
		case "repos/owner/repo/issues/comments?per_page=100":
			return json.Marshal([][]apiComment{comments})
		default:
			t.Fatalf("unexpected endpoint %s", endpoint)
			return nil, nil
		}
	}}
	err = c.prepareImport(context.Background(), intent)
	if !errors.Is(err, lost) || posts != 1 || !strings.Contains(err.Error(), "PATCH not attempted") {
		t.Fatal("uncertain intent POST retried or original cause lost", err, posts)
	}
	check := func(state string) {
		t.Helper()
		plan, err := c.PlanImport(context.Background(), []ImportItem{source}, false)
		if err != nil || plan.Rows[0].Action != "resume" || plan.Rows[0].ResumeState != state || posts != 1 {
			t.Fatal("interrupted apply not recovered", plan, err, posts)
		}
	}
	check("before_apply")
	current = after
	check("after_apply")
	// Completed receipt after the intent takes precedence and makes replay a
	// no-op. The original before-basis is not an excuse to rerun the PATCH.
	history := importSourceProvenanceComments(t, source, "gh-1", 2)
	comments = append(comments, history...)
	receiptBody, err := renderImportReceipt(importReceipt{Version: 1, SourceKey: source.SourceKey(), SourceDigest: digest, TargetID: "gh-1", TargetBasis: afterBasis, SourceParts: len(history)})
	if err != nil {
		t.Fatal(err)
	}
	comments = append(comments, apiComment{ID: 3, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: receiptBody})
	plan, err := c.PlanImport(context.Background(), []ImportItem{source}, false)
	if err != nil || plan.Rows[0].Action != "skip" || plan.Rows[0].ResumeState != "" {
		t.Fatal("completed import resumed a stale intent", plan, err)
	}
	// A newer intent supersedes an older completion, allowing recovery after
	// the new PATCH but before its new completion receipt.
	newSource := source
	newSource.Issue.Title += " changed"
	newDigest, _ := newSource.Digest()
	newAfter := after
	newAfter.Title = newSource.Issue.Title
	newBasis, _ := importTargetBasis(newAfter)
	intent.SourceDigest, intent.BeforeBasis, intent.AfterBasis = newDigest, afterBasis, newBasis
	intentBody, _ := renderImportIntent(intent)
	comments = append(comments, apiComment{ID: 4, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: intentBody})
	current = newAfter
	plan, err = c.PlanImport(context.Background(), []ImportItem{newSource}, false)
	if err != nil || plan.Rows[0].ResumeState != "after_apply" {
		t.Fatal("old completion incorrectly conflicted with newer interrupted intent", plan, err)
	}
	current.Title = "External edit"
	for _, force := range []bool{false, true} {
		_, err := c.PlanImport(context.Background(), []ImportItem{newSource}, force)
		var conflict *ConflictError
		if !errors.As(err, &conflict) {
			t.Fatal("external edit accepted during unfinished import", force, err)
		}
	}
	current = newAfter
	if _, err := c.PlanImport(context.Background(), []ImportItem{source}, true); err == nil || !strings.Contains(err.Error(), "original input") {
		t.Fatal("force replaced a pending source version", err)
	}
}

func TestImportPrepareStopsBeforeOrDuringConcurrentEdits(t *testing.T) {
	before := apiIssue{Number: 1, State: "open", Title: "Before"}
	basis, _ := importTargetBasis(before)
	intent := importIntent{Version: 1, SourceKey: "legacy:project:td-source", SourceDigest: strings.Repeat("a", 64), TargetID: "gh-1", BeforeBasis: basis, AfterBasis: strings.Repeat("b", 64)}
	for _, when := range []string{"before", "during"} {
		t.Run(when, func(t *testing.T) {
			current := before
			if when == "before" {
				current.Title = "External"
			}
			posts := 0
			c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") {
					return json.Marshal(current)
				}
				if !slices.Contains(args, "POST") || args[5] != "repos/owner/repo/issues/1/comments" {
					t.Fatal("issue PATCH attempted", args)
				}
				posts++
				current.Title = "External during prepare"
				var fields map[string]string
				if err := json.Unmarshal(payload, &fields); err != nil {
					t.Fatal(err)
				}
				return json.Marshal(apiComment{ID: 1, Body: fields["body"]})
			}}
			err := c.prepareImport(context.Background(), intent)
			var conflict *ConflictError
			if !errors.As(err, &conflict) || (when == "before" && posts != 0) || (when == "during" && posts != 1) {
				t.Fatal("concurrent edit not detected at prepare boundary", when, posts, err)
			}
		})
	}
}

func TestImportCompletionPublicationRecoverUncertainResponse(t *testing.T) {
	for _, loseResponse := range []bool{false, true} {
		name := "success"
		if loseResponse {
			name = "response_lost"
		}
		t.Run(name, func(t *testing.T) {
			source := importTestItem("td-source")
			source.SourceNamespace = "project"
			digest, _ := source.Digest()
			body, err := encodeBody("", metadata{OperationID: importOperationID(source.SourceKey()), Type: source.Issue.Type, Priority: source.Issue.Priority})
			if err != nil {
				t.Fatal(err)
			}
			item := apiIssue{Number: 1, State: "open", Title: source.Issue.Title, Body: body}
			basis, _ := importTargetBasis(item)
			comments := importSourceProvenanceComments(t, source, "gh-1", 1)
			receipt := importReceipt{Version: 1, SourceKey: source.SourceKey(), SourceDigest: digest, TargetID: "gh-1", TargetBasis: basis, SourceParts: len(comments)}
			posts := 0
			failure := errors.New("fixture completion response lost")
			c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "POST") && args[5] == "repos/owner/repo/issues/1/comments" {
					posts++
					var fields map[string]string
					if err := json.Unmarshal(payload, &fields); err != nil {
						t.Fatal(err)
					}
					comment := apiComment{ID: int64(len(comments) + 1), Body: fields["body"], IssueURL: "https://api.github.com/repos/owner/repo/issues/1"}
					comments = append(comments, comment)
					if loseResponse {
						return nil, failure
					}
					return json.Marshal(comment)
				}
				if !slices.Contains(args, "GET") || payload != nil {
					t.Fatal("unexpected completion mutation", args)
				}
				switch args[5] {
				case "repos/owner/repo/issues/1":
					return json.Marshal(item)
				case "repos/owner/repo/issues?state=all&per_page=100":
					return json.Marshal([][]apiIssue{{item}})
				case "repos/owner/repo/issues/comments?per_page=100":
					return json.Marshal([][]apiComment{comments})
				default:
					t.Fatal("unexpected endpoint", args)
					return nil, nil
				}
			}}
			err = c.completeImport(context.Background(), receipt)
			if (loseResponse && !errors.Is(err, failure)) || (!loseResponse && err != nil) || posts != 1 {
				t.Fatal("completion cause lost or POST repeated", err, posts)
			}
			plan, err := c.PlanImport(context.Background(), []ImportItem{source}, false)
			if err != nil || plan.Rows[0].Action != "skip" || posts != 1 {
				t.Fatal("completion not recovered by fresh plan", plan, err, posts)
			}
		})
	}
}
