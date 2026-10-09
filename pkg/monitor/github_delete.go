package monitor

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type MonitorDeleteStore interface{ Delete() error }
type MonitorDeleteSource interface {
	PrepareDelete(string) (*models.Issue, MonitorDeleteStore, error)
}
type githubDeleteReader interface {
	Get(context.Context, string) (*ghstore.Record, error)
}
type githubDeleteWriter interface {
	SetDeletedObserved(context.Context, *ghstore.Record, bool, string, string) (*ghstore.Record, bool, error)
}
type githubIssueDelete struct {
	source   *GitHubDataSource
	observed ghstore.Record
}

func (s *GitHubDataSource) PrepareDelete(id string) (*models.Issue, MonitorDeleteStore, error) {
	n, err := ghstore.Number(id)
	if err != nil || id != fmt.Sprintf("gh-%d", n) {
		return nil, nil, fmt.Errorf("monitor deletion requires canonical gh-N issue ID")
	}
	if err := s.ctx.Err(); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(s.actor) == "" {
		return nil, nil, fmt.Errorf("monitor deletion requires actual session")
	}
	raw, err := s.open(s.ctx)
	if err != nil {
		return nil, nil, err
	}
	c, ok := raw.(githubDeleteReader)
	if !ok {
		return nil, nil, fmt.Errorf("GitHub monitor deletion reader unavailable")
	}
	record, err := c.Get(s.ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if record == nil || record.ID != id || record.DeletedAt != nil {
		return nil, nil, fmt.Errorf("deletion target unavailable or deleted")
	}
	issue := record.Issue
	return &issue, &githubIssueDelete{source: s, observed: *record}, nil
}
func (d *githubIssueDelete) Delete() error {
	if err := d.source.ctx.Err(); err != nil {
		return err
	}
	raw, err := d.source.open(d.source.ctx)
	if err != nil {
		return err
	}
	c, ok := raw.(githubDeleteWriter)
	if !ok {
		return fmt.Errorf("GitHub monitor deletion writer unavailable")
	}
	_, _, err = c.SetDeletedObserved(d.source.ctx, &d.observed, true, d.source.actor, "")
	return err
}

type MonitorDeletePreparedMsg struct {
	Issue   *models.Issue
	Store   MonitorDeleteStore
	Request uint64
	Error   error
}
type MonitorDeletedMsg struct {
	IssueID string
	Request uint64
	Error   error
}

func (m Model) confirmRemoteDelete() (tea.Model, tea.Cmd) {
	if m.DeletePreparing || m.DeletePending || m.WorkflowPending {
		m.StatusMessage = "A deletion request is still running"
		return m, nil
	}
	id := m.SelectedIssueID(m.ActivePanel)
	if modal := m.CurrentModal(); modal != nil && modal.Issue != nil {
		id = modal.IssueID
	}
	if id == "" {
		return m, nil
	}
	source, ok := m.DataSource.(MonitorDeleteSource)
	if !ok {
		m.StatusMessage = "Monitor deletion source unavailable"
		m.StatusIsError = true
		return m, nil
	}
	m.DeleteRequest++
	request := m.DeleteRequest
	m.DeletePreparing = true
	return m, func() tea.Msg {
		issue, store, err := source.PrepareDelete(id)
		return MonitorDeletePreparedMsg{Issue: issue, Store: store, Request: request, Error: err}
	}
}
func (m Model) executeRemoteDelete() (tea.Model, tea.Cmd) {
	if m.DeletePending || m.WorkflowPending {
		return m, nil
	}
	if m.DeleteStore == nil || m.ConfirmIssueID == "" {
		m.StatusMessage = "Deletion observation unavailable; reopen confirmation"
		m.StatusIsError = true
		return m, nil
	}
	store := m.DeleteStore
	id := m.ConfirmIssueID
	request := m.DeleteRequest
	m.DeletePending = true
	return m, func() tea.Msg { return MonitorDeletedMsg{IssueID: id, Request: request, Error: store.Delete()} }
}
func (m Model) handleRemoteDeletePrepared(msg MonitorDeletePreparedMsg) (tea.Model, tea.Cmd) {
	if !m.DeletePreparing || msg.Request != m.DeleteRequest {
		return m, nil
	}
	m.DeletePreparing = false
	if msg.Error != nil {
		m.StatusMessage = "Cannot prepare deletion: " + msg.Error.Error()
		m.StatusIsError = true
		return m, nil
	}
	if msg.Issue == nil || msg.Store == nil {
		m.StatusMessage = "Deletion observation unavailable"
		m.StatusIsError = true
		return m, nil
	}
	m.DeleteStore = msg.Store
	m = m.openDeleteConfirmModal(msg.Issue.ID, msg.Issue.Title)
	return m, nil
}
func (m Model) handleRemoteDeleted(msg MonitorDeletedMsg) (tea.Model, tea.Cmd) {
	if !m.DeletePending || msg.Request != m.DeleteRequest {
		return m, nil
	}
	m.DeletePending = false
	m.DeleteStore = nil
	if msg.Error != nil {
		m.StatusMessage = "Failed to delete: " + msg.Error.Error() + "; inspect GitHub and reopen confirmation before retrying"
		m.StatusIsError = true
		return m, nil
	}
	m.closeDeleteConfirmModal()
	if modal := m.CurrentModal(); modal != nil && modal.IssueID == msg.IssueID {
		m.closeModal()
	}
	m.StatusMessage = "Deleted " + msg.IssueID
	m.StatusIsError = false
	if m.TaskListMode == TaskListModeBoard && m.BoardMode.Board != nil {
		return m, tea.Batch(m.fetchData(), m.fetchBoardIssues(m.BoardMode.Board.ID))
	}
	return m, m.fetchData()
}
