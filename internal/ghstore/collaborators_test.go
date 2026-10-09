package ghstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestCollaboratorsBulkPagesPreserveGitHubPermissions(t *testing.T) {
	calls := 0
	client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		calls++
		joined := strings.Join(args, " ")
		if len(payload) != 0 || !strings.Contains(joined, "--method GET") || !strings.Contains(joined, "repos/owner/repo/collaborators?affiliation=all&per_page=100") || !strings.Contains(joined, "--paginate --slurp") {
			t.Fatal("unexpected collection request", args)
		}
		return []byte(`[[{"id":1,"login":"one","role_name":"custom-role","permissions":{"pull":true,"push":false}}],[{"id":2,"login":"two","role_name":"maintain","permissions":{"maintain":true}}]]`), nil
	}}
	members, err := client.Collaborators(context.Background())
	if err != nil || calls != 1 || len(members) != 2 || members[0].RoleName != "custom-role" || members[0].Permissions["push"] || !members[1].Permissions["maintain"] {
		t.Fatal(members, calls, err)
	}
}

func TestCollaboratorsEmptyAndFailuresDoNotYieldPartialSuccess(t *testing.T) {
	for _, data := range []string{`[]`, `[[]]`} {
		client := &Client{run: func(context.Context, string, []byte, ...string) ([]byte, error) { return []byte(data), nil }}
		members, err := client.Collaborators(context.Background())
		if err != nil || members == nil || len(members) != 0 {
			t.Fatal(members, err)
		}
	}
	for _, data := range []string{`null`, `[null]`, `{}`, `[[{"id":1,"login":"one"}],[{"id":0,"login":"bad"}]]`, `[[{"id":1}]]`} {
		client := &Client{run: func(context.Context, string, []byte, ...string) ([]byte, error) { return []byte(data), nil }}
		if members, err := client.Collaborators(context.Background()); err == nil || members != nil {
			t.Fatal("malformed response yielded success", members, err)
		}
	}
	calls := 0
	client := &Client{run: func(context.Context, string, []byte, ...string) ([]byte, error) {
		calls++
		return nil, fmt.Errorf("fixture HTTP 403 permission denied")
	}}
	if members, err := client.Collaborators(context.Background()); err == nil || members != nil || !strings.Contains(err.Error(), "fixture HTTP 403") || calls != 1 {
		t.Fatal(members, err, calls)
	}
}
