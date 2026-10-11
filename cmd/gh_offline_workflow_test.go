package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func TestWorkflowDocumentedReviewPoliciesMatchEligibility(t *testing.T) {
	t.Setenv("TD_FEATURE_REVIEW_POLICY_MODE", "")
	t.Setenv("TD_FEATURE_BALANCED_REVIEW_POLICY", "")
	mode, err := features.ResolveReviewPolicyMode("")
	if err != nil || workflowReviewPolicyJSON()["default_mode"] != mode {
		t.Fatal("documented default differs from resolver", mode, err)
	}
	for _, doc := range workflowReviewModeDocs {
		parsed, err := reviewpolicy.ParseMode(string(doc.Name))
		if err != nil {
			t.Fatal(err)
		}
		in := reviewpolicy.ReviewerEligibilityInput{
			Mode: parsed, Issue: &models.Issue{ID: "gh-1", ImplementerSession: "implementer"},
			SessionID: "implementer", SessionIsImplementer: true,
			HasImplementationHistory: true, WasAnyInvolved: true,
		}
		if reviewpolicy.EvaluateReviewerEligibility(in).Allowed {
			t.Fatal("unacknowledged implementer approval allowed", parsed)
		}
		in.SelfReviewAcknowledged = true
		if got := reviewpolicy.EvaluateReviewerEligibility(in).Allowed; got != (parsed == reviewpolicy.ModeTrusted) {
			t.Fatal("documented self-review boundary differs", parsed, got)
		}
		in.SelfReviewAcknowledged = false
		in.AttributedTo = "actual reviewer"
		if got := reviewpolicy.EvaluateReviewerEligibility(in).Allowed; got != (parsed == reviewpolicy.ModeTrusted) {
			t.Fatal("attribution must not bypass independence outside trusted", parsed, got)
		}
		in.Issue.Minor = true
		if !reviewpolicy.EvaluateReviewerEligibility(in).Allowed {
			t.Fatal("documented minor exemption differs", parsed)
		}
	}
}

func TestGitHubOfflineWorkflowCLIContracts(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	contextRoot := t.TempDir()
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_GH_CONTEXT_ROOT", contextRoot)
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	t.Setenv("TD_ANALYTICS", "true")
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	if err := os.WriteFile(ghPath, []byte("#!/bin/sh\nprintf 'unexpected gh call\\n' >> \"$TD_SYNC_STATUS_CALLS\"\nexit 9\n"), 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		for _, tc := range []struct {
			args []string
			want string
			json bool
			fail bool
		}{
			{nil, "COMMAND REVIEW POLICY", false, false},
			{[]string{"--json"}, `"project_policy_resolved":false`, true, false},
			{[]string{"--mermaid"}, "%% " + workflowReviewPolicyNote, false, false},
			{[]string{"--mermaid", "--json"}, `"review_policy":`, true, false},
			{[]string{"--dot", "--json"}, `"format":"dot"`, true, false},
			{[]string{"--dot"}, "digraph", false, false},
			{[]string{"--refresh", "--json"}, `"project_policy_resolved":false`, true, false},
			{[]string{"--mermaid", "--dot", "--json"}, `"error":`, true, true},
			{[]string{"unused", "--json"}, `"error":`, true, true},
			{[]string{"--help"}, "review_policy_mode", false, false},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			args := append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "workflow"}, tc.args...)
			child := exec.CommandContext(ctx, executable, args...)
			child.Dir = dir
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			err := child.Run()
			output := stdout.Bytes()
			cancel()
			if tc.json && json.Valid(output) {
				var compact bytes.Buffer
				if err := json.Compact(&compact, output); err != nil {
					t.Fatal(err)
				}
				output = compact.Bytes()
			}
			if (err != nil) != tc.fail || !strings.Contains(string(output), tc.want) || (tc.json && !json.Valid(output)) {
				t.Fatalf("%v: %v %s stderr=%s", tc.args, err, output, stderr.String())
			}
		}
	}
	if data, _ := os.ReadFile(calls); len(data) != 0 {
		t.Fatal("offline workflow called gh", string(data))
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("offline workflow created SQLite", err)
	}
	entries, err := os.ReadDir(contextRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("offline workflow created issue session context", entries, err)
	}
}
