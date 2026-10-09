package cmd

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

type gitHubShowDetail struct {
	ghstore.Record
	Logs          []map[string]any        `json:"logs"`
	Handoff       map[string]any          `json:"handoff"`
	Comments      []models.Comment        `json:"comments"`
	ReviewHistory []gitHubDisplayedReview `json:"review_history"`
	Files         []models.IssueFile      `json:"files"`
	Dependencies  []string                `json:"dependencies"`
	Blocks        []string                `json:"blocks"`
	Git           map[string]any          `json:"git,omitempty"`
	modelLogs     []models.Log
	modelHandoff  *models.Handoff
}

type gitHubDisplayedReview struct {
	models.IssueReview
	RequestedBy string `json:"requested_by"`
	Superseded  bool   `json:"superseded"`
}

func resolveGitHubShowSelection(cmd *cobra.Command, cfg *models.Config, client *ghstore.Client) ([]string, error) {
	dir, err := gitHubContextDirectory()
	if err != nil {
		return nil, err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
	if err != nil {
		return nil, err
	}
	state, err := scope.Update(cmd.Context(), nil)
	if err != nil {
		return nil, err
	}
	if state.Focus != "" {
		return []string{state.Focus}, nil
	}
	rows, err := client.List(cmd.Context(), false)
	if err != nil {
		return nil, err
	}
	for _, status := range []models.Status{models.StatusInProgress, models.StatusInReview} {
		ids := []string{}
		for _, r := range rows {
			if r.Status == status {
				ids = append(ids, r.ID)
			}
		}
		if len(ids) == 1 {
			return ids, nil
		}
		if len(ids) > 1 {
			return nil, fmt.Errorf("no issue ID specified: multiple issues %s: %v", status, ids)
		}
	}
	return nil, fmt.Errorf("no issue ID specified and no issues in progress or review; use td show <issue-id>")
}

func loadGitHubShowDetails(cmd *cobra.Command, client *ghstore.Client, records []ghstore.Record) (map[string]gitHubShowDetail, error) {
	rows, err := client.List(cmd.Context(), true)
	if err != nil {
		return nil, err
	}
	// Preserve direct reads when the repository listing lags a newly-created issue.
	byID := map[string]ghstore.Record{}
	for _, r := range rows {
		if _, ok := byID[r.ID]; ok {
			return nil, fmt.Errorf("GitHub listing repeated %s", r.ID)
		}
		byID[r.ID] = r
	}
	for _, r := range records {
		byID[r.ID] = r
	}
	rows = nil
	for _, r := range byID {
		rows = append(rows, r)
	}
	snapshot, err := issuestore.NewGitHubQuerySnapshot(cmd.Context(), rows, client)
	if err != nil {
		return nil, err
	}
	result := map[string]gitHubShowDetail{}
	for _, r := range records {
		d := gitHubShowDetail{Record: r, Logs: []map[string]any{}, Comments: []models.Comment{}, ReviewHistory: []gitHubDisplayedReview{}, Files: []models.IssueFile{}, Dependencies: []string{}, Blocks: []string{}}
		d.modelLogs, err = snapshot.GetLogs(r.ID, 0)
		if err != nil {
			return nil, fmt.Errorf("show %s logs: %w", r.ID, err)
		}
		d.modelHandoff, err = snapshot.GetLatestHandoff(r.ID)
		if err != nil {
			return nil, err
		}
		d.Comments, err = snapshot.GetComments(r.ID)
		if err != nil {
			return nil, err
		}
		for _, log := range d.modelLogs {
			d.Logs = append(d.Logs, map[string]any{"timestamp": log.Timestamp, "session": log.SessionID, "message": log.Message, "type": log.Type})
		}
		if h := d.modelHandoff; h != nil {
			d.Handoff = map[string]any{"timestamp": h.Timestamp, "session": h.SessionID, "done": jsonList(h.Done), "remaining": jsonList(h.Remaining), "decisions": jsonList(h.Decisions), "uncertain": jsonList(h.Uncertain)}
		}
		if r.Details != nil {
			d.Files = append(d.Files, r.Details.Files...)
			d.Dependencies = append(d.Dependencies, r.Details.Dependencies...)
			reviews := append([]models.IssueReview{}, r.Details.Reviews...)
			sort.SliceStable(reviews, func(i, j int) bool { return reviews[i].CreatedAt.Before(reviews[j].CreatedAt) })
			if len(reviews) > 3 {
				reviews = reviews[len(reviews)-3:]
			}
			for _, review := range reviews {
				d.ReviewHistory = append(d.ReviewHistory, gitHubDisplayedReview{IssueReview: review, RequestedBy: review.RequestedBySession, Superseded: review.SupersededAt != nil})
			}
			for _, event := range r.Details.Transitions {
				if event.Action == "start" && event.Snapshot != nil {
					s := event.Snapshot
					d.Git = map[string]any{"start_commit": s.CommitSHA, "start_branch": s.Branch, "started_at": s.Timestamp}
				}
			}
		}
		for _, row := range rows {
			if row.Details != nil {
				for _, dep := range row.Details.Dependencies {
					if dep == r.ID {
						d.Blocks = append(d.Blocks, row.ID)
					}
				}
			}
		}
		if d.Git != nil {
			if err := addGitHubShowCurrentGit(cmd, d.Git); err != nil {
				return nil, fmt.Errorf("show %s local git state: %w", r.ID, err)
			}
		}
		sort.Strings(d.Blocks)
		result[r.ID] = d
	}
	return result, nil
}

// A shared start commit may not exist in this clone. Preserve that fact instead
// of presenting unavailable commit/diff counts as zero.
func addGitHubShowCurrentGit(cmd *cobra.Command, info map[string]any) error {
	dir, err := gitHubContextDirectory()
	if err != nil {
		return err
	}
	current, err := gitHubSnapshot(cmd.Context(), dir)
	if err != nil {
		return err
	}
	info["current_commit"], info["current_branch"], info["dirty_files"] = current.CommitSHA, current.Branch, current.DirtyFiles
	start := fmt.Sprint(info["start_commit"])
	run := func(args ...string) ([]byte, error) {
		c := exec.CommandContext(cmd.Context(), "git", args...)
		c.Dir = dir
		return c.Output()
	}
	_, err = run("rev-parse", "--verify", "--quiet", "--end-of-options", start+"^{commit}")
	if err != nil {
		if cmd.Context().Err() != nil {
			return cmd.Context().Err()
		}
		info["comparison_available"] = false
		info["comparison_warning"] = "shared start commit is unavailable in this local clone"
		return nil
	}
	count, err := run("rev-list", "--count", start+"..HEAD")
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(count)))
	if err != nil {
		return err
	}
	stats, err := run("diff", "--numstat", start+"..HEAD", "--")
	if err != nil {
		return err
	}
	files, additions, deletions := 0, 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(stats)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return fmt.Errorf("invalid git diff statistics")
		}
		files++
		if parts[0] != "-" {
			value, err := strconv.Atoi(parts[0])
			if err != nil {
				return err
			}
			additions += value
		}
		if parts[1] != "-" {
			value, err := strconv.Atoi(parts[1])
			if err != nil {
				return err
			}
			deletions += value
		}
	}
	info["comparison_available"], info["commits_since_start"] = true, n
	info["files_changed"], info["additions"], info["deletions"] = files, additions, deletions
	return nil
}

func printGitHubShowDetails(cmd *cobra.Command, d gitHubShowDetail) {
	if len(d.ReviewHistory) > 0 {
		cmd.Print(output.SectionHeader("Recent Reviews"))
		for _, r := range d.ReviewHistory {
			marker := ""
			if r.ReviewedBy != "" {
				marker = " (reviewed by " + output.SanitizeIssueText(r.ReviewedBy) + ")"
			} else if r.SelfReview {
				marker = " (self-review)"
			}
			if r.SupersededAt != nil {
				marker += " [superseded]"
			}
			cmd.Printf("  [%s] %s by %s: %s%s\n", r.CreatedAt.Format("2006-01-02 15:04"), r.Decision, output.SanitizeIssueText(r.ReviewerSession), output.IndentContinuation(r.Summary), marker)
		}
	}
	if d.ReviewerSession != "" || d.ClosedBySession != "" {
		cmd.Print(output.SectionHeader("Review / Close"))
		if d.ReviewerSession != "" {
			cmd.Println("  Reviewer of record:", output.SanitizeIssueText(d.ReviewerSession))
		}
		if d.ReviewRequestedBySession != "" {
			cmd.Println("  Review requested by:", output.SanitizeIssueText(d.ReviewRequestedBySession))
		}
		if d.ClosedBySession != "" {
			cmd.Println("  Closed by:", output.SanitizeIssueText(d.ClosedBySession))
		}
	}
	if d.Git != nil {
		cmd.Print(output.SectionHeader("Git State"))
		cmd.Printf("  Started: %s (%s)\n", output.SanitizeIssueText(fmt.Sprint(d.Git["start_commit"])), output.SanitizeIssueText(fmt.Sprint(d.Git["start_branch"])))
		cmd.Printf("  Current: %s (%s), dirty files: %v\n", output.SanitizeIssueText(fmt.Sprint(d.Git["current_commit"])), output.SanitizeIssueText(fmt.Sprint(d.Git["current_branch"])), d.Git["dirty_files"])
		if warning, ok := d.Git["comparison_warning"]; ok {
			cmd.Println("  " + fmt.Sprint(warning))
		} else {
			cmd.Printf("  Since start: %v commits, %v files (+%v -%v)\n", d.Git["commits_since_start"], d.Git["files_changed"], d.Git["additions"], d.Git["deletions"])
		}
	}
	if len(d.Files) > 0 {
		cmd.Print(output.SectionHeader("Linked Files"))
		for _, f := range d.Files {
			cmd.Printf("  %s (%s)\n", output.SanitizeIssueText(f.FilePath), f.Role)
		}
	}
	for _, section := range []struct {
		name string
		ids  []string
	}{{"Blocked By", d.Dependencies}, {"Blocks", d.Blocks}} {
		if len(section.ids) > 0 {
			cmd.Print(output.SectionHeader(section.name))
			for _, id := range section.ids {
				cmd.Println("  " + id)
			}
		}
	}
}
