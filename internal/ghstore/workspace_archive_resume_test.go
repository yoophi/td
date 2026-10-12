package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type archiveResumeRecorderFixture struct{ mutationRecorderFixture }

func (*archiveResumeRecorderFixture) ReplayGuardEnabled() bool { return true }

func (r *archiveResumeRecorderFixture) BeginMutation(request MutationRequest) (int, error) {
	for _, previous := range r.requests {
		if previous == request {
			return 0, fmt.Errorf("previously attempted request cannot replay")
		}
	}
	return r.mutationRecorderFixture.BeginMutation(request)
}

func TestWorkspaceArchiveExplicitResumeObservesLostResponsesWithoutDuplicatePosts(t *testing.T) {
	for _, stage := range []string{"part_lost", "root_lost", "part_not_observed", "root_deleted"} {
		t.Run(stage, func(t *testing.T) {
			mode := ""
			if stage == "part_lost" || stage == "part_not_observed" {
				mode = "part-uncertain"
			}
			f := newMultipartWriteFixture(t, mode)
			if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
				t.Fatal(err)
			}
			plan, _, _, data := workspaceArchiveFixture(t)
			base, rootWrites := f.client.run, 0
			lost := errors.New("fixture connection lost after archive root accepted")
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if args[4] == "POST" && strings.HasSuffix(args[5], "/issues/9/comments") {
					var input struct{ Body string }
					if err := json.Unmarshal(payload, &input); err != nil {
						return nil, err
					}
					if strings.Contains(input.Body, workspaceArchivePrefix) {
						rootWrites++
						clock := f.issues[9].UpdatedAt.Add(time.Minute)
						root := apiComment{ID: 1000, Body: input.Body, IssueURL: "https://api.github.com/repos/owner/repo/issues/9", CreatedAt: clock, UpdatedAt: clock}
						root.User.Login = "github-author"
						f.comment = &root
						if stage == "root_lost" && rootWrites == 1 {
							return nil, lost
						}
						return json.Marshal(root)
					}
				}
				return base(ctx, dir, payload, args...)
			}
			recorder := &archiveResumeRecorderFixture{}
			ctx := WithMutationRecorder(context.Background(), recorder)
			_, err := f.client.PublishWorkspaceArchive(ctx, plan)
			if stage != "root_deleted" && err == nil {
				t.Fatal("fixture did not lose original response")
			}
			if stage == "root_lost" && !errors.Is(err, lost) {
				t.Fatal("original transport failure hidden", err)
			}
			f.mode = ""
			if stage == "part_not_observed" {
				f.parts = map[int64]apiComment{} // Accepted part is not in this observation.
			}
			if stage == "root_deleted" {
				f.comment = nil
			}
			beforeParts, beforeRoots := f.partWrites, rootWrites
			var result *WorkspaceArchiveEvidence
			if stage == "root_lost" {
				result, err = f.client.ResumeWorkspaceArchiveData(ctx, plan.manifest.OperationID, data, plan.manifest.Mappings, 0)
			} else {
				result, err = f.client.ResumeWorkspaceArchive(ctx, plan)
			}
			if stage == "part_not_observed" || stage == "root_deleted" {
				if err == nil || result != nil || !strings.Contains(err.Error(), "replay") || f.partWrites != beforeParts || rootWrites != beforeRoots {
					t.Fatal("absence authorized repeating accepted POST", result, err, f.partWrites, rootWrites)
				}
				return
			}
			if err != nil || result == nil || string(result.SourceEnvelope) != string(data) || f.partWrites != plan.PartCount() || rootWrites != 1 || f.targetWrites != 0 {
				t.Fatal("explicit observation duplicated or changed native data", result, err, f.partWrites, rootWrites)
			}
			parts, roots := f.partWrites, rootWrites
			if result, err := f.client.ResumeWorkspaceArchive(ctx, plan); err != nil || result == nil || f.partWrites != parts || rootWrites != roots {
				t.Fatal("completed resume wrote again", result, err)
			}
		})
	}
}

func TestWorkspaceArchiveResumeRequiresReplayGuardBeforeAnyAPI(t *testing.T) {
	var client *Client
	for _, ctx := range []context.Context{context.Background(), WithMutationRecorder(context.Background(), &mutationRecorderFixture{})} {
		if _, err := client.ResumeWorkspaceArchive(ctx, nil); err == nil {
			t.Fatal("unguarded resume reached client")
		}
	}
}
