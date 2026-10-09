package cmd

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func init() {
	for _, command := range []*cobra.Command{wsStartCmd, wsTagCmd, wsUntagCmd, wsCurrentCmd, wsListCmd, wsEndCmd} {
		local, original := command.RunE, command
		command.RunE = func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(getBaseDir())
			if err != nil {
				return err
			}
			kind, err := config.Store(cfg)
			if err != nil {
				return err
			}
			if kind == config.StoreSQLite {
				return local(cmd, args)
			}
			return runGitHubWorkSession(original, cmd, args, cfg)
		}
	}
}

func runGitHubWorkSession(original, cmd *cobra.Command, args []string, cfg *models.Config) error {
	cmd.SetOut(cmd.OutOrStdout())
	allowed := []string{"json", "work-dir", "help"}
	if original == wsTagCmd {
		allowed = append(allowed, "no-start")
	}
	var flagErr error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !slices.Contains(allowed, f.Name) {
			flagErr = fmt.Errorf("gh-issue does not support --%s for ws %s", f.Name, original.Name())
		}
	})
	if flagErr != nil {
		return flagErr
	}
	if original == wsStartCmd && len(args) != 1 {
		return fmt.Errorf("requires one work session name")
	}
	if (original == wsTagCmd || original == wsUntagCmd) && len(args) == 0 {
		return fmt.Errorf("requires at least one issue ID")
	}
	if (original == wsCurrentCmd || original == wsListCmd || original == wsEndCmd) && len(args) != 0 {
		return fmt.Errorf("ws %s takes no arguments", original.Name())
	}
	client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
	if err != nil {
		return err
	}
	dir, err := gitHubContextDirectory()
	if err != nil {
		return err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
	if err != nil {
		return err
	}
	state, err := scope.Update(cmd.Context(), nil)
	if err != nil {
		return err
	}
	if original == wsListCmd {
		entries := make([]wsListEntry, 0, len(state.WorkSessions))
		for _, ws := range state.WorkSessions {
			status := "completed"
			if ws.EndedAt == nil {
				status = "abandoned"
				if ws.ID == state.ActiveWorkSession {
					status = "active"
				}
			}
			entries = append(entries, wsListEntry{WorkSession: ws.WorkSession, Issues: jsonList(ws.Issues), Status: status})
		}
		slices.SortFunc(entries, func(a, b wsListEntry) int { return b.StartedAt.Compare(a.StartedAt) })
		if len(entries) > 20 {
			entries = entries[:20]
		}
		if jsonMode(cmd) {
			return githubWSJSON(cmd, entries)
		}
		for _, entry := range entries {
			cmd.Printf("%s  %q  %s  %s  [%s]\n", entry.ID, entry.Name, output.FormatTimeAgo(entry.StartedAt), strings.Join(entry.Issues, ","), entry.Status)
		}
		if len(entries) == 0 {
			cmd.Println("No work sessions")
		}
		return nil
	}
	if original == wsCurrentCmd {
		if state.ActiveWorkSession == "" {
			if jsonMode(cmd) {
				return githubWSJSON(cmd, map[string]any{"work_session": nil, "issues": []string{}, "storage": "device-local"})
			}
			cmd.Println("No active work session")
			return nil
		}
		ws, err := state.CurrentWorkSession()
		if err != nil {
			return err
		}
		issues := make([]ghstore.Record, 0, len(ws.Issues))
		for _, id := range ws.Issues {
			issue, err := client.Get(cmd.Context(), id)
			if err != nil {
				return fmt.Errorf("read tagged issue %s: %w", id, err)
			}
			issues = append(issues, *issue)
		}
		if jsonMode(cmd) {
			return githubWSJSON(cmd, map[string]any{"work_session": ws.WorkSession, "issues": jsonList(ws.Issues), "issue_details": issues, "storage": "device-local"})
		}
		cmd.Printf("WORK SESSION: %s %q\nStarted: %s\n", ws.ID, ws.Name, output.FormatTimeAgo(ws.StartedAt))
		for _, issue := range issues {
			cmd.Printf("  %s  %s  %s  %s\n", issue.ID, issue.Title, issue.Status, issue.Priority)
		}
		return nil
	}
	if original == wsStartCmd || original == wsEndCmd {
		snapshot, err := gitHubSnapshot(cmd.Context(), scope.Worktree)
		if err != nil {
			return err
		}
		var changed *ghcontext.WorkSession
		_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
			if !reflect.DeepEqual(current, state) {
				return fmt.Errorf("local context changed; reread before retrying")
			}
			if original == wsStartCmd {
				changed, err = current.StartWorkSession(args[0], snapshot.CommitSHA, scope.Worktree, "")
			} else {
				changed, err = current.EndWorkSession(snapshot.CommitSHA)
			}
			return err
		})
		if err != nil {
			return err
		}
		action := "started_work_session"
		if original == wsEndCmd {
			action = "ended_work_session"
		}
		if jsonMode(cmd) {
			return githubWSResult(cmd, action, map[string]any{"work_session": changed.WorkSession, "issues": jsonList(changed.Issues), "storage": "device-local"})
		}
		if original == wsStartCmd {
			cmd.Printf("WORK SESSION STARTED: %s\nName: %s\n", changed.ID, changed.Name)
		} else {
			cmd.Printf("WORK SESSION ENDED: %s (no handoff recorded)\n", changed.ID)
		}
		return nil
	}
	ws, err := state.CurrentWorkSession()
	if err != nil {
		return err
	}
	wsID := ws.ID
	// Validate all requested IDs before starting any remote writes.
	records := make([]*ghstore.Record, 0, len(args))
	for _, id := range args {
		record, err := client.Get(cmd.Context(), id)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(records, func(r *ghstore.Record) bool { return r.ID == record.ID }) {
			records = append(records, record)
		}
	}
	results := make([]string, 0, len(records))
	noStart, _ := cmd.Flags().GetBool("no-start")
	for _, record := range records {
		// Pin the active bundle while applying each operation. Remote changes use
		// observed revisions; GitHub cannot provide an atomic multi-issue write.
		_, err = scope.Update(cmd.Context(), func(current *ghcontext.State) error {
			if current.Session.ID != state.Session.ID || current.ActiveWorkSession != wsID {
				return fmt.Errorf("active work session changed; reread before retrying")
			}
			if original == wsUntagCmd {
				return current.UntagWorkSession(record.ID)
			}
			if !noStart && record.Status == models.StatusOpen {
				snapshot, err := gitHubSnapshot(cmd.Context(), scope.Worktree)
				if err != nil {
					return err
				}
				if _, _, err := client.TransitionObserved(cmd.Context(), record, "start", ghstore.TransitionOptions{SessionID: current.Session.ID, Reason: "Started via work session " + wsID, Snapshot: snapshot}); err != nil {
					return err
				}
			}
			return current.TagWorkSession(record.ID)
		})
		if err != nil {
			return fmt.Errorf("ws %s failed for %s (completed local IDs: %s; any remote success is retained, no automatic retry): %w", original.Name(), record.ID, strings.Join(results, ","), err)
		}
		results = append(results, record.ID)
	}
	action := "tagged_work_session"
	if original == wsUntagCmd {
		action = "untagged_work_session"
	}
	if jsonMode(cmd) {
		return githubWSResult(cmd, action, map[string]any{"work_session": wsID, "issues": results, "storage": "device-local"})
	}
	cmd.Printf("%s %s → %s\n", strings.ToUpper(original.Name()), strings.Join(results, ", "), wsID)
	return nil
}

func githubWSJSON(cmd *cobra.Command, value any) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
}
func githubWSResult(cmd *cobra.Command, action string, value map[string]any) error {
	value["action"] = action
	return githubWSJSON(cmd, value)
}
