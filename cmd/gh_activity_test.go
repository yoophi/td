package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/session"
	"github.com/spf13/cobra"
)

type memoryActivities struct {
	items  []models.Activity
	writes int
}

func (m *memoryActivities) ListActivity(context.Context, string) ([]models.Activity, error) {
	return m.items, nil
}
func (m *memoryActivities) AppendActivity(_ context.Context, id string, a models.Activity) (*models.Activity, error) {
	m.writes++
	a.IssueID = id
	a.ID = fmt.Sprintf("ghc-%d", m.writes)
	m.items = append(m.items, a)
	return &a, nil
}
func activityTestCommand(original *cobra.Command, store *memoryActivities, dir string) *cobra.Command {
	cmd := &cobra.Command{Use: original.Use, SilenceErrors: true, SilenceUsage: true}
	cmd.Flags().Bool("json", false, "")
	for _, name := range []string{"issue", "task", "type", "note", "message"} {
		cmd.Flags().String(name, "", "")
	}
	for _, name := range []string{"blocker", "hypothesis", "tried", "result"} {
		cmd.Flags().Bool(name, false, "")
	}
	if original == handoffCmd {
		for _, name := range []string{"done", "remaining", "decision", "uncertain"} {
			cmd.Flags().StringArray(name, nil, "")
		}
	} else {
		cmd.Flags().Bool("decision", false, "")
	}
	cmd.SetIn(strings.NewReader(""))
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runGitHubActivity(original, cmd, args, store, &ghcontext.State{Session: session.Session{ID: "ses1"}, Focus: "gh-8"}, dir)
	}
	return cmd
}

func TestGitHubActivityLogTargetsAndTypes(t *testing.T) {
	for _, tc := range []struct {
		args               []string
		stdin, id, message string
		typ                models.LogType
	}{
		{[]string{"progress"}, "", "gh-8", "progress", models.LogTypeProgress},
		{[]string{"gh-2", "message", "--decision"}, "", "gh-2", "message", models.LogTypeDecision},
		{[]string{"message", "#3", "--type", "orchestration"}, "", "#3", "message", models.LogTypeOrchestration},
		{[]string{"--task", "9"}, "piped\n", "9", "piped", models.LogTypeProgress},
		{[]string{"gh-10"}, "piped\n", "gh-10", "piped", models.LogTypeProgress},
		{[]string{"gh-001", "message", "--issue", "1"}, "", "1", "message", models.LogTypeProgress},
	} {
		store := &memoryActivities{}
		cmd := activityTestCommand(logCmd, store, "")
		cmd.SetIn(strings.NewReader(tc.stdin))
		out, err := executeGitHubTest(cmd, append(tc.args, "--json")...)
		if err != nil || store.writes != 1 {
			t.Fatalf("%v: %s %v", tc.args, out, err)
		}
		a := store.items[0]
		if a.IssueID != tc.id || a.Message != tc.message || a.LogType != tc.typ || a.SessionID != "ses1" {
			t.Fatalf("%+v", a)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
	}
	store := &memoryActivities{}
	_, err := executeGitHubTest(activityTestCommand(logCmd, store, ""), "gh-2", "text", "--issue", "3")
	if err == nil || store.writes != 0 {
		t.Fatalf("conflicting targets: %v", err)
	}
}

func TestGitHubCommentsSeparateNativeAndStructuredHistory(t *testing.T) {
	store := &memoryActivities{items: []models.Activity{
		{ID: "ghc-1", Kind: "comment", Native: true, Message: "native"},
		{ID: "ghc-2", Kind: "log", Message: "internal progress"},
		{ID: "ghc-3", Kind: "handoff", Done: []string{"done"}},
		{ID: "ghc-4", Kind: "comment", SessionID: "ses2", Message: "td comment"},
	}}
	out, err := executeGitHubTest(activityTestCommand(commentsCmd, store, ""), "gh-1", "--json")
	var got []models.Activity
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || len(got) != 2 || !got[0].Native || got[1].SessionID != "ses2" {
		t.Fatalf("%s %v", out, err)
	}
	for _, original := range []*cobra.Command{commentCmd, commentsAddCmd} {
		_, err := executeGitHubTest(activityTestCommand(original, store, ""), "gh-1", "new comment")
		if err != nil {
			t.Fatal(err)
		}
	}
	if store.writes != 2 {
		t.Fatalf("writes %d", store.writes)
	}
}

func TestGitHubHandoffInputAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Initial")
	file := filepath.Join(t.TempDir(), "done.txt")
	if err := os.WriteFile(file, []byte("one\ntwo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store := &memoryActivities{}
	cmd := activityTestCommand(handoffCmd, store, dir)
	cmd.SetIn(strings.NewReader("next\n"))
	out, err := executeGitHubTest(cmd, "gh-2", "--done", "@"+file, "--remaining", "-", "--decision", "chosen", "--json")
	if err != nil || store.writes != 1 {
		t.Fatalf("%s %v", out, err)
	}
	a := store.items[0]
	if len(a.Done) != 2 || len(a.Remaining) != 1 || len(a.Decisions) != 1 || a.Snapshot == nil || len(a.Snapshot.CommitSHA) != 40 {
		t.Fatalf("%+v", a)
	}
	store = &memoryActivities{}
	cmd = activityTestCommand(handoffCmd, store, dir)
	cmd.SetIn(strings.NewReader("done:\n- completed\nuncertain:\n  - question\n"))
	_, err = executeGitHubTest(cmd, "gh-2")
	if err != nil || len(store.items[0].Done) != 1 || len(store.items[0].Uncertain) != 1 {
		t.Fatalf("%+v %v", store.items, err)
	}
	for _, tc := range []struct {
		args  []string
		stdin string
	}{
		{[]string{"gh-2", "--done", "@" + file + ".missing"}, ""},
		{[]string{"gh-2", "--done", "-", "--remaining", "-"}, "item\n"},
		{[]string{"gh-2"}, "unknown:\n- lost content"},
		{[]string{"gh-2", "--done", "-"}, ""},
	} {
		store = &memoryActivities{}
		cmd = activityTestCommand(handoffCmd, store, dir)
		cmd.SetIn(strings.NewReader(tc.stdin))
		_, err = executeGitHubTest(cmd, tc.args...)
		if err == nil || store.writes != 0 {
			t.Fatalf("%v: err=%v writes=%d", tc.args, err, store.writes)
		}
	}
}

func TestGitHubSnapshotUnbornAndRenames(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	if err := os.WriteFile(filepath.Join(dir, "new-file"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := gitHubSnapshot(context.Background(), dir)
	if err != nil || snapshot.CommitSHA != "" || snapshot.Branch == "" || snapshot.DirtyFiles != 1 {
		t.Fatalf("%+v %v", snapshot, err)
	}
	if got := gitHubDirtyFiles([]byte("R  new name\x00old name\x00?? new\nfile\x00")); got != 2 {
		t.Fatalf("dirty files %d", got)
	}
	if _, err := gitHubSnapshot(context.Background(), t.TempDir()); err == nil {
		t.Fatal("accepted non-repository")
	}
}
