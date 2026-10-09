package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	for _, original := range []*cobra.Command{blockedListCmd, inReviewCmd, readyCmd, nextCmd, reviewableCmd} {
		command, localRun := original, original.RunE
		command.RunE = func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(getBaseDir())
			if err != nil {
				return err
			}
			store, err := config.Store(cfg)
			if err != nil {
				return err
			}
			if store == config.StoreSQLite {
				return localRun(cmd, args)
			}
			cmd.SilenceUsage = true
			cmd.SetOut(cmd.OutOrStdout())
			if len(args) > 0 {
				return fmt.Errorf("%s does not accept positional filters; use td list or td query", command.Name())
			}
			return runGitHubListShortcut(cmd, command.Name(), cfg)
		}
	}
}

func runGitHubListShortcut(cmd *cobra.Command, name string, cfg *models.Config) error {
	client, state, rows, err := openGitHubQueryData(cmd, cfg)
	if err != nil {
		return err
	}
	snapshot, err := issuestore.NewGitHubQuerySnapshot(cmd.Context(), rows, client)
	if err != nil {
		return err
	}
	issues, err := snapshot.ListIssues(db.ListIssuesOptions{SortBy: "priority"})
	if err != nil {
		return err
	}
	if name == "reviewable" || (name == "in-review" && !jsonMode(cmd)) {
		// Review facts use observed records and revisions from the same listing.
		mode, err := resolveReviewPolicyMode(getBaseDir())
		if err != nil {
			return err
		}
		awaitingIDs, readyIDs, err := readGitHubReviewBuckets(cmd.Context(), client, rows, state.Session.ID, mode)
		if err != nil {
			return err
		}
		includeApproved, _ := cmd.Flags().GetBool("include-approved")
		awaiting, ready := []models.Issue{}, []models.Issue{}
		for _, issue := range issues {
			if awaitingIDs[issue.ID] {
				awaiting = append(awaiting, issue)
			}
			if includeApproved && readyIDs[issue.ID] {
				ready = append(ready, issue)
			}
			if name == "in-review" && issue.Status == models.StatusInReview {
				marker := ""
				if awaitingIDs[issue.ID] {
					marker = " [reviewable]"
				}
				cmd.Printf("%s  (impl: %s)%s\n", output.FormatIssueShort(&issue), issue.ImplementerSession, marker)
			}
		}
		if name == "in-review" {
			found := false
			for _, i := range issues {
				if i.Status == models.StatusInReview {
					found = true
				}
			}
			if !found {
				cmd.Println("No issues in review")
			}
			return nil
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"awaiting": awaiting, "ready_to_close": ready})
		}
		for _, bucket := range []struct {
			title  string
			issues []models.Issue
		}{{"AWAITING YOUR REVIEW", awaiting}, {"READY TO CLOSE", ready}} {
			if len(bucket.issues) == 0 {
				continue
			}
			cmd.Printf("%s (%d):\n", bucket.title, len(bucket.issues))
			for _, issue := range bucket.issues {
				cmd.Printf("  %s  (impl: %s)\n", output.FormatIssueShort(&issue), issue.ImplementerSession)
			}
		}
		if len(awaiting)+len(ready) == 0 {
			cmd.Println("No issues awaiting your review or ready to close")
		}
		return nil
	}
	openDeps := map[string]bool{}
	if name == "ready" || name == "next" {
		openDeps, err = snapshot.GetIssuesWithOpenDeps()
		if err != nil {
			return err
		}
	}
	selected := []models.Issue{}
	status := models.StatusOpen
	if name == "blocked" {
		status = models.StatusBlocked
	}
	if name == "in-review" {
		status = models.StatusInReview
	}
	for _, issue := range issues {
		if issue.Status == status && !openDeps[issue.ID] {
			selected = append(selected, issue)
		}
	}
	if name == "next" && len(selected) > 1 {
		selected = selected[:1]
	}
	if jsonMode(cmd) {
		if name == "next" {
			if len(selected) == 0 {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(nil)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(selected[0])
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(selected)
	}
	for _, issue := range selected {
		cmd.Println(output.FormatIssueShort(&issue))
	}
	if len(selected) == 0 {
		switch name {
		case "blocked":
			cmd.Println("No blocked issues")
		case "in-review":
			cmd.Println("No issues in review")
		default:
			cmd.Println("No open issues")
		}
	}
	if name == "next" && len(selected) == 1 {
		cmd.Printf("\nRun `td start %s` to begin working on this issue.\n", selected[0].ID)
	}
	return nil
}
