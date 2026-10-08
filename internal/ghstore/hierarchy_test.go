package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func hierarchyFixture(t *testing.T) (*Client, map[int]map[string]any, *int) {
	t.Helper()
	issues := map[int]map[string]any{}
	for n := 1; n <= 3; n++ {
		issues[n] = map[string]any{"number": n, "state": "open", "title": fmt.Sprintf("Fixture %d", n)}
	}
	writes := 0
	client := &Client{repo: "owner/repo", stateLabels: fixtureStateLabels(), run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		endpoint := args[5]
		if strings.Contains(endpoint, "?state=") {
			page := []json.RawMessage{}
			for n := len(issues); n > 0; n-- {
				if issue, ok := issues[n]; ok {
					data, err := fixtureIssueJSON(issue)
					if err != nil {
						return nil, err
					}
					page = append(page, data)
				}
			}
			return json.Marshal([][]json.RawMessage{page})
		}
		n, err := strconv.Atoi(strings.TrimPrefix(endpoint, "repos/owner/repo/issues/"))
		if slices.Contains(args, "POST") {
			n = len(issues) + 1
			issues[n] = map[string]any{"number": n, "state": "open"}
		} else if err != nil {
			return nil, fmt.Errorf("unexpected endpoint %s", endpoint)
		}
		issue, ok := issues[n]
		if !ok {
			return nil, fmt.Errorf("HTTP 404")
		}
		if slices.Contains(args, "PATCH") || slices.Contains(args, "POST") {
			writes++
			var fields map[string]any
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			for key, value := range fields {
				issue[key] = value
			}
		}
		return fixtureIssueJSON(issue)
	}}
	return client, issues, &writes
}

func TestHierarchyParentMutationRejectsCyclesAndAllowsDetach(t *testing.T) {
	client, _, writes := hierarchyFixture(t)
	parent := "gh-1"
	r, err := client.Update(context.Background(), "2", Changes{ParentID: &parent})
	if err != nil || r.ParentID != parent {
		t.Fatalf("%+v %v", r, err)
	}
	parent = "gh-2"
	if _, err := client.Update(context.Background(), "3", Changes{ParentID: &parent}); err != nil {
		t.Fatal(err)
	}
	before := *writes
	for _, bad := range []string{"gh-1", "gh-3", "gh-999", "2"} {
		if _, err := client.Update(context.Background(), "1", Changes{ParentID: &bad}); err == nil || *writes != before {
			t.Fatalf("accepted invalid parent %s: %v", bad, err)
		}
	}
	descendants, err := client.Descendants(context.Background(), "1")
	if err != nil || len(descendants) != 2 || descendants[0].ID != "gh-2" || descendants[1].ID != "gh-3" {
		t.Fatalf("%+v %v", descendants, err)
	}
	empty := ""
	r, err = client.Update(context.Background(), "2", Changes{ParentID: &empty})
	if err != nil || r.ParentID != "" {
		t.Fatalf("detach: %+v %v", r, err)
	}
	descendants, err = client.Descendants(context.Background(), "1")
	if err != nil || len(descendants) != 0 {
		t.Fatalf("detached descendants: %+v %v", descendants, err)
	}
}

func TestCreateParentAndAncestorConflicts(t *testing.T) {
	client, _, _ := hierarchyFixture(t)
	r, err := client.Create(context.Background(), &models.Issue{Title: "Child issue fixture", Type: models.TypeTask, Priority: models.PriorityP2, ParentID: "gh-1"})
	if err != nil || r.ParentID != "gh-1" {
		t.Fatalf("%+v %v", r, err)
	}
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			client, issues, writes := hierarchyFixture(t)
			base := client.run
			reads := 0
			client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") && slices.Contains(args, "repos/owner/repo/issues/1") {
					reads++
					if (!after && reads == 2) || (after && reads == 3) {
						issues[1]["title"] = "Concurrent parent change"
					}
				}
				return base(ctx, dir, payload, args...)
			}
			parent := "gh-1"
			_, err := client.Update(context.Background(), "2", Changes{ParentID: &parent})
			var conflict *ConflictError
			want := 0
			if after {
				want = 1
			}
			if !errors.As(err, &conflict) || conflict.AfterWrite != after || *writes != want {
				t.Fatalf("writes=%d error=%v", *writes, err)
			}
		})
	}
}

func TestHierarchyRejectsCorruptCyclesAndDuplicatePages(t *testing.T) {
	client, issues, writes := hierarchyFixture(t)
	for id, parent := range map[int]string{1: "gh-2", 2: "gh-1"} {
		body, err := encodeBody("Fixture", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{ParentID: parent}})
		if err != nil {
			t.Fatal(err)
		}
		issues[id]["body"] = body
	}
	if _, err := client.Descendants(context.Background(), "1"); err == nil {
		t.Fatal("cycle accepted")
	}
	if _, err := client.Create(context.Background(), &models.Issue{Title: "Should not be created", Type: models.TypeTask, Priority: models.PriorityP2, ParentID: "gh-1"}); err == nil || *writes != 0 {
		t.Fatalf("corrupt ancestry accepted: %v", err)
	}
	row := Record{Issue: models.Issue{ID: "gh-1"}, Number: 1}
	if _, err := descendantsFromRecords([]Record{row, row}, "gh-1"); err == nil {
		t.Fatal("duplicate page accepted")
	}
}
