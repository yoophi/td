package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceArchivePublicationVerifiesPartsRootAndStopsUncertainWrites(t *testing.T) {
	for _, mode := range []string{"success", "part-denied", "part-uncertain", "part-other-author", "part-readback-edited", "root-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f := newMultipartWriteFixture(t, mode)
			if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
				t.Fatal(err)
			}
			plan, _, _, data := workspaceArchiveFixture(t)
			rootWrites := 0
			run := f.client.run
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
						if mode == "root-uncertain" {
							return nil, errors.New("connection lost after root POST")
						}
						return json.Marshal(root)
					}
				}
				return run(ctx, dir, payload, args...)
			}
			result, err := f.client.PublishWorkspaceArchive(context.Background(), plan)
			if mode == "success" {
				if err != nil || result == nil || string(result.SourceEnvelope) != string(data) || rootWrites != 1 || f.partWrites != plan.PartCount() {
					t.Fatal("unverified publication", err)
				}
				writes := f.partWrites
				if result, err := f.client.PublishWorkspaceArchive(context.Background(), plan); err == nil || result != nil || rootWrites != 1 || f.partWrites != writes {
					t.Fatal("normal retry replayed published archive")
				}
			} else {
				if err == nil || result != nil {
					t.Fatal("failed publication reported success")
				}
				if mode == "part-denied" && !strings.Contains(err.Error(), "HTTP 403 part denied") {
					t.Fatal("lost original API diagnostic", err)
				}
				if mode == "part-uncertain" && (f.partWrites != 1 || rootWrites != 0 || !strings.Contains(err.Error(), "connection lost")) {
					t.Fatal("uncertain part was retried or root published", err)
				}
				if mode == "root-uncertain" && (rootWrites != 1 || !strings.Contains(err.Error(), "connection lost")) {
					t.Fatal("uncertain root was retried", err)
				}
				if mode == "part-uncertain" || mode == "root-uncertain" {
					writes, roots := f.partWrites, rootWrites
					if result, err := f.client.PublishWorkspaceArchive(context.Background(), plan); err == nil || result != nil || f.partWrites != writes || rootWrites != roots {
						t.Fatal("explicit normal retry replayed uncertain publication", err)
					}
				}
			}
			if f.targetWrites != 0 {
				t.Fatal("passive archive mutated an entity")
			}
		})
	}
}
