package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func TestListPointsAndTimestampSQLiteGitHubParity(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	conn, err := db.OpenSQLite(filepath.Join(dir, ".todos", "issues.db"), db.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	issues := []models.Issue{}
	for n, points := range []int{0, 1, 3, 8} {
		i := models.Issue{Title: "point fixture", Points: points}
		if err := database.CreateIssue(&i); err != nil {
			t.Fatal(err)
		}
		stamp := time.Date(2026, 10, 9+n, 0, 0, 0, 0, time.UTC)
		var closed any
		if n%2 == 0 {
			closed = stamp
		}
		if _, err := conn.Exec("UPDATE issues SET created_at=?, updated_at=?, closed_at=?, implementer_session=?, reviewer_session=? WHERE id=?", stamp, stamp, closed, "synthetic-worker", "synthetic-reviewer", i.ID); err != nil {
			t.Fatal(err)
		}
		i2, err := database.GetIssue(i.ID)
		if err != nil {
			t.Fatal(err)
		}
		issues = append(issues, *i2)
	}
	check := func(opts db.ListIssuesOptions, f gitHubListFilters) {
		t.Helper()
		rows, err := database.ListIssues(opts)
		if err != nil {
			t.Fatal(err)
		}
		a, b := []string{}, []string{}
		for _, r := range rows {
			a = append(a, r.ID)
		}
		for _, i := range issues {
			if f.matches(i) {
				b = append(b, i.ID)
			}
		}
		sort.Strings(a)
		sort.Strings(b)
		if !reflect.DeepEqual(a, b) {
			t.Fatal(opts, a, b)
		}
	}
	for _, raw := range []string{"0", "<=0", ">=0", "1", "1-3", ">=3", "<=3", "0-8"} {
		r, err := parseListPointsRange(raw)
		if err != nil {
			t.Fatal(err)
		}
		opts := db.ListIssuesOptions{}
		if r.min != nil {
			opts.PointsMin = *r.min
		}
		if r.max != nil {
			opts.PointsMax = *r.max
			opts.PointsZero = *r.max == 0
		}
		check(opts, gitHubListFilters{points: r})
	}
	for _, raw := range []string{"2026-10-09", "after:2026-10-10", "before:2026-10-10", "2026-10-09..2026-10-11", "2026-10-10..", "..2026-10-10"} {
		r, err := parseListDateRange(raw)
		if err != nil {
			t.Fatal(err)
		}
		check(db.ListIssuesOptions{CreatedAfter: r.after, CreatedBefore: r.before}, gitHubListFilters{created: r})
		check(db.ListIssuesOptions{UpdatedAfter: r.after, UpdatedBefore: r.before}, gitHubListFilters{updated: r})
		check(db.ListIssuesOptions{ClosedAfter: r.after, ClosedBefore: r.before}, gitHubListFilters{closed: r})
	}
	check(db.ListIssuesOptions{Implementer: "synthetic-worker", Reviewer: "synthetic-reviewer"}, gitHubListFilters{implementer: "synthetic-worker", reviewer: "synthetic-reviewer"})
}

func TestGitHubListFilterCLIValidationAndZero(t *testing.T) {
	dir := githubScheduleTestDir(t)
	for _, args := range [][]string{{"--all", "--points", "0"}, {"--status", "ALL"}, {"--status", "review"}, {"--implementer", "missing"}, {"--created", "after:2026-10-01"}, {"--mine"}, {"--closed", "before:2026-10-01"}} {
		out, _, err := executeGitHubReadTest(listCmd, append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(args, out, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{{"--points", "3oops"}, {"--points", "-1"}, {"--points", "8-3"}, {"--created", "2026-02-30"}, {"--updated", ".."}, {"--closed", "2026-10-10..2026-10-09"}} {
		out, _, err := executeGitHubReadTest(listCmd, args...)
		if err == nil || out != "" || strings.Contains(err.Error(), "gh CLI") {
			t.Fatal(args, out, err)
		}
	}
}

type syntheticReviewObserver struct {
	facts map[string]*ghstore.MonitorReviewFacts
	err   error
}

func (r syntheticReviewObserver) ObserveMonitorReview(_ context.Context, record *ghstore.Record, _ string) (*ghstore.MonitorReviewFacts, error) {
	return r.facts[record.ID], r.err
}

func TestGitHubReviewBucketsPolicyAndFailures(t *testing.T) {
	rows := []ghstore.Record{{Issue: models.Issue{ID: "self", Status: models.StatusInReview, ImplementerSession: "synthetic-actor"}}, {Issue: models.Issue{ID: "other", Status: models.StatusInReview, ImplementerSession: "synthetic-worker"}}, {Issue: models.Issue{ID: "approved", Status: models.StatusInReview, ImplementerSession: "synthetic-worker"}}}
	f := syntheticReviewObserver{facts: map[string]*ghstore.MonitorReviewFacts{"self": {Fresh: true, ImplementationInvolved: true, AnyInvolved: true}, "other": {Fresh: true}, "approved": {Fresh: true, ActiveApproval: true}}}
	for _, mode := range []reviewpolicy.Mode{reviewpolicy.ModeStrict, reviewpolicy.ModeBalanced, reviewpolicy.ModeDelegated, reviewpolicy.ModeTrusted} {
		a, r, err := readGitHubReviewBuckets(context.Background(), f, rows, "synthetic-actor", mode)
		if err != nil || !a["other"] || a["self"] != (mode == reviewpolicy.ModeTrusted) || a["approved"] || r["approved"] != (mode == reviewpolicy.ModeTrusted || mode == reviewpolicy.ModeDelegated) {
			t.Fatal(mode, a, r, err)
		}
	}
	f.facts["other"].Fresh = false
	if a, r, err := readGitHubReviewBuckets(context.Background(), f, rows, "synthetic-actor", reviewpolicy.ModeTrusted); err == nil || a != nil || r != nil {
		t.Fatal(a, r, err)
	}
	f.err = errors.New("permission denied")
	if _, _, err := readGitHubReviewBuckets(context.Background(), f, rows, "synthetic-actor", reviewpolicy.ModeTrusted); err == nil {
		t.Fatal("read failure ignored")
	}
}

func TestSQLiteReviewListApprovalScopeBeforeLimit(t *testing.T) {
	database, actor := setupReadJSONTest(t)
	approved := newReadJSONIssue(t, database, "Approved high priority fixture", models.StatusInReview, "synthetic-other")
	approved.Priority = models.PriorityP0
	if err := database.UpdateIssue(approved); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateIssueReview(db.NewReview{IssueID: approved.ID, ReviewerSession: "synthetic-reviewer", Decision: reviewpolicy.DecisionApproved}); err != nil {
		t.Fatal(err)
	}
	awaiting := newReadJSONIssue(t, database, "Awaiting fixture", models.StatusInReview, "synthetic-other")
	opts := db.ListIssuesOptions{ReviewableBy: actor, ReviewPolicyMode: string(reviewpolicy.ModeTrusted), Limit: 1}
	rows, err := listSQLiteReviewableIssues(database, opts, false)
	if err != nil || len(rows) != 1 || rows[0].ID != awaiting.ID {
		t.Fatal(rows, err)
	}
	rows, err = listSQLiteReviewableIssues(database, opts, true)
	if err != nil || len(rows) != 1 || rows[0].ID != approved.ID {
		t.Fatal(rows, err)
	}
}
