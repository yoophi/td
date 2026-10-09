package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestGitHubShowGitComparisonAvailableAndMissing(t *testing.T) {
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Synthetic Fixture")
	runGit(t, dir, "config", "user.email", "synthetic@example.test")
	file := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(file, []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "sample.go")
	runGit(t, dir, "commit", "-m", "synthetic baseline")
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	first, err := gitHubSnapshot(cmd.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("first\nsecond\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "sample.go")
	runGit(t, dir, "commit", "-m", "synthetic second commit")
	info := map[string]any{"start_commit": first.CommitSHA}
	if err := addGitHubShowCurrentGit(cmd, info); err != nil {
		t.Fatal(err)
	}
	if info["comparison_available"] != true || info["commits_since_start"] != 1 || info["files_changed"] != 1 || info["additions"] != 1 || info["deletions"] != 0 {
		t.Fatal(info)
	}
	info = map[string]any{"start_commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := addGitHubShowCurrentGit(cmd, info); err != nil {
		t.Fatal(err)
	}
	if info["comparison_available"] != false || info["comparison_warning"] == nil || info["commits_since_start"] != nil {
		t.Fatal("missing shared commit presented as zero", info)
	}
}
