package monitor

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGitHubObservationStatusReachesFooterAndSurvivesFailedRefresh(t *testing.T) {
	m := NewModel(nil, "actor", time.Minute, "test", t.TempDir())
	m.DataSource = dashboardOnlyFixture{}
	m.Width, m.Height = 140, 40
	observed := time.Now().UTC()
	full := observed.Add(-2 * time.Minute)
	result, _ := m.Update(RefreshDataMsg{Timestamp: observed, FullReconciledAt: full, ObservationMode: "incremental"})
	m = result.(Model)
	if m.LastRefresh != observed || m.LastFullReconciliation != full || m.GitHubObservationMode != "incremental" {
		t.Fatal("observation metadata lost")
	}
	footer := m.renderFooter()
	if !strings.Contains(footer, "incremental; full") {
		t.Fatalf("freshness not visible: %s", footer)
	}
	failed, _ := m.Update(RefreshDataMsg{Error: errors.New("partial pagination failed")})
	next := failed.(Model)
	if next.LastRefresh != observed || next.LastFullReconciliation != full || !next.StatusIsError {
		t.Fatal("failed refresh appeared newer")
	}
}
