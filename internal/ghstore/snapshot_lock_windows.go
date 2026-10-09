package ghstore

import (
	"context"
	"fmt"
	"os"
)

func lockSnapshotFile(context.Context, *os.File) error {
	return fmt.Errorf("snapshot disk caching is unavailable on Windows")
}
func unlockSnapshotFile(*os.File) {}
