package serve

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/pkg/monitor"
)

func TestGitHubMonitorDTOExposesObservationAndFullReconciliation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	full := now.Add(-time.Minute)
	dto := MonitorDataToDTO(&monitor.RefreshDataMsg{Timestamp: now, FullReconciledAt: full, ObservationMode: "incremental"})
	if dto.Timestamp != now.Format(time.RFC3339) || dto.FullReconciledAt != full.Format(time.RFC3339) || dto.ObservationMode != "incremental" {
		t.Fatal(dto)
	}
	data, err := json.Marshal(MonitorDataToDTO(&monitor.RefreshDataMsg{Timestamp: now}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "full_reconciled_at") || strings.Contains(string(data), "observation_mode") {
		t.Fatal("SQLite response acquired GitHub fields")
	}
}
