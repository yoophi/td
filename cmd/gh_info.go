package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func githubInfo(cmd *cobra.Command, baseDir string) (bool, error) {
	cfg, err := config.Load(baseDir)
	if err != nil {
		return true, err
	}
	kind, err := config.Store(cfg)
	if err != nil {
		return true, err
	}
	if kind != config.StoreGitHub {
		return false, nil
	}
	cmd.SilenceUsage = true
	client, err := ghstore.Open(cmd.Context(), baseDir, cfg.GitHub)
	if err != nil {
		return true, err
	}
	dir, err := gitHubContextDirectory()
	if err != nil {
		return true, err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
	if err != nil {
		return true, err
	}
	state, err := scope.Update(cmd.Context(), nil)
	if err != nil {
		return true, err
	}
	mode, err := resolveReviewPolicyMode(baseDir)
	if err != nil {
		return true, err
	}
	overview, err := issuestore.ReadGitHubOverview(cmd.Context(), client, state.Session.ID, mode)
	if err != nil {
		return true, err
	}
	project := filepath.Base(baseDir)
	if jsonMode(cmd) {
		return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"project": project, "base_dir": baseDir, "store": "gh-issue", "repository": cfg.GitHub.Repo, "remote": cfg.GitHub.Remote, "data_url": "https://github.com/" + cfg.GitHub.Repo + "/issues", "local_context": scope.Path, "current_session": state.Session.ID, "review_policy_mode": mode, "issues": overview.Issues, "by_type": overview.ByType, "by_priority": overview.ByPriority, "review_queue": overview.ReviewQueue})
	}
	w := cmd.OutOrStdout()
	if _, err = fmt.Fprintf(w, "Project: %s\nStore: gh-issue\nRepository: %s (remote: %s)\nData: https://github.com/%s/issues\nLocal Context: %s (device-local)\nCurrent Session: %s\nReview Policy: %s\n\nIssues: %d total\n", output.SanitizeIssueText(project), cfg.GitHub.Repo, cfg.GitHub.Remote, cfg.GitHub.Repo, output.SanitizeIssueText(scope.Path), state.Session.ID, mode, overview.Issues["total"]); err != nil {
		return true, err
	}
	for _, section := range []struct {
		name   string
		keys   []string
		values map[string]int
	}{{"By Status", []string{"open", "in_progress", "blocked", "in_review", "closed"}, overview.Issues}, {"Review Queue", []string{"awaiting_review", "you_can_review", "you_can_close_after", "stale_review"}, overview.ReviewQueue}, {"By Type", []string{"bug", "feature", "task", "epic", "chore"}, overview.ByType}, {"By Priority", []string{"P0", "P1", "P2", "P3", "P4"}, overview.ByPriority}} {
		if _, err = fmt.Fprintf(w, "\n%s:\n", section.name); err != nil {
			return true, err
		}
		for _, key := range section.keys {
			if _, err = fmt.Fprintf(w, "  %s: %d\n", key, section.values[key]); err != nil {
				return true, err
			}
		}
	}
	_, err = fmt.Fprintln(w, "\nReview counts are observations; trusted self-review requires acknowledgement. Analytics: td stats.")
	return true, err
}
