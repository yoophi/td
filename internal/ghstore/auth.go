package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// AuthenticatedAccount validates the active gh credential without exposing it.
// Authentication is host-wide and does not require a working repository origin.
func AuthenticatedAccount(ctx context.Context, dir string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("gh CLI not found or not executable in PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	data, err := runCommand(ctx, dir, "gh", "api", "--hostname", "github.com", "user")
	if err != nil {
		return "", fmt.Errorf("GitHub authentication check failed: %w; run 'gh auth login --hostname github.com'", err)
	}
	var account struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(data, &account); err != nil || account.Login == "" {
		return "", fmt.Errorf("gh returned an invalid authenticated account response")
	}
	return account.Login, nil
}
