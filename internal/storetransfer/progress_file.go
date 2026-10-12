package storetransfer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// saveProgress syncs a private sibling file before publishing it atomically.
// A short exclusive file lock protects the expected previous digest; no network
// request occurs while it is held. A crashed writer's lock requires inspection,
// never automatic lock removal or uncertain-write replay.
func saveProgress(path string, next *Progress, previousSHA string, checkpoint *Checkpoint, maxBytes int64) error {
	if err := next.validate(checkpoint); err != nil {
		return err
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if _, err := ReadProgress(bytes.NewReader(data), checkpoint, maxBytes); err != nil {
		return err
	}
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("lock transfer progress; inspect existing writer before retry: %w", err)
	}
	defer func() { _ = lock.Close(); _ = os.Remove(lockPath) }()
	if previousSHA != "" {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect original progress: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("progress must be an existing regular file: %w", os.ErrInvalid)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		previous, readErr := ReadProgress(file, checkpoint, maxBytes)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if previous.SHA256 != previousSHA {
			return fmt.Errorf("progress changed since it was read; no replacement attempted")
		}
		if err := validateProgressTransition(previous, next); err != nil {
			return err
		}
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".td-transfer-progress-*")
	if err != nil {
		return fmt.Errorf("create private progress stage: %w", err)
	}
	name := file.Name()
	defer func() { _ = file.Close(); _ = os.Remove(name) }()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write progress stage: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync progress stage: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if previousSHA == "" {
		if err := os.Link(name, path); err != nil {
			return fmt.Errorf("publish new progress without overwrite: %w", err)
		}
	} else if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace progress atomically: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("progress may be published; inspect before retry: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("progress may be published; directory sync failed; inspect before retry: %w", err)
	}
	return nil
}
