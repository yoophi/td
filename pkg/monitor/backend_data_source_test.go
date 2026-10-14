package monitor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcus/td/internal/backendstore"
)

type backendMonitorReaderFunc func(context.Context, string, bool, string) (backendstore.Monitor, error)

func (f backendMonitorReaderFunc) Monitor(ctx context.Context, search string, closed bool, sort string) (backendstore.Monitor, error) {
	return f(ctx, search, closed, sort)
}

func TestBackendMonitorDiscardsChangedContext(t *testing.T) {
	var checks atomic.Int32
	reader := backendMonitorReaderFunc(func(_ context.Context, search string, closed bool, sort string) (backendstore.Monitor, error) {
		if search != "needle" || !closed || sort != "-updated" {
			t.Fatalf("unexpected monitor options %q %v %q", search, closed, sort)
		}
		return backendstore.Monitor{Dashboard: backendstore.MonitorSnapshot{Timestamp: time.Now()}}, nil
	})
	source := NewBackendDataSource(context.Background(), reader, func(context.Context) error {
		if checks.Add(1) == 2 {
			return errors.New("session changed")
		}
		return nil
	})
	result := source.Fetch("needle", true, SortByUpdatedDesc)
	if result.Error == nil || result.FocusedIssue != nil || !result.Timestamp.IsZero() || checks.Load() != 2 {
		t.Fatalf("stale snapshot was accepted: %+v", result)
	}
}

func TestBackendMonitorSkipsOverlappingRefresh(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	reader := backendMonitorReaderFunc(func(context.Context, string, bool, string) (backendstore.Monitor, error) {
		close(entered)
		<-release
		return backendstore.Monitor{Dashboard: backendstore.MonitorSnapshot{Timestamp: time.Now()}}, nil
	})
	source := NewBackendDataSource(context.Background(), reader, func(context.Context) error { return nil })
	done := make(chan RefreshDataMsg, 1)
	go func() { done <- source.Fetch("", false, SortByPriority) }()
	<-entered
	if !source.IsRefreshing() {
		t.Fatal("in-flight backend request not reported")
	}
	if second := source.Fetch("", false, SortByPriority); !second.Skipped {
		t.Fatalf("overlapping refresh was queued: %+v", second)
	}
	close(release)
	<-done
	if source.IsRefreshing() {
		t.Fatal("completed backend request still reported in flight")
	}
}
