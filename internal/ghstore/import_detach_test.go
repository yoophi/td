package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/marcus/td/internal/models"
)

func importDetachFixture(t *testing.T) (*Client, []ImportItem, map[int]map[string]any, *int, *[]apiComment) {
	t.Helper()
	c, issues, writes, comments := importBatchFixture(t)
	inputs := []ImportItem{}
	for n := 1; n <= 2; n++ {
		oldDetails, wantDetails := IssueDetails{Status: models.StatusOpen}, IssueDetails{Status: models.StatusOpen}
		if n == 1 {
			oldDetails.Dependencies = []string{"gh-2"}
			wantDetails.ParentID = "gh-2"
		} else {
			oldDetails.ParentID = "gh-1"
			wantDetails.Dependencies = []string{"gh-1"}
		}
		meta := metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &oldDetails}
		body, err := encodeBody("", meta)
		if err != nil {
			t.Fatal(err)
		}
		issues[n]["body"] = body
		meta.Details = &wantDetails
		body, err = encodeBody("", meta)
		if err != nil {
			t.Fatal(err)
		}
		native := apiIssue{Number: n, Title: fmt.Sprintf("Fixture %d", n), State: "open", Body: body}
		record, err := native.record()
		if err != nil {
			t.Fatal(err)
		}
		source := ImportItem{Issue: record.Issue, GitHub: &ExportedIssue{Version: 1, Repository: c.repo, Record: *record, Body: body, NativeState: "open", Activity: []models.Activity{}}}
		for _, id := range wantDetails.Dependencies {
			source.Dependencies = append(source.Dependencies, models.IssueDependency{IssueID: record.ID, DependsOnID: id, RelationType: "depends_on"})
		}
		inputs = append(inputs, source)
	}
	return c, inputs, issues, writes, comments
}

func TestImportDetachBothGraphsAndLostResponseRecovery(t *testing.T) {
	for _, lost := range []string{"none", "intent", "first_detach", "second_detach", "final_apply", "complete", "upgrade"} {
		t.Run(lost, func(t *testing.T) {
			c, inputs, _, writes, comments := importDetachFixture(t)
			base := c.run
			failure := errors.New("fixture graph write response lost")
			failed, patches := false, 0
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				data, err := base(ctx, dir, payload, args...)
				if err != nil {
					return data, err
				}
				if slices.Contains(args, "PATCH") {
					patches++
				}
				var fields map[string]string
				if slices.Contains(args, "POST") {
					_ = json.Unmarshal(payload, &fields)
				}
				intent, err := decodeImportIntent(fields["body"])
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := decodeImportReceipt(fields["body"])
				if err != nil {
					t.Fatal(err)
				}
				match := (lost == "intent" || lost == "upgrade") && intent != nil || lost == "first_detach" && patches == 1 && slices.Contains(args, "PATCH") || lost == "second_detach" && patches == 2 && slices.Contains(args, "PATCH") || lost == "final_apply" && patches == 3 && slices.Contains(args, "PATCH") || lost == "complete" && receipt != nil
				if !failed && match {
					failed = true
					return nil, failure
				}
				return data, nil
			}
			result, err := c.executeImport(context.Background(), inputs, true)
			if lost != "none" {
				if !failed || !errors.Is(err, failure) {
					t.Fatal("failure not preserved", result, err)
				}
				if lost == "upgrade" {
					for i := range *comments {
						intent, err := decodeImportIntent((*comments)[i].Body)
						if err != nil {
							t.Fatal(err)
						}
						if intent != nil {
							intent.DetachedBasis = ""
							(*comments)[i].Body, err = renderImportIntent(*intent)
							if err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				result, err = c.executeImport(context.Background(), inputs, true)
			}
			if err != nil {
				t.Fatal(result, err)
			}
			a, err := c.Get(context.Background(), "gh-1")
			if err != nil {
				t.Fatal(err)
			}
			b, err := c.Get(context.Background(), "gh-2")
			if err != nil {
				t.Fatal(err)
			}
			if a.ParentID != "gh-2" || len(a.Details.Dependencies) != 0 || b.ParentID != "" || !slices.Equal(b.Details.Dependencies, []string{"gh-1"}) || *writes != 4 {
				t.Fatal("final graphs wrong or PATCH repeated", a.ParentID, a.Details.Dependencies, b.ParentID, b.Details.Dependencies, *writes)
			}
			if lost == "none" && result.Detached != 2 {
				t.Fatal("detach phase not reported", result)
			}
			saved := len(*comments)
			result, err = c.executeImport(context.Background(), inputs, true)
			if err != nil || result.Skipped != 2 || *writes != 4 || len(*comments) != saved {
				t.Fatal("complete detached batch repeated writes", result, err)
			}
		})
	}
}

func TestImportDetachResumeRejectsExternalEdit(t *testing.T) {
	c, inputs, issues, _, _ := importDetachFixture(t)
	base := c.run
	failure := errors.New("lost detach response")
	failed := false
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if !failed && err == nil && slices.Contains(args, "PATCH") {
			failed = true
			return nil, failure
		}
		return data, err
	}
	if _, err := c.executeImport(context.Background(), inputs, true); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	plan, err := c.PlanImport(context.Background(), inputs, true)
	if err != nil || plan.Rows[0].ResumeState != "after_detach" {
		t.Fatal(plan, err)
	}
	issues[1]["title"] = "External edit after detachment"
	var conflict *ConflictError
	if _, err := c.executeImport(context.Background(), inputs, true); !errors.As(err, &conflict) {
		t.Fatal("external intermediate edit accepted", err)
	}
}
