package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/marcus/td/internal/backendstore"
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
	kind, endpoint, err := config.ParseStore(store)
	if err != nil {
		return err
	}
	if kind == config.StoreBackend {
		if endpoint == "" {
			cfg, e := config.Load(getBaseDir())
			if e != nil {
				return e
			}
			if cfg.Backend != nil {
				endpoint = cfg.Backend.URL
			}
		}
		client, e := backendstore.NewClient(endpoint, os.Getenv("TD_BACKEND_TOKEN"))
		if e != nil {
			return e
		}
		association, e := client.Initialize(cmd.Context(), getBaseDir(), "")
		if e != nil {
			return e
		}
		return reportBackendStore(cmd, association)
	}
	store = kind
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
			return fmt.Errorf("config setup is interactive; use 'td config set store <sqlite|gh-issue|td-backend,URL> --json'")
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
		cmd.Println("  3. td-backend — requires API URL and TD_BACKEND_TOKEN")
		selection, err := readStoreChoice(cmd, reader, "Store", current)
		if err != nil {
			return err
		}
		switch selection {
		case "1":
			selection = config.StoreSQLite
		case "2":
			selection = config.StoreGitHub
		case "3":
			selection = config.StoreBackend
		}
		if selection == config.StoreBackend {
			endpoint := "http://localhost:7000"
			if cfg.Backend != nil {
				endpoint = cfg.Backend.URL
			}
			endpoint, err = readStoreChoice(cmd, reader, "Backend API URL", endpoint)
			if err != nil {
				return err
			}
			selection += "," + endpoint
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
			return "", fmt.Errorf("setup cancelled: input ended; use 'td config set store <sqlite|gh-issue|td-backend,URL>' for non-interactive setup")
		}
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = fallback
	}
	return line, nil
}

func reportBackendStore(cmd *cobra.Command, b models.BackendStoreConfig) error {
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Store     string                    `json:"store"`
			Backend   models.BackendStoreConfig `json:"backend"`
			Supported bool                      `json:"issue_operations_supported"`
		}{config.StoreBackend, b, false})
	}
	cmd.Printf("Project store: %s\nBackend URL: %s\nProject ID: %s\n", config.StoreBackend, b.URL, b.ProjectID)
	return nil
}
