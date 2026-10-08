package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func prepareProjectStore(cmd *cobra.Command, store, remote string) (*models.GitHubStoreConfig, error) {
	switch store {
	case config.StoreSQLite:
		return nil, nil
	case config.StoreGitHub:
		return ghstore.ResolveRepository(cmd.Context(), getBaseDir(), remote)
	default:
		return nil, fmt.Errorf("invalid store %q (use sqlite or gh-issue)", store)
	}
}

func setProjectStore(cmd *cobra.Command, store, remote string) error {
	github, err := prepareProjectStore(cmd, store, remote)
	if err != nil {
		return err
	}
	if err := config.SetStore(getBaseDir(), store, github); err != nil {
		return err
	}
	return reportProjectStore(cmd, store, github)
}

func reportProjectStore(cmd *cobra.Command, store string, github *models.GitHubStoreConfig) error {
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Store                    string                    `json:"store"`
			GitHub                   *models.GitHubStoreConfig `json:"github,omitempty"`
			IssueOperationsSupported bool                      `json:"issue_operations_supported"`
		}{store, github, true})
	}
	cmd.Printf("Project store: %s\n", store)
	if github != nil {
		cmd.Printf("GitHub repository: %s (remote: %s)\n", github.Repo, github.Remote)
		cmd.Println("Supported issue commands: create, list, show, update, close, reopen. Existing data has not been migrated.")
	}
	return nil
}

var configSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactively select this project's issue store",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if jsonMode(cmd) {
			return fmt.Errorf("config setup is interactive; use 'td config set store <sqlite|gh-issue> --json'")
		}
		cfg, err := config.Load(getBaseDir())
		if err != nil {
			return err
		}
		current, err := config.Store(cfg)
		if err != nil {
			return err
		}
		reader := bufio.NewReader(cmd.InOrStdin())
		cmd.Println("Select project issue store:")
		cmd.Println("  1. SQLite — local database")
		cmd.Println("  2. GitHub Issues — requires a GitHub remote")
		selection, err := readStoreChoice(cmd, reader, "Store", current)
		if err != nil {
			return err
		}
		switch selection {
		case "1":
			selection = config.StoreSQLite
		case "2":
			selection = config.StoreGitHub
		}
		remote := "origin"
		if selection == config.StoreGitHub {
			if cfg.GitHub != nil {
				remote = cfg.GitHub.Remote
			}
			remote, err = readStoreChoice(cmd, reader, "Git remote", remote)
			if err != nil {
				return err
			}
		}
		return setProjectStore(cmd, selection, remote)
	},
}

func readStoreChoice(cmd *cobra.Command, reader *bufio.Reader, label, fallback string) (string, error) {
	cmd.Printf("%s [%s]: ", label, fallback)
	line, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return "", fmt.Errorf("setup cancelled: input ended; use 'td config set store <sqlite|gh-issue>' for non-interactive setup")
		}
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = fallback
	}
	return line, nil
}
