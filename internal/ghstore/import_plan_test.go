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

func importHierarchyFixture(t *testing.T) (*Client, map[int]map[string]any, *int) {
	t.Helper()
	c, issues, writes := hierarchyFixture(t)
	run := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "/issues/comments?") && slices.Contains(args, "GET") {
			return []byte(`[[]]`), nil
		}
		return run(ctx, dir, payload, args...)
	}
	return c, issues, writes
}

func TestImportCreationIdentityRecoversUncertainPOSTWithoutRetry(t *testing.T) {
	item := importTestItem("td-origin")
	operation := importOperationID(item.SourceKey())
	posts := 0
	var created map[string]any
	failure := errors.New("connection lost after fixture creation")
	c := &Client{repo: "owner/import-recovery", stateLabels: fixtureStateLabels(), run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "POST") {
			posts++
			if err := json.Unmarshal(payload, &created); err != nil {
				t.Fatal(err)
			}
			created["number"], created["state"] = 17, "open"
			return nil, failure
		}
		if !strings.Contains(args[5], "/issues?state=all") {
			if strings.Contains(args[5], "/issues/comments?") {
				return []byte(`[[]]`), nil
			}
			t.Fatalf("unexpected recovery request: %v", args)
		}
		data, err := fixtureIssueJSON(created)
		return append(append([]byte("[["), data...), []byte("]]")...), err
	}}
	issue := item.Issue
	issue.Status = models.StatusOpen
	_, err := c.createWithOperationID(context.Background(), &issue, operation)
	if !errors.Is(err, failure) || posts != 1 || !strings.Contains(err.Error(), operation) {
		t.Fatalf("uncertain creation was retried or lost its identity: posts=%d err=%v", posts, err)
	}
	plan, err := c.PlanImport(context.Background(), []ImportItem{item}, false)
	if err != nil || len(plan.Rows) != 1 || plan.Rows[0].TargetID != "gh-17" || plan.Rows[0].Action != "resume" || posts != 1 {
		t.Fatalf("uncertain creation not recovered: %+v %v posts=%d", plan, err, posts)
	}
	r := plan.Rows[0].Observed
	if r == nil || r.PriorityLabelWarning() != "" || r.Priority != item.Issue.Priority {
		t.Fatalf("creation did not preserve priority mirror: %+v", r)
	}
}

func TestImportPlanRecoversCreationIdentityWithoutReusingForeignNumber(t *testing.T) {
	c, issues, writes := importHierarchyFixture(t)
	item := importTestItem("td-local")
	body, err := encodeBody(item.Issue.Description, metadata{OperationID: importOperationID(item.SourceKey()), Type: item.Issue.Type, Priority: item.Issue.Priority})
	if err != nil {
		t.Fatal(err)
	}
	issues[2]["body"] = body
	// The same source ID, even with changed content, must map to the originally
	// allocated target. Resume must not be reported as a completed import.
	item.Issue.Title = "Changed source after interrupted creation"
	plan, err := c.PlanImport(context.Background(), []ImportItem{item}, false)
	if err != nil || len(plan.Rows) != 1 || plan.Rows[0].TargetID != "gh-2" || plan.Rows[0].Action != "resume" || *writes != 0 {
		t.Fatalf("creation mapping lost: %+v %v", plan, err)
	}
	issues[1]["body"] = body
	if _, err := c.PlanImport(context.Background(), []ImportItem{item}, true); err == nil || *writes != 0 {
		t.Fatal("duplicate creation identities accepted", err)
	}
}

func TestImportIdentityNamespaceAndJSONExcludeObservation(t *testing.T) {
	local := importTestItem("gh-1")
	foreign := local
	foreign.GitHub = &ExportedIssue{Repository: "owner/source"}
	if importOperationID(local.SourceKey()) == importOperationID(foreign.SourceKey()) {
		t.Fatal("legacy and GitHub source identities collided")
	}
	otherCase := foreign
	otherCase.GitHub = &ExportedIssue{Repository: "OWNER/SOURCE"}
	if importOperationID(foreign.SourceKey()) != importOperationID(otherCase.SourceKey()) {
		t.Fatal("repository casing changed identity")
	}
	data, err := json.Marshal(ImportPlanRow{OperationID: importOperationID(local.SourceKey()), Observed: &Record{Issue: local.Issue}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil || fields["Observed"] != nil || fields["observed"] != nil {
		t.Fatal("private observation leaked into plan", string(data), err)
	}
}

func TestImportPlanActualRepoMappingsForceAndNoWrites(t *testing.T) {
	c, _, writes := importHierarchyFixture(t)
	r, err := c.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	native := ImportItem{Issue: r.Issue, GitHub: &ExportedIssue{Version: 1, Repository: "owner/repo", Record: *r, NativeState: "open", Activity: []models.Activity{}}}
	local := importTestItem("td-local")
	local.Dependencies = []models.IssueDependency{{DependsOnID: native.Issue.ID, RelationType: "blocks"}}
	// Source-local references cannot cross an unrelated legacy namespace.
	if _, err := c.PlanImport(context.Background(), []ImportItem{native, local}, false); err == nil {
		t.Fatal("cross-namespace reference accepted")
	}
	local.Dependencies = nil
	for _, force := range []bool{false, true} {
		p, err := c.PlanImport(context.Background(), []ImportItem{native, local}, force)
		if err != nil || len(p.Rows) != 2 || p.Rows[0].TargetID != "gh-1" || p.Rows[1].TargetID != "" || p.Rows[1].Action != "create" || *writes != 0 {
			t.Fatal(p, err, *writes)
		}
		action := "skip"
		if force {
			action = "overwrite"
		}
		if p.Rows[0].Action != action {
			t.Fatal(p.Rows)
		}
	}
	native.GitHub.Repository = "another/repo"
	p, err := c.PlanImport(context.Background(), []ImportItem{native}, true)
	if err != nil || p.Rows[0].TargetID != "" || p.Rows[0].Action != "create" || *writes != 0 {
		t.Fatal("foreign issue number reused", p, err)
	}
}
func TestImportMarkdownIDsMultilineDefaultsAndInvalidFields(t *testing.T) {
	data := "# Issues Export\n\n## gh-12: A title\n\n- Status: in_progress\n- Type: feature\n- Priority: P1\n- Points: 3\n- Labels: one, two\n\nline one\nline two\n\n## Anonymous title\n\nbody\n"
	rows, err := ParseImportMarkdown(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Issue.ID != "gh-12" || rows[0].Issue.Description != "line one\nline two" || rows[0].Issue.Points != 3 || rows[1].Issue.Priority != models.PriorityP2 {
		t.Fatal(rows)
	}
	again, err := ParseImportMarkdown(data)
	if err != nil || again[1].SourceKey() != rows[1].SourceKey() {
		t.Fatal("anonymous source identity unstable", err)
	}
	if _, err := ParseImportMarkdown("## Invalid\n- Status: unknown\n"); err == nil {
		t.Fatal("invalid status accepted")
	}
	if _, err := ParseImportMarkdown("not an export"); err == nil {
		t.Fatal("missing headings accepted")
	}
}
