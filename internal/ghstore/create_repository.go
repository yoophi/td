package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type CreatedRepository struct {
	FullName   string `json:"full_name"`
	Visibility string `json:"visibility"`
	HasIssues  bool   `json:"has_issues"`
}

func ValidateRepositoryCreation(name, visibility string) error {
	if !repositoryPattern.MatchString(name) || strings.HasSuffix(name, "/.") || strings.HasSuffix(name, "/..") {
		return fmt.Errorf("repository creation requires an explicit GitHub owner/repository")
	}
	switch visibility {
	case "public", "private", "internal":
		return nil
	default:
		return fmt.Errorf("repository creation requires --visibility public|private|internal")
	}
}

// CreateRepository makes one creation attempt, then reads its explicit target.
// It never clones, pushes, links a store, or changes the local Git remotes.
func CreateRepository(ctx context.Context, dir, name, visibility, description string) (*CreatedRepository, error) {
	if err := ValidateRepositoryCreation(name, visibility); err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("gh CLI not found or not executable in PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return createRepository(ctx, dir, name, visibility, description, runCommand)
}

func createRepository(ctx context.Context, dir, name, visibility, description string, run runner) (*CreatedRepository, error) {
	if err := ValidateRepositoryCreation(name, visibility); err != nil {
		return nil, err
	}
	if _, err := run(ctx, dir, "gh", "repo", "create", name, "--"+visibility, "--description", description); err != nil {
		return nil, fmt.Errorf("gh repository creation for %s failed: %w; creation may have reached GitHub, inspect that repository before retrying; no automatic retry or local relink", name, err)
	}
	data, err := run(ctx, dir, "gh", "api", "--hostname", "github.com", "repos/"+name)
	if err != nil {
		return nil, fmt.Errorf("repository creation command succeeded but verification of %s failed: %w; inspect remote state before any retry; local repository selection is unchanged", name, err)
	}
	var info struct {
		FullName   string `json:"full_name"`
		Visibility string `json:"visibility"`
		HasIssues  *bool  `json:"has_issues"`
	}
	if err := json.Unmarshal(data, &info); err != nil || !strings.EqualFold(info.FullName, name) || info.Visibility != visibility || info.HasIssues == nil {
		return nil, fmt.Errorf("repository creation command succeeded but %s identity/visibility/Issues response was not verified; inspect remote state before retrying; local repository selection is unchanged", name)
	}
	return &CreatedRepository{FullName: info.FullName, Visibility: info.Visibility, HasIssues: *info.HasIssues}, nil
}
