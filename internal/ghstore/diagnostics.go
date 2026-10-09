package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

type DiagnosticCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type DiagnosticReport struct {
	OK         bool              `json:"ok"`
	Store      string            `json:"store"`
	Repository string            `json:"repository,omitempty"`
	Checks     []DiagnosticCheck `json:"checks"`
}

// Diagnose performs only local reads, gh auth status and HTTP GET requests.
// Successful reads cannot prove permission to perform every kind of write.
func Diagnose(ctx context.Context, dir string, cfg *models.GitHubStoreConfig) DiagnosticReport {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return diagnose(ctx, dir, cfg, exec.LookPath, runCommand)
}

func diagnose(ctx context.Context, dir string, cfg *models.GitHubStoreConfig, lookup func(string) (string, error), run runner) DiagnosticReport {
	r := DiagnosticReport{OK: true, Store: "gh-issue", Checks: []DiagnosticCheck{}}
	add := func(name, status, message string) {
		r.Checks = append(r.Checks, DiagnosticCheck{Name: name, Status: status, Message: message})
		if status == "FAIL" {
			r.OK = false
		}
	}
	gh, err := lookup("gh")
	ghOK := err == nil
	if err != nil {
		add("gh CLI", "FAIL", "gh CLI not found or not executable in PATH; install GitHub CLI")
	} else {
		add("gh CLI", "OK", gh)
	}
	git, err := lookup("git")
	gitOK := err == nil
	if err != nil {
		add("git CLI", "FAIL", "git not found or not executable in PATH")
	} else {
		add("git CLI", "OK", git)
	}
	if cfg == nil || cfg.Remote == "" || !repositoryPattern.MatchString(cfg.Repo) {
		add("Configuration", "FAIL", "repository is not configured; run td config set store gh-issue")
		return r
	}
	r.Repository = cfg.Repo
	add("Configuration", "OK", fmt.Sprintf("remote=%s, repository=%s", cfg.Remote, cfg.Repo))
	repo := ""
	if !gitOK {
		add("Git remote", "SKIP", "git is unavailable")
	} else if strings.HasPrefix(cfg.Remote, "-") || strings.ContainsAny(cfg.Remote, "\r\n\t ") {
		add("Git remote", "FAIL", "invalid configured remote name")
	} else {
		data, e := run(ctx, dir, "git", "rev-parse", "--is-inside-work-tree")
		if e != nil || strings.TrimSpace(string(data)) != "true" {
			add("Git working tree", "FAIL", fmt.Sprintf("project is not an accessible Git working tree: %v", e))
		} else {
			add("Git working tree", "OK", dir)
			data, e = run(ctx, dir, "git", "remote", "get-url", "--", cfg.Remote)
			if e != nil {
				add("Git remote", "FAIL", fmt.Sprintf("remote %q is missing or unreadable: %v", cfg.Remote, e))
			} else {
				repo, e = repositoryFromRemote(strings.TrimSpace(string(data)))
				if e != nil {
					add("Git remote", "FAIL", e.Error())
				} else {
					add("Git remote", "OK", cfg.Remote+": "+repo)
				}
			}
		}
	}
	authOK := false
	if !ghOK {
		add("Authentication", "SKIP", "gh is unavailable")
	} else {
		_, e := run(ctx, dir, "gh", "auth", "status", "--hostname", "github.com")
		if e != nil {
			add("Authentication", "FAIL", fmt.Sprintf("GitHub authentication failed: %v; run gh auth login --hostname github.com", e))
		} else {
			authOK = true
			add("Authentication", "OK", "github.com")
		}
	}
	if !authOK || repo == "" {
		add("Repository API", "SKIP", "authentication or Git remote check failed")
		return r
	}
	data, e := run(ctx, dir, "gh", "api", "--hostname", "github.com", "repos/"+repo)
	if e != nil {
		add("Repository API", "FAIL", fmt.Sprintf("cannot access %s: %v; check repository access and network", repo, e))
		return r
	}
	var info struct {
		FullName    string          `json:"full_name"`
		HasIssues   *bool           `json:"has_issues"`
		Archived    bool            `json:"archived"`
		Permissions map[string]bool `json:"permissions"`
	}
	if e = json.Unmarshal(data, &info); e != nil || !repositoryPattern.MatchString(info.FullName) || info.HasIssues == nil {
		add("Repository API", "FAIL", "invalid gh repository response")
		return r
	}
	add("Repository API", "OK", info.FullName)
	if !strings.EqualFold(info.FullName, cfg.Repo) {
		add("Pinned repository", "FAIL", fmt.Sprintf("remote now resolves to %s, configured store is %s; explicitly reconfigure with td config set store gh-issue --remote %s", info.FullName, cfg.Repo, cfg.Remote))
		return r
	}
	add("Pinned repository", "OK", cfg.Repo)
	if !*info.HasIssues {
		add("Issues enabled", "FAIL", "GitHub Issues is disabled; enable Issues in repository settings")
	} else {
		add("Issues enabled", "OK", "enabled")
	}
	if info.Archived {
		add("Repository writable", "FAIL", "repository is archived")
	} else {
		permission := "write permissions are not tested by this read-only diagnostic"
		if info.Permissions != nil {
			permission = fmt.Sprintf("reported permissions: push=%t, triage=%t, maintain=%t, admin=%t; individual write operations may still be denied", info.Permissions["push"], info.Permissions["triage"], info.Permissions["maintain"], info.Permissions["admin"])
		}
		add("Write permissions", "WARN", permission)
	}
	if !*info.HasIssues {
		add("Issues read access", "SKIP", "Issues is disabled")
		return r
	}
	data, e = run(ctx, dir, "gh", "api", "--hostname", "github.com", "repos/"+cfg.Repo+"/issues?state=all&per_page=1")
	if e != nil {
		add("Issues read access", "FAIL", e.Error())
	} else {
		var issues []json.RawMessage
		if e = json.Unmarshal(data, &issues); e != nil || strings.TrimSpace(string(data)) == "null" {
			add("Issues read access", "FAIL", "invalid gh issues response")
		} else {
			add("Issues read access", "OK", "GET issues succeeded (no changes made)")
		}
	}
	return r
}
