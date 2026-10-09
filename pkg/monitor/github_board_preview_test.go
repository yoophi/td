package monitor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestGitHubBoardPreviewLimitsSessionAndReadFailures(t *testing.T) {
	f := &editableBoardFixture{}
	for i := 1; i <= 7; i++ {
		f.records = append(f.records, ghstore.Record{Issue: models.Issue{ID: fmt.Sprintf("gh-%d", i), Title: fmt.Sprintf("Task %d", i), Priority: models.PriorityP2, Status: models.StatusOpen, ImplementerSession: "actual-monitor"}})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opens := 0
	source := &GitHubBoardSource{ctx: ctx, actor: "actual-monitor", open: func(context.Context) (githubBoardClient, error) { opens++; return f, nil }}
	result := source.PreviewQuery("")
	if result.Error != nil || result.Count != 0 || opens != 0 {
		t.Fatalf("empty %+v", result)
	}
	result = source.PreviewQuery("future_field = x")
	if result.Error == nil || opens != 0 {
		t.Fatal("invalid preview opened store")
	}
	result = source.PreviewQuery("implementer = @me sort:id")
	if result.Error != nil || result.Count != -1 || len(result.Titles) != 5 || result.Titles[0] != "Task 1" {
		t.Fatalf("large %+v", result)
	}
	f.records = f.records[:5]
	result = source.PreviewQuery("status = open")
	if result.Error != nil || result.Count != 5 || len(result.Titles) != 5 {
		t.Fatalf("five %+v", result)
	}
	result = source.PreviewQuery("status = closed")
	if result.Error != nil || result.Count != 0 || result.Titles == nil {
		t.Fatalf("zero %+v", result)
	}
	f.fail = errors.New("HTTP 403 permission denied")
	if result = source.PreviewQuery("status = open"); result.Error == nil {
		t.Fatal("permission error hidden")
	}
	f.fail = nil
	cancel()
	result = source.PreviewQuery("status = open")
	if !errors.Is(result.Error, context.Canceled) {
		t.Fatalf("cancel %+v", result)
	}
	if f.writes != 0 {
		t.Fatal("preview wrote carrier/activity")
	}
}

func TestBoardPreviewModelIgnoresStaleQueryAndPreservesDraft(t *testing.T) {
	f := &editableBoardFixture{boardSourceFixture: boardSourceFixture{records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Title: "Match", Priority: models.PriorityP2, Status: models.StatusOpen}}}}}
	source := &GitHubBoardSource{ctx: context.Background(), actor: "actual-monitor", open: func(context.Context) (githubBoardClient, error) { return f, nil }}
	m := Model{BoardSource: source}
	m, _ = m.openBoardEditorCreate()
	m.BoardEditorNameInput.SetValue("Draft")
	m.BoardEditorQueryInput.SetValue("status = open")
	result := m.boardEditorQueryPreview("status = open")().(BoardEditorQueryPreviewMsg)
	updated, _ := m.Update(result)
	m = updated.(Model)
	if m.BoardEditorPreview.Count != 1 || m.BoardEditorNameInput.Value() != "Draft" {
		t.Fatal("preview changed draft")
	}
	stale := m.boardEditorQueryPreview("status = closed")().(BoardEditorQueryPreviewMsg)
	updated, _ = m.Update(stale)
	m = updated.(Model)
	if m.BoardEditorPreview.Count != 1 || m.BoardEditorPreview.Query != "status = open" {
		t.Fatal("old preview overwrote current query")
	}
	f.fail = errors.New("gh unavailable")
	result = m.boardEditorQueryPreview("status = open")().(BoardEditorQueryPreviewMsg)
	updated, _ = m.Update(result)
	m = updated.(Model)
	if m.BoardEditorPreview.Error == nil || m.BoardEditorNameInput.Value() != "Draft" {
		t.Fatal("preview failure hidden/discarded draft")
	}
}

type waitingPreviewFixture struct {
	editableBoardFixture
	started chan struct{}
	mu      sync.Mutex
	calls   int
}

func (f *waitingPreviewFixture) List(ctx context.Context, _ bool) ([]ghstore.Record, error) {
	f.mu.Lock()
	f.calls++
	first := f.calls == 1
	f.mu.Unlock()
	if first {
		close(f.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.records, nil
}
func TestGitHubBoardPreviewSupersedesOnlyPreviousPreview(t *testing.T) {
	f := &waitingPreviewFixture{started: make(chan struct{})}
	source := &GitHubBoardSource{ctx: context.Background(), actor: "actual-monitor", open: func(context.Context) (githubBoardClient, error) { return f, nil }}
	done := make(chan BoardEditorQueryPreviewMsg, 1)
	go func() { done <- source.PreviewQuery("status = open") }()
	select {
	case <-f.started:
	case <-time.After(time.Second):
		t.Fatal("preview never started")
	}
	current := source.PreviewQuery("status = closed")
	if current.Error != nil {
		t.Fatal(current.Error)
	}
	select {
	case previous := <-done:
		if !errors.Is(previous.Error, context.Canceled) {
			t.Fatal(previous.Error)
		}
	case <-time.After(time.Second):
		t.Fatal("old preview not cancelled")
	}
	// Closing the editor calls the same cancellation path without cancelling
	// the parent source, so later previews and writes can still use it.
	m := Model{BoardSource: source}
	m.closeBoardEditorModal()
	if result := source.PreviewQuery("status = open"); result.Error != nil {
		t.Fatal(result.Error)
	}
}
