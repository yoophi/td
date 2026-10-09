package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/query"
	"github.com/spf13/cobra"
)

func TestGitHubScheduleSQLiteFilterAndQueryParity(t *testing.T) {
	database, err := db.Initialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	now := time.Now()
	today, yesterday, tomorrow, third, fourth := now.Format("2006-01-02"), now.AddDate(0, 0, -1).Format("2006-01-02"), now.AddDate(0, 0, 1).Format("2006-01-02"), now.AddDate(0, 0, 3).Format("2006-01-02"), now.AddDate(0, 0, 4).Format("2006-01-02")
	issues := []models.Issue{{Title: "unscheduled"}, {Title: "due today", DueDate: &today}, {Title: "overdue", DueDate: &yesterday}, {Title: "due third day", DueDate: &third}, {Title: "due fourth day", DueDate: &fourth}, {Title: "future deferred", DeferUntil: &tomorrow}, {Title: "surfaced", DeferUntil: &today, DeferCount: 1}, {Title: "closed overdue", Status: models.StatusClosed, DueDate: &yesterday}}
	records := []ghstore.Record{}
	for _, issue := range issues {
		if err := database.CreateIssue(&issue); err != nil {
			t.Fatal(err)
		}
		records = append(records, ghstore.Record{Issue: issue})
	}
	titles := func(rows []models.Issue) map[string]bool {
		m := map[string]bool{}
		for _, r := range rows {
			m[r.Title] = true
		}
		return m
	}
	compare := func(a, b []models.Issue) {
		t.Helper()
		ma, mb := titles(a), titles(b)
		if len(ma) != len(mb) {
			t.Fatal(ma, mb)
		}
		for key := range ma {
			if !mb[key] {
				t.Fatal(ma, mb)
			}
		}
	}
	for _, tc := range []struct {
		filter gitHubScheduleFilter
		opts   db.ListIssuesOptions
	}{{gitHubScheduleFilter{}, db.ListIssuesOptions{ExcludeDeferred: true}}, {gitHubScheduleFilter{all: true}, db.ListIssuesOptions{}}, {gitHubScheduleFilter{deferred: true}, db.ListIssuesOptions{DeferredOnly: true}}, {gitHubScheduleFilter{overdue: true}, db.ListIssuesOptions{OverdueOnly: true}}, {gitHubScheduleFilter{surfacing: true}, db.ListIssuesOptions{SurfacingOnly: true}}, {gitHubScheduleFilter{dueSoon: true}, db.ListIssuesOptions{DueSoonDays: 3}}} {
		a, err := database.ListIssues(tc.opts)
		if err != nil {
			t.Fatal(err)
		}
		b := []models.Issue{}
		for _, r := range records {
			if tc.filter.matches(r.Issue, now) {
				b = append(b, r.Issue)
			}
		}
		compare(a, b)
	}
	snapshot, err := issuestore.NewGitHubQuerySnapshot(context.Background(), records, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{"due < today", "due >= today AND due <= " + third, "defer > today", "defer <= today AND defer_count > 0", "due = NULL"} {
		a, err := query.Execute(database, expression, "", query.ExecuteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := query.Execute(snapshot, expression, "", query.ExecuteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		compare(a, b)
	}
}

func githubScheduleTestDir(t *testing.T) string {
	t.Helper()
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TD_CONTEXT_ID", "schedule-fixture")
	bin := t.TempDir()
	t.Setenv("TD_SCHEDULE_STATE", filepath.Join(bin, "state.json"))
	script := `#!/usr/bin/env python3
import os,sys,json,pathlib
args=sys.argv[1:]
if args[0]=='auth':sys.exit(0)
path=next(a for a in args if a.startswith('repos/owner/repo'));method=args[args.index('--method')+1] if '--method' in args else 'GET'
f=pathlib.Path(os.environ['TD_SCHEDULE_STATE'])
def issue(n,details={}):return {'number':n,'title':'Schedule fixture '+str(n),'state':'closed' if n==3 else 'open','labels':[{'name':'td:closed' if n==3 else 'td:open'}],'body':'<!-- td:issue:v1\n'+json.dumps({'type':'task','priority':'P0' if n==2 else 'P2','points':0,'details':details})+'\n-->'}
state=json.loads(f.read_text()) if f.exists() else {'issues':{'1':issue(1),'2':issue(2,{'defer_until':'2099-01-01'}),'3':issue(3,{'due_date':'2020-01-01','defer_until':'2099-01-01'}),'4':issue(4,{'deleted_at':'2026-01-01T00:00:00Z'}),'5':dict(issue(5),pull_request={})},'comments':[]}
issues=state['issues']
if path=='repos/owner/repo':data={'full_name':'owner/repo','has_issues':True}
elif '/labels?' in path:data=[[{'name':'td:'+v} for v in ['open','in_progress','blocked','in_review','closed']]]
elif path.startswith('repos/owner/repo/issues?'):data=[list(issues.values())[:2],list(issues.values())[2:]]
elif '/comments?' in path:data=[state['comments']]
elif path.endswith('/comments') and method=='POST':
 if os.environ.get('TD_SCHEDULE_FAIL_LOG'):sys.stderr.write('HTTP 403 log denied');sys.exit(1)
 body=json.load(sys.stdin)['body'];data={'id':len(state['comments'])+1,'body':body,'created_at':'2026-10-09T00:00:00Z','updated_at':'2026-10-09T00:00:00Z'};state['comments'].append(data);f.write_text(json.dumps(state))
elif path=='repos/owner/repo/issues' and method=='POST':
 n=str(max(map(int,issues))+1);data=issue(int(n));patch=json.load(sys.stdin);patch['labels']=[{'name':v} for v in patch['labels']];data.update(patch);issues[n]=data;f.write_text(json.dumps(state))
else:
 n=path.split('/issues/')[1]
 if n not in issues:sys.stderr.write('HTTP 404 missing');sys.exit(1)
 if method=='PATCH':
  if os.environ.get('TD_SCHEDULE_FAIL_WRITE'):sys.stderr.write('HTTP 403 write denied');sys.exit(1)
  patch=json.load(sys.stdin)
  if 'labels' in patch:patch['labels']=[{'name':v} for v in patch['labels']]
  issues[n].update(patch);f.write_text(json.dumps(state))
 data=issues[n]
sys.stdout.write('HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'+json.dumps(data))
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestGitHubScheduleCommandsFlagsAndPartialFailure(t *testing.T) {
	dir := githubScheduleTestDir(t)
	execute := func(original *cobra.Command, args ...string) string {
		t.Helper()
		out, err := executeGitHubTest(githubTestCommand(original), append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatal(out, err)
		}
		return out
	}
	read := func(id string) models.Issue {
		t.Helper()
		var r models.Issue
		if err := json.Unmarshal([]byte(execute(showCmd, id)), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	execute(createCmd, "Schedule creation fixture", "--due", "tomorrow", "--defer", "+7d")
	r := read("6")
	if r.DueDate == nil || *r.DueDate != time.Now().AddDate(0, 0, 1).Format("2006-01-02") || r.DeferUntil == nil {
		t.Fatal(r)
	}
	execute(updateCmd, "6", "--due", "", "--defer", "")
	r = read("6")
	if r.DueDate != nil || r.DeferUntil != nil {
		t.Fatal(r)
	}
	execute(dueCmd, "1", "2020-01-01")
	execute(deferCmd, "1", "2020-01-01")
	execute(deferCmd, "1", "2020-01-02")
	execute(deferCmd, "1", "2020-01-02")
	r = read("1")
	if r.DeferCount != 1 {
		t.Fatal("repeat changed count", r)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{{[]string{"--overdue", "--limit", "1"}, "gh-1"}, {[]string{"--surfacing"}, "gh-1"}, {[]string{"--deferred", "--overdue"}, "gh-2"}} {
		var rows []models.Issue
		if err := json.Unmarshal([]byte(execute(listCmd, tc.args...)), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].ID != tc.want {
			t.Fatal(tc, rows)
		}
	}
	for _, tc := range []struct {
		args []string
		want int
	}{{[]string{"--status", "closed"}, 0}, {[]string{"--status", "closed", "--all"}, 1}, {[]string{"--all", "--open"}, 3}} {
		var rows []models.Issue
		if err := json.Unmarshal([]byte(execute(listCmd, tc.args...)), &rows); err != nil || len(rows) != tc.want {
			t.Fatal("status versus all deferral scope", tc, rows, err)
		}
	}
	execute(deferCmd, "1", "--clear")
	execute(dueCmd, "1", "--clear")
	r = read("1")
	if r.DeferCount != 1 || r.DeferUntil != nil || r.DueDate != nil {
		t.Fatal(r)
	}
	t.Setenv("TD_SCHEDULE_FAIL_LOG", "1")
	if _, err := executeGitHubTest(githubTestCommand(deferCmd), "1", "2099-01-01", "--json"); err == nil || !strings.Contains(err.Error(), "schedule was saved") {
		t.Fatal("partial log failure hidden", err)
	}
	if r = read("1"); r.DeferUntil == nil || *r.DeferUntil != "2099-01-01" {
		t.Fatal("saved date lost", r)
	}
	t.Setenv("TD_SCHEDULE_FAIL_LOG", "")
	before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cmd  *cobra.Command
		args []string
	}{{dueCmd, []string{"4", "today"}}, {dueCmd, []string{"5", "today"}}, {dueCmd, []string{"99", "today"}}, {dueCmd, []string{"1", "invalid"}}, {deferCmd, []string{"1", "today", "--clear"}}, {updateCmd, []string{"1", "--due", "2026-02-30", "--title", "Must not write"}}} {
		if _, err := executeGitHubTest(githubTestCommand(tc.cmd), tc.args...); err == nil {
			t.Fatal("invalid schedule accepted", tc.args)
		}
	}
	after, _ := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if string(before) != string(after) {
		t.Fatal("preflight failure wrote")
	}
	t.Setenv("TD_SCHEDULE_FAIL_WRITE", "1")
	if _, err := executeGitHubTest(githubTestCommand(dueCmd), "1", "tomorrow"); err == nil {
		t.Fatal("write denial ignored")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

func TestGitHubScheduleLocalDateBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 1, 0, 0, time.FixedZone("KST", 9*60*60))
	date := "2026-10-10"
	future := "2026-10-11"
	third := "2026-10-13"
	fourth := "2026-10-14"
	i := models.Issue{Status: models.StatusOpen, DueDate: &date, DeferUntil: &date, DeferCount: 1}
	if !(gitHubScheduleFilter{}).matches(i, now) || (gitHubScheduleFilter{deferred: true}).matches(i, now) || (gitHubScheduleFilter{overdue: true}).matches(i, now) || !(gitHubScheduleFilter{surfacing: true}).matches(i, now) {
		t.Fatal("local midnight boundary")
	}
	i.DeferUntil = &future
	if (gitHubScheduleFilter{}).matches(i, now) || !(gitHubScheduleFilter{all: true}).matches(i, now) {
		t.Fatal("default deferral")
	}
	i.DueDate = &third
	if !(gitHubScheduleFilter{dueSoon: true}).matches(i, now) {
		t.Fatal("third day omitted")
	}
	i.DueDate = &fourth
	if (gitHubScheduleFilter{dueSoon: true}).matches(i, now) {
		t.Fatal("fourth day included")
	}
}
