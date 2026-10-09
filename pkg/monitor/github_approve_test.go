package monitor

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestGitHubMonitorApprovalPlanFreshnessSelfReviewAndExistingApproval(t *testing.T) {
	f := &transitionFixture{detailFixture: detailFixture{root: &ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Review", Status: models.StatusInReview}}, fresh: true}}
	s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	store := &githubIssueTransition{source: s, observed: *f.root}
	plan, err := store.ApprovalPlan()
	if err != nil || plan.NeedInput || plan.CloseExisting || f.writes != 0 {
		t.Fatalf("independent %+v %v", plan, err)
	}
	f.root.ImplementerSession = "actual-monitor"
	store.observed = *f.root
	plan, err = store.ApprovalPlan()
	if err != nil || !plan.NeedInput || !plan.SelfReviewPrompt || f.writes != 0 {
		t.Fatalf("self %+v %v", plan, err)
	}
	f.approval = true
	plan, err = store.ApprovalPlan()
	if err != nil || !plan.CloseExisting || plan.SelfReviewPrompt {
		t.Fatalf("existing %+v %v", plan, err)
	}
	f.fresh = false
	if _, err = store.ApprovalPlan(); err == nil || f.writes != 0 {
		t.Fatal("stale review granted approval")
	}
}
func TestGitHubMonitorApprovalForwardsOnlyActualAttribution(t *testing.T) {
	f := &transitionFixture{detailFixture: detailFixture{root: &ghstore.Record{Issue: models.Issue{ID: "gh-1", Status: models.StatusInReview}}}}
	s := &GitHubDataSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (GitHubMonitorReader, error) { return f, nil }}
	store := &githubIssueTransition{source: s, observed: *f.root}
	if err := store.Approve("human reviewer", " verified ", false); err != nil || f.options.ReviewedBy != "human reviewer" || f.options.Reason != "verified" || f.options.SelfReview || f.options.SessionID != "actual-monitor" || f.action != "approve" {
		t.Fatalf("%+v %v", f.options, err)
	}
	if err := store.Approve("", "checked own work", true); err != nil || !f.options.SelfReview || f.options.ReviewedBy != "" {
		t.Fatalf("self %v", err)
	}
	before := f.writes
	if err := store.Approve("fake", "reason", true); err == nil || f.writes != before {
		t.Fatal("mutually exclusive attribution wrote")
	}
	if err := store.Approve("", "", true); err == nil || f.writes != before {
		t.Fatal("unreasoned self-review wrote")
	}
}

type modelApproveFixture struct {
	modelCloseFixture
	plan       MonitorApprovalPlan
	planErr    error
	reviewedBy string
	self       bool
}

func (f *modelApproveFixture) ApprovalPlan() (MonitorApprovalPlan, error) { return f.plan, f.planErr }
func (f *modelApproveFixture) Approve(name, reason string, self bool) error {
	f.calls++
	f.reviewedBy = name
	f.reason = reason
	f.self = self
	return f.failure
}
func approvalModel(f *modelApproveFixture) Model {
	return Model{DataSource: dashboardOnlyFixture{}, ModalStack: []ModalEntry{{IssueID: "gh-1", Issue: &f.issue, Transitions: map[string]MonitorTransitionStore{"gh-1": f}}}}
}
func TestGitHubMonitorApprovalDirectAndExistingApprovalRoutes(t *testing.T) {
	for _, existing := range []bool{false, true} {
		f := &modelApproveFixture{modelCloseFixture: modelCloseFixture{issue: models.Issue{ID: "gh-1", Status: models.StatusInReview}}, plan: MonitorApprovalPlan{CloseExisting: existing}}
		m := approvalModel(f)
		result, prepare := m.approveIssue()
		m = result.(Model)
		if prepare == nil || !m.WorkflowPending || f.calls != 0 {
			t.Fatal("missing eligibility read")
		}
		result, write := m.Update(prepare())
		m = result.(Model)
		if existing {
			if write != nil || !m.CloseConfirmOpen || m.CloseStore != f || f.calls != 0 {
				t.Fatal("existing approval was overwritten")
			}
			continue
		}
		if write == nil || !m.WorkflowPending || m.CurrentModal().Issue.Status != models.StatusInReview {
			t.Fatal("optimistic approval")
		}
		msg := write().(MonitorTransitionedMsg)
		result, refresh := m.Update(msg)
		m = result.(Model)
		if msg.Error != nil || refresh == nil || m.WorkflowPending || f.calls != 1 || f.self || f.reviewedBy != "" {
			t.Fatal("independent approval fabricated attribution")
		}
	}
}
func TestGitHubMonitorApprovalAttestationFailureKeepsDialogAndHandleFrozen(t *testing.T) {
	f := &modelApproveFixture{modelCloseFixture: modelCloseFixture{modelTransitionFixture: modelTransitionFixture{failure: errors.New("conflict")}, issue: models.Issue{ID: "gh-1", Status: models.StatusInReview}}, plan: MonitorApprovalPlan{NeedInput: true, SelfReviewPrompt: true}}
	m := approvalModel(f)
	result, prepare := m.approveIssue()
	m = result.(Model)
	result, _ = m.Update(prepare())
	m = result.(Model)
	if !m.SelfReviewConfirmOpen || m.ApproveStore != f || !m.ApprovalSelfReviewPrompt {
		t.Fatal("missing acknowledgment")
	}
	if _, cmd := m.executeSelfReviewApprove(); cmd != nil || f.calls != 0 {
		t.Fatal("empty acknowledgment wrote")
	}
	m.SelfReviewConfirmModal.Render(80, 24, m.SelfReviewConfirmMouseHandler)
	_, _ = m.SelfReviewConfirmModal.HandleMsg(tea.PasteMsg{Content: "real reviewer"})
	m.ModalStack[0].Transitions = map[string]MonitorTransitionStore{}
	result, write := m.executeSelfReviewApprove()
	m = result.(Model)
	if write == nil || f.calls != 0 || !m.SelfReviewConfirmOpen {
		t.Fatal("write not queued")
	}
	result, refresh := m.Update(write())
	m = result.(Model)
	if refresh != nil || !m.StatusIsError || !m.SelfReviewConfirmOpen || m.ApproveStore != nil || f.calls != 1 || f.reviewedBy != "real reviewer" || f.self {
		t.Fatal("attestation lost or failed dialog closed")
	}
	if _, retry := m.executeSelfReviewApprove(); retry != nil || f.calls != 1 {
		t.Fatal("stale approval retry")
	}
}

func TestGitHubMonitorApprovalReasonDoesNotInventSelfReview(t *testing.T) {
	for _, self := range []bool{false, true} {
		f := &modelApproveFixture{modelCloseFixture: modelCloseFixture{issue: models.Issue{ID: "gh-1", Status: models.StatusInReview}}, plan: MonitorApprovalPlan{NeedInput: true, SelfReviewPrompt: self}}
		m := approvalModel(f)
		result, prepare := m.approveIssue()
		m = result.(Model)
		result, _ = m.Update(prepare())
		m = result.(Model)
		m.SelfReviewConfirmModal.Render(80, 24, m.SelfReviewConfirmMouseHandler)
		_, _ = m.SelfReviewConfirmModal.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
		if m.SelfReviewConfirmModal.FocusedID() != "reason" {
			t.Fatal("reason input not focused")
		}
		m.SelfReviewConfirmModal.Render(80, 24, m.SelfReviewConfirmMouseHandler)
		_, _ = m.SelfReviewConfirmModal.HandleMsg(tea.PasteMsg{Content: " verified review "})
		result, write := m.executeSelfReviewApprove()
		m = result.(Model)
		if write == nil {
			t.Fatal("reason confirmation not submitted")
		}
		result, refresh := m.Update(write())
		m = result.(Model)
		if refresh == nil || m.SelfReviewConfirmOpen || m.ApproveStore != nil || f.self != self || f.reason != "verified review" || f.reviewedBy != "" {
			t.Fatal("reason invented reviewer or lost self-review acknowledgement")
		}
	}
}
