package serve

import (
	"encoding/json"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/pkg/monitor"
	"testing"
)

func TestMonitorDTOIncludesRecordedApprovalAndOtherPendingQueues(t *testing.T) {
	msg := &monitor.RefreshDataMsg{TaskList: monitor.TaskListData{ReadyToClose: []models.Issue{{ID: "gh-1"}}, PendingOther: []models.Issue{{ID: "gh-2"}}}}
	dto := MonitorDataToDTO(msg)
	if len(dto.TaskList.ReadyToClose) != 1 || dto.TaskList.ReadyToClose[0].ID != "gh-1" || len(dto.TaskList.PendingOther) != 1 || dto.TaskList.PendingOther[0].ID != "gh-2" {
		t.Fatalf("queues dropped: %+v", dto.TaskList)
	}
	raw, err := json.Marshal(MonitorDataToDTO(&monitor.RefreshDataMsg{}))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		TaskList map[string]any `json:"task_list"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ready_to_close", "pending_other"} {
		if values, ok := decoded.TaskList[name].([]any); !ok || len(values) != 0 {
			t.Fatalf("empty queue is not array: %s %s", name, raw)
		}
	}
}
