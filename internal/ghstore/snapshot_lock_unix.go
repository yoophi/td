//go:build !windows

package ghstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func lockSnapshotFile(ctx context.Context, file *os.File) error {
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("snapshot cache lock timeout")
		case <-tick.C:
		}
	}
}
func unlockSnapshotFile(file *os.File) { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }
