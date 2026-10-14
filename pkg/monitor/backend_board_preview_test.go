package monitor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

type backendPreviewFixture struct {
	rows    []backendstore.Issue
	ids     []string
	started chan struct{}
}

func (f backendPreviewFixture) Boards(context.Context) ([]backendstore.Board, error) {
	return nil, nil
}
func (f backendPreviewFixture) BoardDetail(context.Context, string, bool) (backendstore.BoardDetail, error) {
	return backendstore.BoardDetail{}, nil
}
func (f backendPreviewFixture) ReadConsistent(_ context.Context, read func() error) error {
	return read()
}
func (f backendPreviewFixture) QueryIDs(ctx context.Context, expression string) ([]string, error) {
	if expression == "status = blocked" && f.started != nil {
		close(f.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.ids, nil
}
func (f backendPreviewFixture) List(context.Context, bool) ([]backendstore.Issue, error) {
	return f.rows, nil
}

func TestBackendBoardPreviewUsesServerQueryOrderAndCancelsOldRead(t *testing.T) {
	fixture := backendPreviewFixture{started: make(chan struct{})}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("td-%016x", i+1)
		fixture.ids = append(fixture.ids, id)
		fixture.rows = append([]backendstore.Issue{{Issue: models.Issue{ID: id, Title: fmt.Sprintf("Issue %d", i+1)}}}, fixture.rows...)
	}
	source := NewBackendBoardSource(context.Background(), fixture, func(context.Context) error { return nil }, nil)
	old := make(chan BoardEditorQueryPreviewMsg, 1)
	go func() { old <- source.PreviewQuery("status = blocked") }()
	<-fixture.started
	current := source.PreviewQuery("status = open")
	if current.Error != nil || current.Query != "status = open" || current.Count != -1 || len(current.Titles) != 5 || current.Titles[0] != "Issue 1" || current.Titles[4] != "Issue 5" {
		t.Fatalf("backend query preview omitted ordered titles: %+v", current)
	}
	if stale := <-old; !errors.Is(stale.Error, context.Canceled) || len(stale.Titles) != 0 {
		t.Fatalf("superseded preview was published: %+v", stale)
	}
	if empty := source.PreviewQuery(""); empty.Error != nil || empty.Count != 0 || len(empty.Titles) != 0 {
		t.Fatalf("empty query preview failed: %+v", empty)
	}
}
