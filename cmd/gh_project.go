package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

// Route before reading td-sync credentials or opening its SQLite sync state.
func githubSyncProject(cmd *cobra.Command, args []string) (bool, error) {
	cfg, err := config.Load(getBaseDir())
	if err != nil {
		return true, err
	}
	kind, err := config.Store(cfg)
	if err != nil {
		return true, err
	}
	if kind != config.StoreGitHub {
		if cmd.Flags().Changed("remote") {
			return true, fmt.Errorf("--remote applies only to GitHub repository selection")
		}
		if !features.IsEnabledForProcess(features.SyncCLI.Name) {
			return true, fmt.Errorf("SQLite sync-project requires the sync_cli feature")
		}
		return false, nil
	}
	if cfg.GitHub == nil {
		return true, fmt.Errorf("GitHub repository configuration is missing")
	}
	switch cmd.Name() {
	case "link", "join":
		if len(args) != 1 {
			return true, fmt.Errorf("GitHub %s requires an explicit owner/repository; select an existing Git remote with --remote NAME", cmd.Name())
		}
		if cmd.Flags().Changed("force") {
			return true, fmt.Errorf("--force cannot bypass GitHub origin or repository validation; there is no SQLite action replay to reset")
		}
		remote, _ := cmd.Flags().GetString("remote")
		if remote == "" {
			remote = cfg.GitHub.Remote
		}
		target, err := ghstore.ResolveRepository(cmd.Context(), getBaseDir(), remote)
		if err != nil {
			return true, err
		}
		if !strings.EqualFold(args[0], target.Repo) {
			return true, fmt.Errorf("requested repository %q does not match Git remote %q (%s); configure the desired remote explicitly first", args[0], remote, target.Repo)
		}
		if err := config.SetStore(getBaseDir(), config.StoreGitHub, target); err != nil {
			return true, err
		}
		return true, reportGitHubProject(cmd, cmd.Name(), target)
	case "list":
		if _, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub); err != nil {
			return true, err
		}
		if jsonMode(cmd) {
			return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "scope": "configured-repository", "projects": []*models.GitHubStoreConfig{cfg.GitHub}, "note": "Configured repository only; not an account-wide repository listing."})
		}
		cmd.Printf("Configured GitHub repository: %s (remote: %s)\n", cfg.GitHub.Repo, cfg.GitHub.Remote)
		return true, nil
	case "unlink":
		return true, fmt.Errorf("GitHub issue storage requires a repository; unlink cannot leave gh-issue without one. To switch storage explicitly use 'td config set store sqlite' after exporting/importing any data you need; no migration or remote deletion was attempted")
	case "members":
		client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
		if err != nil {
			return true, err
		}
		members, err := client.Collaborators(cmd.Context())
		if err != nil {
			return true, err
		}
		note := "Visible GitHub collaborators and effective roles; may include organization/team/inherited access. This list cannot identify the source of a grant and does not include pending invitations or td-sync roles."
		if jsonMode(cmd) {
			return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "repository": cfg.GitHub.Repo, "scope": "visible-collaborators", "members": members, "note": note})
		}
		cmd.Printf("GitHub collaborators for %s (%d)\n", cfg.GitHub.Repo, len(members))
		for _, member := range members {
			cmd.Printf("%s (%d) role=%s\n", output.SanitizeIssueText(member.Login), member.ID, output.SanitizeIssueText(member.RoleName))
		}
		if len(members) == 0 {
			cmd.Println("No visible collaborators.")
		}
		cmd.Println(note)
		return true, nil
	case "kick":
		if len(args) != 1 {
			return true, fmt.Errorf("GitHub kick requires one explicit account login")
		}
		if err := ghstore.ValidateCollaboratorLogin(args[0]); err != nil {
			return true, err
		}
		client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
		if err != nil {
			return true, err
		}
		if err := client.RemoveCollaborator(cmd.Context(), args[0]); err != nil {
			return true, err
		}
		note := "Repository collaborator removal completed; organization/team/other inherited access may remain and related GitHub changes may finish asynchronously. Effective access was not verified."
		if jsonMode(cmd) {
			return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "repository": cfg.GitHub.Repo, "account": args[0], "action": "remove-repository-collaborator", "effective_access_verified": false, "note": note})
		}
		cmd.Printf("GitHub collaborator removal completed: %s from %s\n%s\n", args[0], cfg.GitHub.Repo, note)
		return true, nil
	default:
		return true, fmt.Errorf("GitHub sync-project %s is not implemented yet; no td-sync, SQLite or GitHub write was attempted. Use GitHub repository settings or gh for repository/access management", cmd.Name())
	}
}

func reportGitHubProject(cmd *cobra.Command, action string, target *models.GitHubStoreConfig) error {
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "action": action, "github": target, "remote_changed": false, "data_migrated": false})
	}
	cmd.Printf("GitHub repository selected: %s (remote: %s); Git remotes and existing task data were not changed.\n", target.Repo, target.Remote)
	return nil
}
