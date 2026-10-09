package cmd

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/session"
	"github.com/spf13/cobra"
)

func TestRootMetadataHelperProcess(t *testing.T) {
	if os.Getenv("TD_ROOT_METADATA_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			args := os.Args[i+1:]
			os.Args = append([]string{"td"}, args...)
			SetVersion("fixture-offline")
			rootCmd.SetArgs(args)
			Execute()
			os.Exit(0)
		}
	}
	t.Fatal("missing helper arguments")
}

func TestRootMetadataDiagnosticsDoNotResolveExistingSession(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_ROOT_METADATA_HELPER", "1")
	t.Setenv("TD_ANALYTICS", "true")
	t.Setenv("TD_CONTEXT_ID", "root-metadata-fixture")
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "false")
	database, err := db.Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if _, err := session.GetOrCreate(database); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
		fail bool
	}{
		{nil, "Usage:", false},
		{[]string{"--help"}, "Usage:", false},
		{[]string{"--version"}, "fixture-offline", false},
		{[]string{"--invalid-root-flag", "--json"}, `"error":`, true},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		args := append([]string{"-test.run=^TestRootMetadataHelperProcess$", "--", "--work-dir", dir}, tc.args...)
		child := exec.CommandContext(ctx, executable, args...)
		child.Dir = dir
		output, err := child.CombinedOutput()
		cancel()
		if (err != nil) != tc.fail || !strings.Contains(string(output), tc.want) {
			t.Fatalf("%v: %v %s", tc.args, err, output)
		}
	}
	events, err := db.ReadCommandUsage(dir)
	if err != nil || len(events) != 4 {
		t.Fatal(len(events), err)
	}
	for _, event := range events {
		if event.SessionID != "" {
			t.Fatal("root metadata resolved issue session", event)
		}
	}
	records, err := db.ReadAgentErrors(dir)
	if err != nil || len(records) != 1 || records[0].SessionID != "" {
		t.Fatal(records, err)
	}
}

func TestMetadataCommandsSkipSyncAndDiagnosticIssueContext(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	t.Setenv("TD_SYNC_AUTO", "true")
	t.Setenv("TD_AUTH_KEY", "fixture-only")
	t.Setenv("TD_ANALYTICS", "true")
	t.Setenv("TD_CONTEXT_ID", "offline-sql-fixture")
	database, err := db.Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if err := database.SetSyncState("fixture-project"); err != nil {
		t.Fatal(err)
	}
	sess, err := session.GetOrCreate(database)
	if err != nil {
		t.Fatal(err)
	}
	oldHooks, oldExecuted := syncFeatureHooks, executedCmd
	t.Cleanup(func() { syncFeatureHooks = oldHooks; executedCmd = oldExecuted })
	startup, mutation := 0, 0
	syncFeatureHooks = SyncFeatureHooks{OnStartup: func(string) { startup++ }, OnAfterMutation: func() { mutation++ }, IsMutatingCommand: func(string) bool { return true }}
	makeCommand := func(topName, childName string) *cobra.Command {
		root := &cobra.Command{Use: "td"}
		top := &cobra.Command{Use: topName}
		root.AddCommand(top)
		if childName == "" {
			return top
		}
		child := &cobra.Command{Use: childName}
		top.AddCommand(child)
		return child
	}
	commands := []*cobra.Command{}
	for _, path := range [][2]string{{"config", "set"}, {"config", "associate"}, {"feature", "set"}, {"workflow", ""}, {"debug-stats", ""}, {"version", ""}, {"completion", "zsh"}, {"help", ""}, {"init", ""}} {
		commands = append(commands, makeCommand(path[0], path[1]))
	}
	for _, flag := range []string{"fields", "examples", "explain"} {
		cmd := makeCommand("query", "")
		cmd.Flags().Bool(flag, true, "")
		commands = append(commands, cmd)
	}
	helped := makeCommand("create", "")
	helped.Flags().Bool("help", true, "")
	commands = append(commands, helped)
	for _, cmd := range commands {
		runGatedSyncStartupHook(cmd)
		runGatedSyncMutationHook(cmd)
		if startup != 0 || mutation != 0 {
			t.Fatal("metadata command synced issue data", cmd.CommandPath())
		}
		if id := diagnosticCommandSessionID(cmd, dir); id != "" {
			t.Fatal("metadata command looked up issue session", cmd.CommandPath(), id)
		}
		executedCmd = cmd
		logAnalytics(nil)
		logAgentErrorDetails([]string{cmd.Name()}, "fixture error", nil)
	}
	events, err := db.ReadCommandUsage(dir)
	if err != nil || len(events) != len(commands) {
		t.Fatal(len(events), err)
	}
	for _, event := range events {
		if event.SessionID != "" {
			t.Fatal("metadata analytics retained issue context", event)
		}
	}
	errors, err := db.ReadAgentErrors(dir)
	if err != nil || len(errors) != len(commands) {
		t.Fatal(len(errors), err)
	}
	for _, record := range errors {
		if record.SessionID != "" {
			t.Fatal("metadata error retained issue context", record)
		}
	}
	// A configured data command still uses both hooks and the actual session.
	data := makeCommand("create", "")
	runGatedSyncStartupHook(data)
	runGatedSyncMutationHook(data)
	if startup != 1 || mutation != 1 {
		t.Fatal("data sync regressed", startup, mutation)
	}
	if id := diagnosticCommandSessionID(data, dir); id != sess.ID {
		t.Fatal("data command lost session", id, sess.ID)
	}
	query := makeCommand("query", "")
	if skipImplicitIssueAccess(query) {
		t.Fatal("normal query classified as offline")
	}
}
