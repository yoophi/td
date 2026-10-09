package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type formCreateFixture struct {
	monitorDataFixture
	calls             []string
	created           models.Issue
	root              *ghstore.Record
	createErr, depErr error
	getErr            error
	deleted           bool
}

func (f *formCreateFixture) Get(_ context.Context, id string) (*ghstore.Record, error) {
	f.calls = append(f.calls, "get:"+id)
	r := &ghstore.Record{Issue: models.Issue{ID: id}}
	if f.deleted {
		now := time.Now()
		r.DeletedAt = &now
	}
	return r, f.getErr
}
func (f *formCreateFixture) Create(_ context.Context, issue *models.Issue) (*ghstore.Record, error) {
	f.calls = append(f.calls, "create")
	f.created = *issue
	f.root = &ghstore.Record{Issue: *issue}
	f.root.ID = "gh-10"
	return f.root, f.createErr
}
func (f *formCreateFixture) ChangeDependencyObserved(_ context.Context, root *ghstore.Record, id string, add bool, actor string) (*ghstore.Record, error) {
	if root != f.root || !add || actor != "actual-monitor" {
		return nil, errors.New("wrong revision or actor")
	}
	f.calls = append(f.calls, "add:"+id)
	if f.depErr != nil {
		return nil, f.depErr
	}
	next := *root
	f.root = &next
	return f.root, nil
}
func formCreator(t *testing.T) (*formCreateFixture, *GitHubDataSource) {
	t.Helper()
	f := &formCreateFixture{}
	s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", branch: "actual-branch", open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	return f, s
}
func TestGitHubFormCreatePrechecksAndCarriesCreatedRevision(t *testing.T) {
	f, s := formCreator(t)
	issue := models.Issue{Title: "new", Type: models.TypeTask, Priority: models.PriorityP1, Minor: true, ParentID: "gh-8", CreatorSession: "untrusted", CreatedBranch: "untrusted"}
	id, err := s.CreateIssue(issue, []string{"2", "gh-3"})
	if err != nil || id != "gh-10" || strings.Join(f.calls, ",") != "get:gh-2,get:gh-3,create,add:gh-2,add:gh-3" {
		t.Fatalf("%s %v %v", id, f.calls, err)
	}
	if f.created.CreatorSession != "actual-monitor" || f.created.CreatedBranch != "actual-branch" || f.created.Status != models.StatusOpen || !f.created.Minor || f.created.ParentID != "gh-8" {
		t.Fatalf("%+v", f.created)
	}
}
func TestGitHubFormCreateFailureRetainsRecoveryAndNeverRetries(t *testing.T) {
	f, s := formCreator(t)
	f.depErr = &ghstore.ConflictError{ID: "gh-10", AfterWrite: true}
	id, err := s.CreateIssue(models.Issue{Title: "new"}, []string{"gh-2", "gh-3"})
	var conflict *ghstore.ConflictError
	if id != "gh-10" || !errors.As(err, &conflict) || !strings.Contains(err.Error(), "was created") || len(f.calls) != 4 {
		t.Fatalf("%s %v %v", id, f.calls, err)
	}
	f, s = formCreator(t)
	f.createErr = errors.New("uncertain operation td-op-real; inspect before retrying")
	id, err = s.CreateIssue(models.Issue{Title: "new"}, nil)
	if id != "" || !errors.Is(err, f.createErr) || len(f.calls) != 1 {
		t.Fatal("lost create recovery error or continued")
	}
	f, s = formCreator(t)
	f.deleted = true
	if _, err = s.CreateIssue(models.Issue{Title: "new"}, []string{"gh-2"}); err == nil || len(f.calls) != 1 {
		t.Fatal("created with deleted dependency")
	}
	f, s = formCreator(t)
	if _, err = s.CreateIssue(models.Issue{Title: "new"}, []string{"gh-2", "2"}); err == nil || len(f.calls) != 0 {
		t.Fatal("duplicate dependencies created task")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx = ctx
	if _, err = s.CreateIssue(models.Issue{Title: "new"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestGitHubFormCreateUIFailureKeepsDraftAndAttemptGuard(t *testing.T) {
	for _, failed := range []bool{false, true} {
		f, s := formCreator(t)
		if failed {
			f.depErr = errors.New("permission denied")
		}
		m := Model{DataSource: s, Width: 100, Height: 40}
		opened, _ := m.openNewIssueForm()
		m = opened.(Model)
		m.FormState.Title = "draft"
		m.FormState.Dependencies = "gh-2"
		pending, cmd := m.submitForm()
		m = pending.(Model)
		if cmd == nil || !m.WorkflowPending || !m.PendingRemoteWrite() || !m.FormOpen || !m.FormCreateAttempted {
			t.Fatal("create not queued safely")
		}
		if _, duplicate := m.submitForm(); duplicate != nil {
			t.Fatal("duplicate create queued")
		}
		msg := cmd().(MonitorFormSavedMsg)
		updated, _ := m.Update(msg)
		m = updated.(Model)
		if m.WorkflowPending {
			t.Fatal("create still pending")
		}
		if failed {
			if !m.FormOpen || m.FormState.Title != "draft" || m.FormSaveError == nil || !strings.Contains(m.StatusMessage, "gh-10") {
				t.Fatal("partial create lost draft or task ID")
			}
			if _, retry := m.submitForm(); retry != nil {
				t.Fatal("partial create retried")
			}
		} else if m.FormOpen || !strings.Contains(m.StatusMessage, "create saved for gh-10") {
			t.Fatal("create was not confirmed")
		}
	}
}
