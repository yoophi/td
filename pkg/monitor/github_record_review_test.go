package monitor

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func TestGitHubMonitorRecordPlanRejectsDuplicateAndStrictPolicy(t *testing.T) {
	f := &transitionFixture{detailFixture: detailFixture{root: &ghstore.Record{Issue: models.Issue{ID: "gh-1", Status: models.StatusInReview, ImplementerSession: "actual-monitor"}}, fresh: true}}
	s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	store := &githubIssueTransition{source: s, observed: *f.root}
	plan, err := store.RecordPlan()
	if err != nil || !plan.SelfReviewPrompt || f.writes != 0 {
		t.Fatalf("trusted %+v %v", plan, err)
	}
	f.approval = true
	if _, err = store.RecordPlan(); err == nil || f.writes != 0 {
		t.Fatal("duplicate record allowed")
	}
	f.approval = false
	t.Setenv("TD_FEATURE_REVIEW_POLICY_MODE", "strict")
	if _, err = store.RecordPlan(); err == nil {
		t.Fatal("strict record-only allowed")
	}
	t.Setenv("TD_FEATURE_REVIEW_POLICY_MODE", "delegated")
	plan, err = store.RecordPlan()
	if err == nil || f.writes != 0 {
		t.Fatalf("delegated implementer was allowed %+v %v", plan, err)
	}
	f.root.ImplementerSession = "other-monitor"
	store.observed = *f.root
	if plan, err = store.RecordPlan(); err != nil || plan.SelfReviewPrompt {
		t.Fatalf("independent delegated review %+v %v", plan, err)
	}
}
func TestGitHubMonitorRecordReviewForwardsRecordOnlyDecisionAndAttribution(t *testing.T) {
	f := &transitionFixture{detailFixture: detailFixture{root: &ghstore.Record{Issue: models.Issue{ID: "gh-1", Status: models.StatusInReview}}}}
	s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	store := &githubIssueTransition{source: s, observed: *f.root}
	for _, decision := range []string{reviewpolicy.DecisionApproved, reviewpolicy.DecisionChangesRequested} {
		if err := store.RecordReview(decision, " reviewer ", " summary ", false); err != nil || !f.options.RecordOnly || f.options.Decision != decision || f.options.ReviewedBy != "reviewer" || f.options.Reason != "summary" || f.options.SelfReview || f.action != "approve" || f.options.SessionID != "actual-monitor" {
			t.Fatalf("%+v %v", f.options, err)
		}
	}
	before := f.writes
	if err := store.RecordReview("invalid", "", "summary", false); err == nil || f.writes != before {
		t.Fatal("invalid decision wrote")
	}
	if err := store.RecordReview(reviewpolicy.DecisionApproved, "", "", false); err == nil || f.writes != before {
		t.Fatal("missing summary wrote")
	}
}

type modelRecordFixture struct {
	modelApproveFixture
	decision string
}

func (f *modelRecordFixture) RecordPlan() (MonitorApprovalPlan, error) { return f.plan, f.planErr }
func (f *modelRecordFixture) RecordReview(decision, by, reason string, self bool) error {
	f.calls++
	f.decision = decision
	f.reviewedBy = by
	f.reason = reason
	f.self = self
	return f.failure
}
func TestGitHubMonitorRecordReviewModalWaitsAndPreservesFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		f := &modelRecordFixture{modelApproveFixture: modelApproveFixture{modelCloseFixture: modelCloseFixture{issue: models.Issue{ID: "gh-1", Status: models.StatusInReview}}, plan: MonitorApprovalPlan{SelfReviewPrompt: true}}}
		if failed {
			f.failure = errors.New("write outcome unknown")
		}
		m := Model{DataSource: dashboardOnlyFixture{}, ModalStack: []ModalEntry{{IssueID: "gh-1", Issue: &f.issue, Transitions: map[string]MonitorTransitionStore{"gh-1": f}}}}
		result, prepare := m.recordReviewAction()
		m = result.(Model)
		if prepare == nil || !m.WorkflowPending || f.calls != 0 {
			t.Fatal("missing read-only preparation")
		}
		prepared := prepare()
		result, _ = m.Update(prepared)
		m = result.(Model)
		if !m.RecordReviewOpen || m.RecordStore != f {
			t.Fatal("missing record prompt")
		}
		if _, write := m.executeRecordReview(); write != nil {
			t.Fatal("empty summary wrote")
		}
		m.RecordReviewModal.Render(80, 24, m.RecordReviewMouseHandler)
		_, _ = m.RecordReviewModal.HandleMsg(tea.PasteMsg{Content: " verified feedback "})
		m.RecordReviewDecision = reviewpolicy.DecisionChangesRequested
		m.ModalStack[0].Transitions = map[string]MonitorTransitionStore{}
		result, write := m.executeRecordReview()
		m = result.(Model)
		if write == nil || f.calls != 0 || m.CurrentModal().Issue.Status != models.StatusInReview {
			t.Fatal("optimistic record")
		}
		if _, duplicate := m.executeRecordReview(); duplicate != nil {
			t.Fatal("duplicate record")
		}
		msg := write().(MonitorTransitionedMsg)
		result, refresh := m.Update(msg)
		m = result.(Model)
		if f.calls != 1 || f.decision != reviewpolicy.DecisionChangesRequested || f.reason != "verified feedback" || !f.self || f.reviewedBy != "" || m.RecordStore != nil || m.WorkflowPending {
			t.Fatal("record input changed")
		}
		if failed {
			if refresh != nil || !m.RecordReviewOpen || !m.StatusIsError {
				t.Fatal("failed prompt lost")
			}
			if _, retry := m.executeRecordReview(); retry != nil {
				t.Fatal("stale retry")
			}
		} else {
			if refresh == nil || m.RecordReviewOpen {
				t.Fatal("saved prompt retained")
			}
		}
		if _, duplicate := m.Update(msg); duplicate != nil {
			t.Fatal("duplicate reply refreshed")
		}
	}
}
