package ghstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunAPICancellationStopsExecutingCLI(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	t.Setenv("TD_TEST_STARTED", marker)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := []byte("#!/bin/sh\nprintf ready > \"$TD_TEST_STARTED\"\nexec /bin/sleep 30\n")
	if err := os.WriteFile(filepath.Join(dir, "gh"), script, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := runAPI(ctx, dir, nil, "api"); result <- err }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
waiting:
	for {
		select {
		case <-tick.C:
			if _, err := os.Stat(marker); err == nil {
				break waiting
			}
		case err := <-result:
			t.Fatalf("CLI ended before cancellation: %v", err)
		case <-deadline.C:
			t.Fatal("CLI never started")
		}
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation not preserved: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CLI did not terminate on cancellation")
	}
}
