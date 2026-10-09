package ghstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestDiagnosticsReadOnlyFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, missing, remote, response, failCommand, failCheck string
		wantOK                                                  bool
	}{
		{name: "success empty issues", wantOK: true},
		{name: "missing gh", missing: "gh", failCheck: "gh CLI"},
		{name: "missing git", missing: "git", failCheck: "git CLI"},
		{name: "not working tree", failCommand: "rev-parse", failCheck: "Git working tree"},
		{name: "missing remote", failCommand: "remote", failCheck: "Git remote"},
		{name: "non GitHub", remote: "https://gitlab.com/owner/repo", failCheck: "Git remote"},
		{name: "authentication", failCommand: "auth", failCheck: "Authentication"},
		{name: "repository access", failCommand: "repository", failCheck: "Repository API"},
		{name: "invalid JSON", response: "broken", failCheck: "Repository API"},
		{name: "missing issues field", response: `{"full_name":"owner/repo"}`, failCheck: "Repository API"},
		{name: "pinned repo changed", response: `{"full_name":"owner/other","has_issues":true}`, failCheck: "Pinned repository"},
		{name: "disabled Issues", response: `{"full_name":"owner/repo","has_issues":false}`, failCheck: "Issues enabled"},
		{name: "archived", response: `{"full_name":"owner/repo","has_issues":true,"archived":true}`, failCheck: "Repository writable"},
		{name: "issue permissions", failCommand: "issues", failCheck: "Issues read access"},
		{name: "invalid issues response", failCommand: "issues json", failCheck: "Issues read access"},
		{name: "read only repository", response: `{"full_name":"owner/repo","has_issues":true,"permissions":{"push":false}}`, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(name string) (string, error) {
				if name == tc.missing {
					return "", errors.New("missing")
				}
				return "/bin/" + name, nil
			}
			run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
				if dir != "/project" {
					t.Fatal(dir)
				}
				key := args[0]
				result := ""
				if name == "git" {
					switch key {
					case "rev-parse":
						result = "true"
					case "remote":
						result = tc.remote
						if result == "" {
							result = "https://github.com/owner/repo.git"
						}
					default:
						t.Fatal("unexpected git mutation", args)
					}
				} else if key == "api" {
					if len(args) != 4 || args[1] != "--hostname" || args[2] != "github.com" {
						t.Fatal(args)
					}
					switch args[3] {
					case "repos/owner/repo":
						key = "repository"
						result = tc.response
						if result == "" {
							result = `{"full_name":"owner/repo","has_issues":true}`
						}
					case "repos/owner/repo/issues?state=all&per_page=1":
						key = "issues"
						result = "[]"
						if tc.failCommand == "issues json" {
							result = "null"
						}
					default:
						t.Fatal("unexpected API path", args)
					}
				} else if key != "auth" {
					t.Fatal("unexpected gh mutation", args)
				}
				if key == tc.failCommand {
					return nil, errors.New("controlled failure")
				}
				return []byte(result), nil
			}
			r := diagnose(context.Background(), "/project", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}, lookup, run)
			if r.OK != tc.wantOK {
				t.Fatalf("%+v", r)
			}
			if tc.failCheck != "" {
				found := false
				for _, c := range r.Checks {
					if c.Name == tc.failCheck && c.Status == "FAIL" {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing failed check %s: %+v", tc.failCheck, r)
				}
			}
		})
	}
}

func TestDiagnosticsConfigurationCancellationAndRename(t *testing.T) {
	lookup := func(name string) (string, error) { return name, nil }
	calls := 0
	run := func(context.Context, string, string, ...string) ([]byte, error) {
		calls++
		return nil, context.Canceled
	}
	r := diagnose(context.Background(), "/project", nil, lookup, run)
	if r.OK || calls != 0 {
		t.Fatalf("%+v calls=%d", r, calls)
	}
	r = diagnose(context.Background(), "/project", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}, lookup, run)
	if r.OK {
		t.Fatal(r)
	}
	if !strings.Contains(r.Checks[3].Message, "context canceled") {
		t.Fatal(r)
	}
	run = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if name == "git" {
			if args[0] == "rev-parse" {
				return []byte("true"), nil
			}
			return []byte("https://github.com/old/repo.git"), nil
		}
		if args[0] == "auth" {
			return nil, nil
		}
		if args[3] == "repos/old/repo" {
			return []byte(`{"full_name":"owner/repo","has_issues":true}`), nil
		}
		if args[3] == "repos/owner/repo/issues?state=all&per_page=1" {
			return []byte("[]"), nil
		}
		t.Fatal(args)
		return nil, nil
	}
	r = diagnose(context.Background(), "/project", &models.GitHubStoreConfig{Remote: "upstream", Repo: "owner/repo"}, lookup, run)
	if !r.OK {
		t.Fatal(r)
	}
}
