package ghstore

import (
	"context"
	"encoding/json"
	"errors"
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

func TestCollaboratorGrantReceiptsAndExistingOnlyRace(t *testing.T) {
	invitation := `{"id":17,"invitee":{"login":"some-user"},"repository":{"full_name":"owner/repo"}}`
	for _, tc := range []struct {
		existing bool
		response string
		outcome  string
		conflict bool
	}{{false, invitation, "invitation-created", false}, {false, "", "assignment-accepted", false}, {true, "", "assignment-accepted", false}, {true, invitation, "", true}} {
		calls := 0
		client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
			calls++
			if args[4] == "GET" {
				if args[5] == "repos/owner/repo" {
					return []byte(`{"full_name":"owner/repo","owner":{"type":"Organization"}}`), nil
				}
				return nil, nil
			}
			var body map[string]string
			if err := json.Unmarshal(payload, &body); err != nil || body["permission"] != "triage" || args[4] != "PUT" || args[5] != "repos/owner/repo/collaborators/some-user" {
				t.Fatal(args, string(payload), err)
			}
			return []byte(tc.response), nil
		}}
		result, err := client.GrantCollaboratorPermission(context.Background(), "some-user", "triage", false, tc.existing)
		wantCalls := 2
		if tc.existing {
			wantCalls = 3
		}
		if calls != wantCalls {
			t.Fatal("retry/unexpected read", calls)
		}
		if tc.conflict {
			var conflict *ConflictError
			if result != nil || !errors.As(err, &conflict) || !conflict.AfterWrite || !strings.Contains(err.Error(), "invitation 17") {
				t.Fatal(result, err)
			}
		} else if err != nil || result == nil || result.Outcome != tc.outcome {
			t.Fatal(result, err)
		}
	}
}

func TestCollaboratorGrantValidationOwnershipAndFailures(t *testing.T) {
	for _, permission := range []string{"", "writer", "reader", "owner", "write", "bogus"} {
		client := &Client{run: func(context.Context, string, []byte, ...string) ([]byte, error) {
			t.Fatal("invalid permission called gh")
			return nil, nil
		}}
		if _, err := client.GrantCollaboratorPermission(context.Background(), "user", permission, false, false); err == nil {
			t.Fatal(permission)
		}
	}
	for _, tc := range []struct {
		owner, permission string
		custom            bool
		allowed           bool
	}{{"User", "pull", false, false}, {"User", "push", false, true}, {"User", "custom", true, false}, {"Unknown", "push", false, false}, {"Organization", "Security Reader", true, true}} {
		writes := 0
		client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
			if args[4] == "GET" {
				return []byte(fmt.Sprintf(`{"full_name":"owner/repo","owner":{"type":%q}}`, tc.owner)), nil
			}
			writes++
			var body map[string]string
			if err := json.Unmarshal(payload, &body); err != nil || body["permission"] != tc.permission {
				t.Fatal(string(payload), err)
			}
			return nil, nil
		}}
		result, err := client.GrantCollaboratorPermission(context.Background(), "user", tc.permission, tc.custom, false)
		if tc.allowed {
			if err != nil || result == nil || writes != 1 {
				t.Fatal(tc, result, err)
			}
		} else if err == nil || result != nil || writes != 0 {
			t.Fatal(tc, result, err)
		}
	}
	for _, response := range []string{`null`, `{}`, `{"id":2,"invitee":{"login":"other"},"repository":{"full_name":"owner/repo"}}`} {
		client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
			if args[4] == "GET" {
				return []byte(`{"full_name":"owner/repo","owner":{"type":"Organization"}}`), nil
			}
			return []byte(response), nil
		}}
		if result, err := client.GrantCollaboratorPermission(context.Background(), "user", "push", false, false); err == nil || result != nil || !strings.Contains(err.Error(), "after permission write") {
			t.Fatal(result, err)
		}
	}
	for _, failMethod := range []string{"GET", "PUT"} {
		writes := 0
		client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
			if args[5] == "repos/owner/repo" {
				return []byte(`{"full_name":"owner/repo","owner":{"type":"Organization"}}`), nil
			}
			if args[4] == "PUT" {
				writes++
			}
			if args[4] == failMethod {
				return nil, fmt.Errorf("fixture %s failed", failMethod)
			}
			return nil, nil
		}}
		result, err := client.GrantCollaboratorPermission(context.Background(), "user", "push", false, true)
		wantWrites := 0
		if failMethod == "PUT" {
			wantWrites = 1
		}
		if err == nil || result != nil || writes != wantWrites || !strings.Contains(err.Error(), "fixture "+failMethod) {
			t.Fatal(result, err, writes)
		}
	}
}

func TestRemoveCollaboratorValidationAndSingleAttempt(t *testing.T) {
	for _, login := range []string{"", "a@example.com", "../other", "user/name", "user?x=1", "-user", "user\n"} {
		client := &Client{run: func(context.Context, string, []byte, ...string) ([]byte, error) {
			t.Fatal("invalid login reached gh")
			return nil, nil
		}}
		if err := client.RemoveCollaborator(context.Background(), login); err == nil {
			t.Fatal("invalid identity accepted", login)
		}
	}
	for _, login := range []string{"some-user", "123", "app[bot]"} {
		calls := 0
		client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
			calls++
			joined := strings.Join(args, " ")
			if len(payload) != 0 || !strings.Contains(joined, "--method DELETE repos/owner/repo/collaborators/"+login) || strings.Contains(joined, "--paginate") {
				t.Fatal(args)
			}
			return nil, nil
		}}
		if err := client.RemoveCollaborator(context.Background(), login); err != nil || calls != 1 {
			t.Fatal(login, calls, err)
		}
	}
	for _, cause := range []error{fmt.Errorf("HTTP 403 denied"), fmt.Errorf("connection lost after write"), context.Canceled} {
		calls := 0
		client := &Client{run: func(context.Context, string, []byte, ...string) ([]byte, error) { calls++; return nil, cause }}
		err := client.RemoveCollaborator(context.Background(), "some-user")
		if err == nil || !errors.Is(err, cause) || calls != 1 || !strings.Contains(err.Error(), "inspect repository/access state") {
			t.Fatal(calls, err)
		}
	}
	client := &Client{run: func(context.Context, string, []byte, ...string) ([]byte, error) {
		return []byte(`{"unexpected":true}`), nil
	}}
	if err := client.RemoveCollaborator(context.Background(), "some-user"); err == nil || !strings.Contains(err.Error(), "may have applied") {
		t.Fatal(err)
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
