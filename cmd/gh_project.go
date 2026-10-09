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
		if cmd.Flags().Changed("all") {
			return true, fmt.Errorf("--all applies only to GitHub accessible repository listing")
		}
		if cmd.Flags().Changed("visibility") {
			return true, fmt.Errorf("--visibility applies only to GitHub repository creation")
		}
		if cmd.Flags().Changed("custom-role") {
			return true, fmt.Errorf("--custom-role applies only to GitHub permissions")
		}
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
	case "create":
		if len(args) != 1 {
			return true, fmt.Errorf("GitHub create requires an explicit owner/repository")
		}
		visibility, _ := cmd.Flags().GetString("visibility")
		if err := ghstore.ValidateRepositoryCreation(args[0], visibility); err != nil {
			return true, err
		}
		if _, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub); err != nil {
			return true, err
		}
		description, _ := cmd.Flags().GetString("description")
		repo, err := ghstore.CreateRepository(cmd.Context(), getBaseDir(), args[0], visibility, description)
		if err != nil {
			return true, err
		}
		note := "Created repository only; current Git remotes/store selection and local commits were not changed. Configure an explicit Git remote and select it separately; no data migration occurred."
		if !repo.HasIssues {
			note += " Issues are disabled; enable them before selecting this repository for gh-issue."
		}
		if jsonMode(cmd) {
			return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "action": "create-repository", "created": repo, "linked": false, "remote_changed": false, "data_migrated": false, "note": note})
		}
		cmd.Printf("Created GitHub repository: %s (%s)\n%s\n", repo.FullName, repo.Visibility, note)
		return true, nil
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
		client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
		if err != nil {
			return true, err
		}
		all, _ := cmd.Flags().GetBool("all")
		if all {
			repositories, err := client.AccessibleRepositories(cmd.Context())
			if err != nil {
				return true, err
			}
			note := "Repositories visible to the active credential through owner/collaborator/organization affiliation; token scope may limit results. This does not select a repository or prove issue-write access. Each selection still requires an explicit matching Git remote and repository validation."
			if jsonMode(cmd) {
				return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "scope": "credential-accessible-repositories", "configured_repository": cfg.GitHub.Repo, "projects": repositories, "note": note})
			}
			cmd.Printf("Credential-visible GitHub repositories (%d)\n", len(repositories))
			for _, repo := range repositories {
				cmd.Printf("%s visibility=%s issues=%t archived=%t disabled=%t\n", output.SanitizeIssueText(repo.FullName), repo.Visibility, *repo.HasIssues, *repo.Archived, *repo.Disabled)
			}
			if len(repositories) == 0 {
				cmd.Println("No repositories visible to this credential.")
			}
			cmd.Println(note)
			return true, nil
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
	case "invite", "role":
		if len(args) != 2 {
			return true, fmt.Errorf("GitHub %s requires an explicit login and permission; no email or td-sync role conversion is performed", cmd.Name())
		}
		custom, _ := cmd.Flags().GetBool("custom-role")
		if err := ghstore.ValidateCollaboratorLogin(args[0]); err != nil {
			return true, err
		}
		if err := ghstore.ValidateCollaboratorPermission(args[1], custom); err != nil {
			return true, err
		}
		client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
		if err != nil {
			return true, err
		}
		result, err := client.GrantCollaboratorPermission(cmd.Context(), args[0], args[1], custom, cmd.Name() == "role")
		if err != nil {
			return true, err
		}
		note := "Requested GitHub repository permission; effective inherited permissions were not verified. Invitation creation is pending acceptance, not proof of membership. Existing-collaborator checks are best-effort, not an atomic lock."
		if jsonMode(cmd) {
			return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "repository": cfg.GitHub.Repo, "account": args[0], "action": cmd.Name(), "result": result, "effective_access_verified": false, "note": note})
		}
		cmd.Printf("GitHub %s: %s for %s, requested permission=%s\n", cmd.Name(), result.Outcome, args[0], output.SanitizeIssueText(args[1]))
		if result.InvitationID != 0 {
			cmd.Printf("Invitation: %d\n", result.InvitationID)
		}
		cmd.Println(note)
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
