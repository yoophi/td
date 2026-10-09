package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/marcus/td/internal/models"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStatisticsVisibleIssuesAndCalendarBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.FixedZone("KST", 9*3600))
	midnight := time.Date(2026, 10, 9, 0, 0, 0, 0, now.Location())
	closed := now.Add(-time.Hour)
	records := []Record{
		{Number: 1, Issue: models.Issue{ID: "gh-1", Status: models.StatusOpen, Type: models.TypeTask, Priority: models.PriorityP1, Points: 2, CreatedAt: now.AddDate(0, 0, -8)}},
		{Number: 2, Issue: models.Issue{ID: "gh-2", Status: models.StatusClosed, Type: models.TypeTask, Priority: models.PriorityP2, Points: 3, CreatedAt: midnight, ClosedAt: &closed}},
		{Number: 3, Issue: models.Issue{ID: "gh-3", Status: models.StatusInProgress, Type: models.TypeEpic, Priority: models.PriorityP2, Points: 4, CreatedAt: midnight.AddDate(0, 0, 1)}},
		{Number: 4, Issue: models.Issue{ID: "gh-4", Status: models.StatusOpen, Type: models.TypeTask, Priority: models.PriorityP3, Points: 1, CreatedAt: now.AddDate(0, 0, -7)}},
		{Number: 5, Issue: models.Issue{ID: "gh-5", DeletedAt: &now, Points: 100, CreatedAt: now}},
	}
	s, err := aggregateIssueStats(records, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 4 || s.TotalPoints != 10 || s.AvgPointsPerTask != 2.5 || s.CompletionRate != 0.25 || s.CreatedToday != 1 || s.CreatedThisWeek != 3 || s.OldestOpen.ID != "gh-1" || s.NewestTask.ID != "gh-3" || s.LastClosed.ID != "gh-2" || s.ByPriority[models.PriorityP2] != 2 || s.ByType[models.TypeEpic] != 1 {
		t.Fatalf("%+v", s)
	}
	empty, err := aggregateIssueStats(nil, now)
	if err != nil || empty.Total != 0 || empty.CompletionRate != 0 || empty.OldestOpen != nil || empty.ByStatus == nil {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	for _, points := range [][]int{{-1}, {int(^uint(0) >> 1), 1}} {
		invalid := []Record{}
		for _, p := range points {
			invalid = append(invalid, Record{Issue: models.Issue{Points: p}})
		}
		if _, err := aggregateIssueStats(invalid, now); err == nil {
			t.Fatal("invalid points accepted")
		}
	}
}

func TestStatisticsRetainsDeletedActivityAndFailsWithoutPartialTotals(t *testing.T) {
	for _, mode := range []string{"success", "permission", "malformed", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			c, issues, _ := hierarchyFixture(t)
			now := time.Now().UTC()
			body, err := encodeBody("", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{DeletedAt: &now}})
			if err != nil {
				t.Fatal(err)
			}
			issues[3]["body"] = body
			base := c.run
			c.run = func(ctx context.Context, bin string, input []byte, args ...string) ([]byte, error) {
				endpoint := args[5]
				if strings.Contains(endpoint, "/comments?") {
					if mode == "permission" {
						return nil, errors.New("permission denied")
					}

					comments := []apiComment{}
					for n := 1; n <= 3; n++ {
						id := int64(n)
						kind, session := "log", "ses-b"
						if n == 2 {
							session = "ses-a"
						}
						if n == 3 {
							kind = "handoff"
						}
						data := activityData{Kind: kind, OperationID: fmt.Sprintf("op-%d", n), SessionID: session}
						if kind == "log" {
							data.Message = "progress"
							data.LogType = models.LogTypeProgress
						} else {
							data.Done = []string{"done"}
						}
						text, err := renderActivity(data)
						if err != nil {
							return nil, err
						}
						if mode == "malformed" {
							text = activityPrefix + " invalid"
						}
						if mode == "duplicate" {
							id = 1
						}
						issueURL := fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", n)
						comments = append(comments, apiComment{ID: id, IssueURL: issueURL, Body: text}, apiComment{ID: int64(n + 100), IssueURL: issueURL, Body: "native comment"})
					}
					return json.Marshal([][]apiComment{comments})
				}
				return base(ctx, bin, input, args...)
			}
			s, err := c.ExtendedStats(context.Background(), now)
			if mode != "success" {
				if err == nil || s != nil {
					t.Fatalf("partial success: %+v %v", s, err)
				}
				return
			}
			if err != nil || s.Total != 2 || s.TotalLogs != 2 || s.TotalHandoffs != 1 || s.MostActiveSession != "ses-a" {
				t.Fatalf("%+v %v", s, err)
			}
			if _, err := c.ListActivity(context.Background(), "3"); err == nil {
				t.Fatal("deleted issue leaked through ordinary activity reads")
			}
		})
	}
}

func TestDistinctGitHubLabelsUsesVisibleClosedIssuesAndLiteralCommas(t *testing.T) {
	c, issues, _ := hierarchyFixture(t)
	issues[1]["labels"] = []map[string]string{{"name": " z "}, {"name": "a,b"}, {"name": "td:in_progress"}}
	issues[2]["state"] = "closed"
	issues[2]["labels"] = []map[string]string{{"name": "closed"}, {"name": "a,b"}}
	now := time.Now().UTC()
	body, err := encodeBody("", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{DeletedAt: &now}})
	if err != nil {
		t.Fatal(err)
	}
	issues[3]["body"] = body
	issues[3]["labels"] = []map[string]string{{"name": "hidden"}}
	labels, err := c.DistinctLabels(context.Background())
	if err != nil || !slices.Equal(labels, []string{"a,b", "closed", "td:in_progress", "z"}) {
		t.Fatalf("%v %v", labels, err)
	}
	c.run = func(context.Context, string, []byte, ...string) ([]byte, error) { return nil, errors.New("offline") }
	if labels, err := c.DistinctLabels(context.Background()); err == nil || labels != nil {
		t.Fatalf("partial labels: %v %v", labels, err)
	}
}
