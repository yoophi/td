package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAccessibleRepositoriesBulkWhitelistAndEmpty(t *testing.T) {
	row := `{"id":1,"full_name":"owner/one","visibility":"private","has_issues":true,"archived":false,"disabled":false,"permissions":{"pull":true},"temp_clone_token":"fixture-secret-not-for-output"}`
	for _, data := range []string{"[[" + row + "],[]]", `[]`, `[[]]`} {
		calls := 0
		client := &Client{repo: "current/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
			calls++
			joined := strings.Join(args, " ")
			if len(payload) != 0 || !strings.Contains(joined, "--method GET user/repos?") || !strings.Contains(joined, "--paginate --slurp") || strings.Contains(joined, "repos/current/repo") {
				t.Fatal("per-repo request", args)
			}
			return []byte(data), nil
		}}
		repositories, err := client.AccessibleRepositories(context.Background())
		if err != nil || repositories == nil || calls != 1 {
			t.Fatal(repositories, err, calls)
		}
		raw, err := json.Marshal(repositories)
		if err != nil || strings.Contains(string(raw), "fixture-secret") || strings.Contains(string(raw), "temp_clone_token") {
			t.Fatal("raw credential field leaked", string(raw), err)
		}
	}
}

func TestAccessibleRepositoriesFailuresNoPartialResultsOrRetry(t *testing.T) {
	for _, data := range []string{`null`, `[null]`, `{}`, `[[{"id":1,"full_name":"owner/one"}]]`, `[[{"id":1,"full_name":"owner/one","visibility":"private","has_issues":true,"archived":false,"disabled":false}],[{"id":0}]]`} {
		client := &Client{run: func(context.Context, string, []byte, ...string) ([]byte, error) { return []byte(data), nil }}
		if rows, err := client.AccessibleRepositories(context.Background()); err == nil || rows != nil {
			t.Fatal("partial/malformed success", rows, err)
		}
	}
	calls := 0
	client := &Client{run: func(context.Context, string, []byte, ...string) ([]byte, error) {
		calls++
		return nil, fmt.Errorf("fixture HTTP 403 repo-list denied")
	}}
	if rows, err := client.AccessibleRepositories(context.Background()); err == nil || rows != nil || calls != 1 || !strings.Contains(err.Error(), "fixture HTTP 403") {
		t.Fatal(rows, err, calls)
	}
}
