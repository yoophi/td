package ghstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRepositoryFromRemote(t *testing.T) {
	for _, raw := range []string{"https://github.com/owner/repo.git", "git@github.com:owner/repo.git", "ssh://git@github.com/owner/repo.git", "https://github.com/owner/repo/"} {
		got, err := repositoryFromRemote(raw)
		if err != nil || got != "owner/repo" {
			t.Errorf("%s: got %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"/tmp/repo", "https://gitlab.com/owner/repo", "https://github.com.evil.test/owner/repo", "https://github.com/owner", "https://github.com/owner/repo/issues", "https://github.com/owner/repo?x=y", "https://github.com/owner/..", "https://github.com/owner/repo#x", "git@github.com:owner/repo;echo x"} {
		if _, err := repositoryFromRemote(raw); err == nil {
			t.Errorf("accepted invalid remote %q", raw)
		}
	}
}

func TestResolveRepositoryUsesSelectedRemote(t *testing.T) {
	want := [][]string{
		{"git", "rev-parse", "--is-inside-work-tree"},
		{"git", "remote", "get-url", "--", "upstream"},
		{"gh", "auth", "status", "--hostname", "github.com"},
		{"gh", "api", "--hostname", "github.com", "repos/owner/project"},
	}
	replies := []string{"true\n", "git@github.com:owner/project.git\n", "", `{"full_name":"owner/renamed-project","has_issues":true}`}
	var calls [][]string
	run := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if dir != "/project" {
			t.Fatalf("wrong directory: %s", dir)
		}
		calls = append(calls, append([]string{name}, args...))
		return []byte(replies[len(calls)-1]), nil
	}
	got, err := resolveRepository(context.Background(), "/project", "upstream", run)
	if err != nil {
		t.Fatal(err)
	}
	if got.Remote != "upstream" || got.Repo != "owner/renamed-project" || !reflect.DeepEqual(calls, want) {
		t.Fatalf("got %+v; calls=%v", got, calls)
	}
}

func TestResolveRepositoryRejectsUnavailableStore(t *testing.T) {
	for _, tc := range []struct {
		name, remoteURL, response, message string
		failAt, calls                      int
	}{
		{"not a repository", "", "", "Git working tree", 1, 1},
		{"missing remote", "", "", "remote \"origin\" is missing", 2, 2},
		{"other host", "https://gitlab.com/o/r", "", "github.com", 0, 2},
		{"authentication", "https://github.com/o/r", "", "authentication", 3, 3},
		{"access", "https://github.com/o/r", "", "cannot access", 4, 4},
		{"disabled issues", "https://github.com/o/r", `{"full_name":"o/r","has_issues":false}`, "disabled", 0, 4},
		{"archived", "https://github.com/o/r", `{"full_name":"o/r","has_issues":true,"archived":true}`, "archived", 0, 4},
		{"invalid json", "https://github.com/o/r", "broken", "invalid gh", 0, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			run := func(context.Context, string, string, ...string) ([]byte, error) {
				calls++
				if calls == tc.failAt {
					return nil, errors.New("command failed")
				}
				if calls == 1 {
					return []byte("true\n"), nil
				}
				if calls == 2 {
					return []byte(tc.remoteURL), nil
				}
				return []byte(tc.response), nil
			}
			_, err := resolveRepository(context.Background(), "/project", "origin", run)
			if err == nil || !strings.Contains(err.Error(), tc.message) || calls != tc.calls {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
}
