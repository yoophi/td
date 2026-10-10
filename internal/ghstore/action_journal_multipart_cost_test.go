package ghstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type journalCostRequest struct {
	method, path string
	manifest     bool
}

func recordMultipartRequests(f *multipartWriteFixture) *[]journalCostRequest {
	calls := []journalCostRequest{}
	run := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		var input struct{ Body string }
		_ = json.Unmarshal(payload, &input)
		calls = append(calls, journalCostRequest{args[4], strings.TrimPrefix(args[5], "repos/owner/repo"), strings.Contains(input.Body, actionMultipartJournalStart)})
		return run(ctx, dir, payload, args...)
	}
	return &calls
}

// Drive the real mutation/undo entry points, including target fences. A native
// manifest publication must receive one complete collection readback. Reading
// that collection again before any new target observation wastes every page.
func TestMultipartMutationReadbackBudget(t *testing.T) {
	f := newMultipartWriteFixture(t, "")
	calls := recordMultipartRequests(f)
	description := multipartText(68)
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &description}); err != nil {
		t.Fatal(err)
	}
	updateCalls := len(*calls)
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	for name, requests := range map[string][]journalCostRequest{"update": (*calls)[:updateCalls], "undo": (*calls)[updateCalls:]} {
		t.Run(name, func(t *testing.T) {
			publications, collections, points := 0, 0, 0
			for i, request := range requests {
				if request.method == "GET" && request.path == "/issues/9/comments?per_page=100" {
					collections++
				}
				if request.method == "GET" && strings.HasPrefix(request.path, "/issues/comments/") {
					points++
				}
				if !request.manifest || request.method == "GET" {
					continue
				}
				publications++
				readbacks := 0
				for _, following := range requests[i+1:] {
					if following.method != "GET" || (following.path != "/issues/9/comments?per_page=100" && !strings.HasPrefix(following.path, "/issues/comments/")) {
						break
					}
					readbacks++
				}
				if readbacks != 1 {
					t.Errorf("publication %d repeated native receipt reads before the next target observation: got %d requests, want one complete collection", publications, readbacks)
				}
			}
			if publications < 2 {
				t.Fatal("fixture did not exercise both multipart mutation phases")
			}
			budget := map[string][3]int{"update": {28, 7, 1}, "undo": {31, 9, 3}}[name]
			if len(requests) > budget[0] || collections > budget[1] || points > budget[2] {
				t.Errorf("mutation read budget exceeded: API=%d collections=%d point comments=%d, budget=%v", len(requests), collections, points, budget)
			}
			t.Logf("API invocations=%d carrier collections=%d point comments=%d multipart publications=%d", len(requests), collections, points, publications)
		})
	}
}

func TestMultipartPublicationReadbackRejectsConcurrentChanges(t *testing.T) {
	for _, phase := range []string{"intent", "completion"} {
		for _, change := range []string{"root-clock", "root-author", "missing-part", "edited-part"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				f := newMultipartWriteFixture(t, "")
				run := f.client.run
				injected := false
				f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
					response, err := run(ctx, dir, payload, args...)
					method, path := args[4], strings.TrimPrefix(args[5], "repos/owner/repo")
					wanted := phase == "intent" && method == "POST" && path == "/issues/9/comments" || phase == "completion" && method == "PATCH" && path == "/issues/comments/101"
					if err != nil || !wanted || injected || f.comment == nil || !strings.Contains(f.comment.Body, actionMultipartJournalStart) {
						return response, err
					}
					injected = true
					var manifest actionJournalManifest
					block := strings.SplitN(f.comment.Body, actionMultipartJournalStart, 2)[1]
					if err := json.Unmarshal([]byte(strings.TrimSuffix(block, markerEnd)), &manifest); err != nil {
						t.Fatal(err)
					}
					switch change {
					case "root-clock":
						f.comment.UpdatedAt = f.comment.UpdatedAt.Add(time.Second)
					case "root-author":
						f.comment.User.Login = "other-author"
					case "missing-part":
						delete(f.parts, manifest.Parts[0].ID)
					case "edited-part":
						part := f.parts[manifest.Parts[0].ID]
						part.UpdatedAt = part.UpdatedAt.Add(time.Second)
						f.parts[part.ID] = part
					}
					return response, err // the valid write response predates the change
				}
				description := multipartText(68)
				_, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &description})
				writes := 0
				if phase == "completion" {
					writes = 1
				}
				if !injected || err == nil || f.targetWrites != writes {
					t.Fatalf("unverified publication accepted or target replayed: injected=%t writes=%d err=%v", injected, f.targetWrites, err)
				}
			})
		}
	}

}

func TestMultipartPublicationObservationDoesNotCrossMutationBoundary(t *testing.T) {
	f := newMultipartWriteFixture(t, "")
	description := multipartText(68)
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &description}); err != nil {
		t.Fatal(err)
	}
	// The receipt was verified by publication, and a display snapshot exists.
	// Neither may authorize the later inverse after an immutable part changes.
	if _, err := f.client.ReadSnapshot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	for id, part := range f.parts {
		part.UpdatedAt = part.UpdatedAt.Add(time.Second)
		f.parts[id] = part
	}
	parts, receipts := f.partWrites, f.receiptWrites
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil || f.targetWrites != 1 || parts != f.partWrites || receipts != f.receiptWrites {
		t.Fatal("old publication observation authorized a later mutation", err)
	}
}
