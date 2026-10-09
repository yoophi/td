package ghstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRepositoryCreationTargetVisibilityAndSingleAttempt(t *testing.T) {
	for _, tc := range []struct {
		failAt   int
		response string
		success  bool
	}{{0, `{"full_name":"new-owner/new-repo","visibility":"private","has_issues":true}`, true}, {0, `{"full_name":"new-owner/new-repo","visibility":"private","has_issues":false}`, true}, {1, "", false}, {2, "", false}, {0, `{"full_name":"other/repo","visibility":"private","has_issues":true}`, false}, {0, `{"full_name":"new-owner/new-repo","visibility":"public","has_issues":true}`, false}, {0, `{"full_name":"new-owner/new-repo","visibility":"private"}`, false}, {0, `null`, false}} {
		calls := 0
		description := "literal $(never-run) `never-run` --source=/no"
		result, err := createRepository(context.Background(), "fixture-dir", "new-owner/new-repo", "private", description, func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
			calls++
			if name != "gh" || dir != "fixture-dir" {
				t.Fatal(dir, name)
			}
			want := []string{"repo", "create", "new-owner/new-repo", "--private", "--description", description}
			if calls == 2 {
				want = []string{"api", "--hostname", "github.com", "repos/new-owner/new-repo"}
			}
			if calls > 2 || !reflect.DeepEqual(args, want) {
				t.Fatal("unexpected mutation or target", args)
			}
			if calls == tc.failAt {
				return nil, fmt.Errorf("fixture failed at %d", calls)
			}
			return []byte(tc.response), nil
		})
		wantCalls := 2
		if tc.failAt == 1 {
			wantCalls = 1
		}
		if calls != wantCalls {
			t.Fatal("creation retried", calls)
		}
		if tc.success {
			if err != nil || result == nil || result.Visibility != "private" {
				t.Fatal(result, err)
			}
		} else if err == nil || result != nil || !strings.Contains(err.Error(), "before retrying") && !strings.Contains(err.Error(), "before any retry") {
			t.Fatal(result, err)
		}
	}
	_, err := createRepository(context.Background(), "fixture", "owner/repo", "public", "", func(context.Context, string, string, ...string) ([]byte, error) { return nil, context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation cause", err)
	}
}

func TestRepositoryCreationRejectsImplicitOrInvalidInputsBeforeGH(t *testing.T) {
	for _, tc := range []struct{ name, visibility string }{{"repo", "public"}, {"-owner/repo", "public"}, {"owner/../repo", "public"}, {"owner/.", "public"}, {"owner/..", "public"}, {"owner/repo?x=1", "public"}, {"owner/repo", ""}, {"owner/repo", "unlisted"}} {
		if result, err := createRepository(context.Background(), "fixture", tc.name, tc.visibility, "", func(context.Context, string, string, ...string) ([]byte, error) {
			t.Fatal("invalid input reached gh")
			return nil, nil
		}); err == nil || result != nil {
			t.Fatal(tc, result, err)
		}
	}
}
