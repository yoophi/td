package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/pkg/notes"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func noteTestCommand(original *cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: original.Use, Args: original.Args, RunE: original.RunE, SilenceErrors: true, SilenceUsage: true}
	original.Flags().VisitAll(func(f *pflag.Flag) {
		switch f.Value.Type() {
		case "string":
			c.Flags().StringP(f.Name, f.Shorthand, f.DefValue, "")
		case "bool":
			c.Flags().BoolP(f.Name, f.Shorthand, f.DefValue == "true", "")
		case "int":
			c.Flags().IntP(f.Name, f.Shorthand, 50, "")
		default:
			panic("unsupported note test flag: " + f.Name)
		}
	})
	if c.Flags().Lookup("json") == nil {
		c.Flags().Bool("json", false, "")
	}
	return c
}

func runNoteTest(t *testing.T, original *cobra.Command, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { _, err = executeGitHubTest(noteTestCommand(original), args...) })
	return out, err
}

func TestGitHubNoteAllCommandsFiltersAndIsolation(t *testing.T) {
	dir := githubScheduleTestDir(t)
	execute := func(c *cobra.Command, args ...string) string {
		t.Helper()
		out, err := runNoteTest(t, c, append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatalf("%s %v: %s %v", c.Name(), args, out, err)
		}
		return out
	}
	list := func(args ...string) []notes.Note {
		t.Helper()
		var rows []notes.Note
		if err := json.Unmarshal([]byte(execute(noteListCmd, args...)), &rows); err != nil {
			t.Fatal(err)
		}
		if rows == nil {
			t.Fatal("null list")
		}
		return rows
	}
	if len(list()) != 0 {
		t.Fatal("tasks leaked into notes")
	}
	execute(noteAddCmd, "한글 API 메모", "-c", "Unicode 📝\n본문")
	execute(noteAddCmd, "Other", "--content", "Another body")
	const id = "nt-gh-6"
	execute(notePinCmd, id)
	execute(notePinCmd, id)
	if rows := list("--pinned", "--limit", "1"); len(rows) != 1 || rows[0].ID != id {
		t.Fatal(rows)
	}
	if rows := list("-s", "a_i"); len(rows) != 1 || rows[0].ID != id {
		t.Fatal("LIKE underscore/case parity", rows)
	}
	execute(noteEditCmd, id, "-t", "수정 API", "--content", "")
	var n notes.Note
	if err := json.Unmarshal([]byte(execute(noteShowCmd, id)), &n); err != nil || n.Content != "" || n.Title != "수정 API" {
		t.Fatal(n, err)
	}
	execute(noteArchiveCmd, id)
	if len(list()) != 1 || len(list("-a")) != 2 || len(list("--archived")) != 1 {
		t.Fatal("archive filtering")
	}
	execute(noteUnarchiveCmd, id)
	execute(noteUnpinCmd, id)
	execute(noteDeleteCmd, id)
	execute(noteDeleteCmd, id)
	if len(list("--all")) != 1 {
		t.Fatal("deleted note visible")
	}
	if rows := list("--deleted", "-n", "1"); len(rows) != 1 || rows[0].ID != id || rows[0].DeletedAt == nil {
		t.Fatal(rows)
	}
	execute(noteShowCmd, id, "--include-deleted")
	if _, err := runNoteTest(t, noteShowCmd, id, "--json"); err == nil {
		t.Fatal("deleted note read")
	}
	execute(noteRestoreCmd, id)
	execute(noteRestoreCmd, id)
	if len(list("--deleted")) != 0 || len(list()) != 2 {
		t.Fatal("restore filtering")
	}
	for _, bad := range []string{"gh-1", "nt-gh-1", "nt-gh-06", "nt-abc"} {
		if _, err := runNoteTest(t, noteShowCmd, bad, "--json"); err == nil {
			t.Fatal("wrong entity accepted", bad)
		}
	}
	if _, err := executeGitHubTest(githubTestCommand(showCmd), "gh-6", "--json"); err == nil {
		t.Fatal("note accepted as task")
	}
	if _, err := executeGitHubTest(githubTestCommand(reviewCmd), "gh-6", "--json"); err == nil {
		t.Fatal("note accepted for task review")
	}
	t.Setenv("TD_SCHEDULE_FAIL_WRITE", "1")
	if _, err := runNoteTest(t, noteEditCmd, id, "--content", "Denied change", "--json"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatal("note write permission error hidden", err)
	}
	t.Setenv("TD_SCHEDULE_FAIL_WRITE", "")
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created", err)
	}
}

func TestGitHubNoteEditorConflictAndRemoteChange(t *testing.T) {
	dir := githubScheduleTestDir(t)
	if _, err := runNoteTest(t, noteAddCmd, "Editor fixture", "--content", "original", "--json"); err != nil {
		t.Fatal(err)
	}
	editor := filepath.Join(t.TempDir(), "editor")
	script := `#!/usr/bin/env python3
import os,json,pathlib,sys
p=pathlib.Path(os.environ['TD_SCHEDULE_STATE'])
s=json.loads(p.read_text());s['issues']['6']['title']='Peer edit';p.write_text(json.dumps(s))
pathlib.Path(sys.argv[-1]).write_text('Local draft')
`
	if err := os.WriteFile(editor, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	if _, err := runNoteTest(t, noteEditCmd, "nt-gh-6", "--json"); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatal("editor lost original revision", err)
	}
	s, err := notes.OpenWithContext(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	n, err := s.Get("nt-gh-6")
	if err != nil || n.Title != "Peer edit" || n.Content != "original" {
		t.Fatal(n, err)
	}
	runGit(t, dir, "remote", "set-url", "origin", "https://github.com/another/repo.git")
	if _, err := s.List(notes.ListOptions{}); err == nil {
		t.Fatal("changed remote accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}

func TestGitHubNoteCancellationCloseAndPreflight(t *testing.T) {
	dir := githubScheduleTestDir(t)
	initialized, err := notes.Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialized.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := notes.OpenWithContext(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := s.List(notes.ListOptions{}); err == nil {
		t.Fatal("cancellation ignored")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = notes.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("After close", ""); err == nil {
		t.Fatal("closed store wrote")
	}
	marker := filepath.Join(t.TempDir(), "editor-ran")
	editor := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	t.Setenv("PATH", t.TempDir())
	if _, err := runNoteTest(t, noteAddCmd, "No gh"); err == nil {
		t.Fatal("missing gh accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("editor opened before preflight")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}
