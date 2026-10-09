package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/ghstore"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func githubAuth(cmd *cobra.Command, action string) (bool, error) {
	cfg, err := config.Load(getBaseDir())
	if err != nil {
		return true, err
	}
	kind, err := config.Store(cfg)
	if err != nil {
		return true, err
	}
	if kind != config.StoreGitHub {
		if !features.IsEnabledForProcess(features.SyncCLI.Name) {
			return true, fmt.Errorf("SQLite sync authentication requires the sync_cli feature")
		}
		return false, nil
	}
	if action == "status" {
		account, err := ghstore.AuthenticatedAccount(cmd.Context(), getBaseDir())
		if err != nil {
			return true, err
		}
		if jsonMode(cmd) {
			return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "hostname": "github.com", "account": account, "authenticated": true, "scope": "gh-host-account"})
		}
		cmd.Printf("GitHub: github.com\nAccount: %s\nCredentials: managed by gh; shared across projects\n", account)
		return true, nil
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return true, fmt.Errorf("gh CLI not found or not executable in PATH: %w", err)
	}
	args := []string{"auth", action, "--hostname", "github.com"}
	if action == "login" {
		if jsonMode(cmd) || !term.IsTerminal(int(os.Stdin.Fd())) {
			return true, fmt.Errorf("GitHub login requires an interactive terminal; run 'gh auth login --hostname github.com', or configure GH_TOKEN for automation; td does not receive or store tokens")
		}
		args = append(args, "--web")
	} else {
		account, _ := cmd.Flags().GetString("account")
		confirm, _ := cmd.Flags().GetBool("confirm-global")
		if !confirm || account == "" || strings.HasPrefix(account, "-") || strings.ContainsAny(account, "\r\n\t ") {
			return true, fmt.Errorf("GitHub logout affects every project using this gh account; specify --account LOGIN --confirm-global to remove its local github.com credential (tokens are not revoked)")
		}
		args = append(args, "--user", account)
	}
	child := exec.CommandContext(cmd.Context(), "gh", args...)
	child.Dir = getBaseDir()
	child.Stdin = os.Stdin
	// Keep gh's interactive/progress output away from JSON stdout.
	child.Stdout, child.Stderr = cmd.ErrOrStderr(), cmd.ErrOrStderr()
	if err := child.Run(); err != nil {
		if cmd.Context().Err() != nil {
			return true, cmd.Context().Err()
		}
		return true, fmt.Errorf("gh auth %s failed: %w; see gh diagnostics above", action, err)
	}
	if jsonMode(cmd) {
		return true, json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"store": "gh-issue", "hostname": "github.com", "action": action, "scope": "gh-host-account"})
	}
	cmd.Printf("GitHub %s completed; gh credentials are shared across projects.\n", action)
	return true, nil
}
