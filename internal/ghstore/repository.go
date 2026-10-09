// Package ghstore validates GitHub storage through the installed gh CLI.
package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
	"golang.org/x/sync/singleflight"
)

type runner func(context.Context, string, string, ...string) ([]byte, error)

func runCommand(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	if name == "gh" && len(args) > 0 && args[0] == "api" {
		return runAPI(ctx, dir, nil, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	data, err := cmd.Output()
	if name == "gh" && len(args) > 0 && args[0] == "auth" {
		recordAuthCost(ctx)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && name == "gh" {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		err = classifyRateLimit(err)
	}
	return data, err
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$`)

// repositoryFromRemote accepts GitHub HTTPS, SSH and SCP-style Git URLs.
// Local paths and other hosts must never be treated as GitHub repositories.
func repositoryFromRemote(remoteURL string) (string, error) {
	var path string
	if strings.HasPrefix(remoteURL, "git@github.com:") {
		path = strings.TrimPrefix(remoteURL, "git@github.com:")
	} else {
		u, err := url.Parse(remoteURL)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") ||
			(u.Scheme != "https" && u.Scheme != "ssh") || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
			return "", fmt.Errorf("selected remote must be a github.com HTTPS or SSH repository")
		}
		path = strings.TrimPrefix(u.Path, "/")
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if !repositoryPattern.MatchString(path) || strings.HasSuffix(path, "/.") || strings.HasSuffix(path, "/..") {
		return "", fmt.Errorf("selected remote must identify a GitHub owner/repository")
	}
	return path, nil
}

// ResolveRepository verifies the actual Git remote, gh authentication and Issues availability.
// All calls are read-only; no issue, label or repository setting is changed.
func ResolveRepository(ctx context.Context, baseDir, remote string) (*models.GitHubStoreConfig, error) {
	return resolveRepositoryCredential(ctx, baseDir, remote, nil)
}

func resolveRepositoryCredential(ctx context.Context, baseDir, remote string, credential func([]byte)) (*models.GitHubStoreConfig, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("gh CLI not found or not executable in PATH; install GitHub CLI before selecting gh-issue: %w", err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not found or not executable in PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return resolveRepositoryWithCredential(ctx, baseDir, remote, runCommand, credential)
}

func resolveRepository(ctx context.Context, baseDir, remote string, run runner) (*models.GitHubStoreConfig, error) {
	return resolveRepositoryWithCredential(ctx, baseDir, remote, run, nil)
}

func resolveRepositoryWithCredential(ctx context.Context, baseDir, remote string, run runner, credential func([]byte)) (*models.GitHubStoreConfig, error) {
	if remote == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, "\r\n\t ") {
		return nil, fmt.Errorf("a valid Git remote name is required")
	}
	data, err := run(ctx, baseDir, "git", "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return nil, fmt.Errorf("project is not an accessible Git working tree: %w", err)
	}
	if strings.TrimSpace(string(data)) != "true" {
		return nil, fmt.Errorf("project must be a Git working tree to use gh-issue")
	}
	data, err = run(ctx, baseDir, "git", "remote", "get-url", "--", remote)
	if err != nil {
		return nil, fmt.Errorf("git remote %q is missing or unreadable: %w; configure this remote with a GitHub repository URL first", remote, err)
	}
	repo, err := repositoryFromRemote(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, err
	}
	// auth status probes REST / and can misreport a rate-limited token as
	// invalid. Check local credential availability without that network probe;
	// the repository API below validates access and preserves the real error.
	// The returned credential is discarded and must never be logged.
	token, err := run(ctx, baseDir, "gh", "auth", "token", "--hostname", "github.com")
	if err != nil {
		return nil, fmt.Errorf("GitHub authentication failed: %w; run 'gh auth login --hostname github.com'", err)
	}
	if credential != nil {
		credential(token)
	}
	cooldown := newAPICooldown(repo, token)
	if err := cooldown.check(ctx, time.Now()); err != nil {
		return nil, err
	}
	data, err = repositoryPreflight(ctx, baseDir, repo, run, cooldown)
	if err != nil {
		return nil, fmt.Errorf("cannot access GitHub repository %s: %w; check that it exists, your account has access, and the network is available", repo, err)
	}
	var info struct {
		FullName  string `json:"full_name"`
		HasIssues bool   `json:"has_issues"`
		Archived  bool   `json:"archived"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("invalid gh repository response: %w", err)
	}
	if !repositoryPattern.MatchString(info.FullName) {
		return nil, fmt.Errorf("gh returned an invalid repository name")
	}
	if !info.HasIssues {
		return nil, fmt.Errorf("GitHub Issues is disabled for %s; enable Issues in the repository settings first", info.FullName)
	}
	if info.Archived {
		return nil, fmt.Errorf("GitHub repository %s is archived", info.FullName)
	}
	return &models.GitHubStoreConfig{Remote: remote, Repo: info.FullName}, nil
}

// Only overlapping remote checks are shared. Completed results are not cached:
// later Open calls must still detect changed permissions or repository settings.
var repositoryPreflights singleflight.Group

func repositoryPreflight(ctx context.Context, dir, repo string, run runner, cooldown *apiCooldownScope) ([]byte, error) {
	fetch := func() (any, error) {
		if err := cooldown.check(ctx, time.Now()); err != nil {
			return nil, err
		}
		data, err := run(ctx, dir, "gh", "api", "--hostname", "github.com", "repos/"+repo)
		cooldown.observe(err, time.Now())
		return data, err
	}
	if cooldown == nil {
		data, err := fetch()
		if err != nil {
			return nil, err
		}
		return data.([]byte), nil
	}
	result := repositoryPreflights.DoChan(cooldown.key, fetch)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case observed := <-result:
		if observed.Err != nil {
			return nil, observed.Err
		}
		return observed.Val.([]byte), nil
	}
}
