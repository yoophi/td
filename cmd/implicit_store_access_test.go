package cmd

import (
	"testing"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/session"
	"github.com/spf13/cobra"
)

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
