package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func importArchiveFixture(t *testing.T, historyBytes int) ActionJournal {
	t.Helper()
	a, _, _ := importCommandFixture(t)
	if historyBytes > 0 {
		items, err := a.ImportCommand.inputItems()
		if err != nil {
			t.Fatal(err)
		}
		items[1].Logs = []models.Log{{ID: "source-log", IssueID: items[1].Issue.ID, Type: models.LogTypeProgress, Message: strings.Repeat("h", historyBytes)}}
		data, err := json.Marshal(items)
		if err != nil {
			t.Fatal(err)
		}
		a.ImportCommand.Input = string(data)
		a.ImportCommand.Members[1].Digest, err = items[1].Digest()
		if err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func importArchiveNativeFixture(t *testing.T, a ActionJournal) (apiComment, []apiComment, *journalPublication) {
	t.Helper()
	plan, err := planActionJournalPublication(a)
	if err != nil || plan.importArchive == nil {
		t.Fatal("archive preparation", err)
	}
	root := journalCommentFixture(t, a)
	root.ID, root.IssueURL = 1000000, "https://api.github.com/repos/owner/repo/issues/999"
	root.UpdatedAt = root.CreatedAt.Add(time.Minute)
	parts := []apiComment{}
	add := func(bodies []string) []apiComment {
		added := []apiComment{}
		for _, body := range bodies {
			part := root
			part.ID, part.Body, part.UpdatedAt = int64(len(parts)+100), body, part.CreatedAt
			parts, added = append(parts, part), append(added, part)
		}
		return added
	}
	native := add(plan.importArchive.bodies)
	rootPlan, err := plan.importArchive.bindParts(native)
	if err != nil {
		t.Fatal(err)
	}
	root.Body, err = rootPlan.bindParts(add(rootPlan.partBodies))
	if err != nil {
		t.Fatal(err)
	}
	return root, parts, rootPlan
}

func TestImportArchiveLargeHistoryAndMultipartRoot(t *testing.T) {
	for _, tc := range []struct {
		size      int
		largeRoot bool
	}{{0, false}, {actionDecodedBudget + 1024, false}, {actionDecodedBudget + 1024, true}} {
		a := importArchiveFixture(t, tc.size)
		if tc.largeRoot {
			for n := 10000; n < 14000; n++ {
				a.ImportCommand.OriginalTargets = append(a.ImportCommand.OriginalTargets, ImportTargetIdentity{IssueID: fmt.Sprintf("gh-%d", n), OperationID: "td-op-" + journalTransportHash(fmt.Sprint(n))})
			}
			slices.SortFunc(a.ImportCommand.OriginalTargets, func(a, b ImportTargetIdentity) int { return strings.Compare(a.IssueID, b.IssueID) })
		}
		root, parts, rootPlan := importArchiveNativeFixture(t, a)
		if tc.size > actionDecodedBudget {
			if _, err := renderWriteActionJournal(a); err == nil {
				t.Fatal("fixture did not exceed the old inline archive capacity")
			}
		}
		if tc.largeRoot && len(rootPlan.partBodies) == 0 {
			t.Fatal("large reference inventory did not exercise a multipart root")
		}
		if len(root.Body) > actionCommentBudget || strings.Contains(root.Body, "Existing native activity") {
			t.Fatal("root embedded native history or exceeded native comment capacity")
		}
		decoded, err := decodeMultipartActionJournal(root, a.CarrierID, parts)
		if err != nil || !reflect.DeepEqual(decoded.ActionJournal, a) {
			t.Fatal("archive lost original input/native evidence", err)
		}
		body, err := renderExistingActionJournal(decoded)
		if err != nil || body != root.Body {
			t.Fatal("original reference manifest was not retained", err)
		}
		if _, err := decodeActionJournal(root, a.CarrierID); err == nil {
			t.Fatal("archive accepted without observing referenced native parts")
		}
		// Shared aggregate readers consume one already collected set. Transport
		// parts stay invisible to activity; no per-document/part read is needed.
		collection, err := collectJournalParts(parts)
		if err != nil {
			t.Fatal(err)
		}
		activity, err := decodeActivityWithJournalParts(root, a.CarrierID, collection)
		if err != nil || activity.OperationID != a.OperationID {
			t.Fatal("shared activity could not read import archive", err)
		}
		for _, part := range parts {
			activity, err := decodeActivityWithJournalParts(part, a.CarrierID, collection)
			if err != nil || activity.Kind != "action_journal_part" {
				t.Fatal("archive chunk became user activity", err)
			}
		}
	}
}

func TestImportArchiveRewriteReusesOriginalDocumentsAndRejectsReplacement(t *testing.T) {
	f := newMultipartWriteFixture(t, "")
	a := importArchiveFixture(t, 100000)
	a.CarrierID, a.SessionID = "gh-9", "ses_actor"
	plan, err := planActionJournalPublication(a)
	if err != nil {
		t.Fatal(err)
	}
	root, err := f.client.publishJournal(context.Background(), "/issues/9/comments", plan, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	parts, body := f.partWrites, f.comment.Body
	rewritten, err := f.client.publishJournal(context.Background(), "/issues/comments/101", plan, root, nil)
	if err != nil || f.partWrites != parts || f.comment.Body != body || !reflect.DeepEqual(rewritten.ActionJournal, a) {
		t.Fatal("root rewrite duplicated or changed its immutable documents", err)
	}
	for _, field := range []string{"input", "session", "before"} {
		changed, err := copyCreationJournal(a)
		if err != nil {
			t.Fatal(err)
		}
		switch field {
		case "input":
			changed = importArchiveFixture(t, 100001)
			changed.CarrierID, changed.SessionID = a.CarrierID, a.SessionID
		case "session":
			changed.SessionID = "ses_other"
		case "before":
			changed.ImportCommand.Members[0].Before.Comments[0].Body = "Replacement original comment"
		}
		changedPlan, err := planActionJournalPublication(changed)
		if err != nil {
			t.Fatal(err)
		}
		writes := f.partWrites + f.receiptWrites
		if _, err := f.client.publishJournal(context.Background(), "/issues/comments/101", changedPlan, rewritten, nil); err == nil || writes != f.partWrites+f.receiptWrites || f.comment.Body != body {
			t.Fatal("root rewrite replaced original evidence", field, err)
		}
	}
}

func TestImportArchiveRejectsMissingChangedForeignEvidence(t *testing.T) {
	a := importArchiveFixture(t, 80000)
	root, parts, _ := importArchiveNativeFixture(t, a)
	for name, change := range map[string]func(*apiComment, *[]apiComment){
		"missing chunk": func(_ *apiComment, parts *[]apiComment) { *parts = (*parts)[1:] },
		"edited body":   func(_ *apiComment, parts *[]apiComment) { (*parts)[0].Body += "\n" },
		"edited clock": func(_ *apiComment, parts *[]apiComment) {
			(*parts)[0].UpdatedAt = (*parts)[0].UpdatedAt.Add(time.Second)
		},
		"foreign author":  func(_ *apiComment, parts *[]apiComment) { (*parts)[0].User.Login = "someone-else" },
		"foreign carrier": func(_ *apiComment, parts *[]apiComment) { (*parts)[0].IssueURL += "1" },
		"foreign repository": func(_ *apiComment, parts *[]apiComment) {
			(*parts)[0].IssueURL = "https://api.github.com/repos/other/repo/issues/999"
		},
		"transplanted command": func(root *apiComment, parts *[]apiComment) {
			root.IssueURL = "https://api.github.com/repos/other/repo/issues/999"
			for n := range *parts {
				(*parts)[n].IssueURL = root.IssueURL
			}
		},
		"duplicate chunk": func(_ *apiComment, parts *[]apiComment) {
			other := (*parts)[0]
			other.ID += 10000
			*parts = append(*parts, other)
		},
		"part after root": func(root *apiComment, _ *[]apiComment) {
			root.CreatedAt, root.UpdatedAt = root.CreatedAt.Add(-time.Hour), root.CreatedAt.Add(-time.Hour)
		},
		"changed force": func(root *apiComment, _ *[]apiComment) {
			root.Body = strings.Replace(root.Body, `"force":true`, `"force":false`, 1)
		},
		"changed decision": func(root *apiComment, _ *[]apiComment) {
			root.Body = strings.Replace(root.Body, `"action":"overwrite"`, `"action":"skip"`, 1)
		},
		"guessed native id": func(root *apiComment, _ *[]apiComment) {
			root.Body = strings.Replace(root.Body, `"id":100,`, `"id":9999,`, 1)
		},
		"cross command": func(root *apiComment, _ *[]apiComment) {
			root.Body = strings.ReplaceAll(root.Body, a.OperationID, "different-command")
		},
		"root author":  func(root *apiComment, _ *[]apiComment) { root.User.Login = "other-author" },
		"root summary": func(root *apiComment, _ *[]apiComment) { root.Body = "changed\n" + root.Body },
	} {
		t.Run(name, func(t *testing.T) {
			r, p := root, append([]apiComment{}, parts...)
			change(&r, &p)
			if _, err := decodeMultipartActionJournal(r, a.CarrierID, p); err == nil {
				t.Fatal("invalid archive evidence accepted")
			}
		})
	}
}

func TestImportArchiveIncrementalRootUsesUnchangedBaselineParts(t *testing.T) {
	a := importArchiveFixture(t, 80000)
	root, parts, _ := importArchiveNativeFixture(t, a)
	c := &Client{repo: a.ImportCommand.Repository}
	body, err := encodeBody("Journal", metadata{EntityKind: "journal", Type: models.TypeTask, Priority: models.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	carrier := apiIssue{Number: 999, Title: "Journal", State: "open", Body: body, CreatedAt: root.CreatedAt, UpdatedAt: root.UpdatedAt}
	baseline, err := c.assembleSnapshot([][]apiIssue{{carrier}}, [][]apiComment{append(parts, root)}, true)
	if err != nil {
		t.Fatal(err)
	}
	baseline.observedAt, baseline.fullReconciledAt = time.Now().Add(-time.Minute), time.Now().Add(-time.Minute)
	changed := root
	changed.UpdatedAt = time.Now().UTC()
	reads := 0
	c.run = func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		if args[4] != "GET" || payload != nil || !strings.Contains(args[5], "since=") {
			t.Fatal("unexpected per-part read or write", args)
		}
		reads++
		if strings.Contains(args[5], "/issues/comments?") {
			return json.Marshal([][]apiComment{{changed}})
		}
		return json.Marshal([][]apiIssue{{}})
	}
	merged, err := c.readSnapshotIncremental(context.Background(), baseline)
	if err != nil {
		t.Fatal("incremental root could not resolve original archive parts", err)
	}
	history, err := merged.ActionHistory(a.SessionID)
	if err != nil || len(history) != 1 || !reflect.DeepEqual(history[0].ActionJournal, a) || !history[0].UpdatedAt.Equal(changed.UpdatedAt) || reads != 2 {
		t.Fatal("incremental observation lost original evidence or added N+1 reads", err, reads)
	}
	// Failed delta validation must not mutate the original cached observation.
	changed = parts[0]
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
	if _, err := c.readSnapshotIncremental(context.Background(), baseline); err == nil {
		t.Fatal("edited archive part was accepted by incremental reader")
	}
	history, err = baseline.ActionHistory(a.SessionID)
	if err != nil || len(history) != 1 || !history[0].UpdatedAt.Equal(root.UpdatedAt) {
		t.Fatal("failed delta damaged baseline", err)
	}
}

func TestImportArchivePublicationBulkReadbackAndExplicitPartialRecovery(t *testing.T) {
	for _, mode := range []string{"", "part-uncertain", "part-denied", "part-other-author", "part-readback-edited", "manifest-denied", "fence"} {
		t.Run(mode, func(t *testing.T) {
			f := newMultipartWriteFixture(t, mode)
			a := importArchiveFixture(t, 100000)
			a.CarrierID, a.SessionID = "gh-9", "ses_actor"
			plan, err := planActionJournalPublication(a)
			if err != nil {
				t.Fatal(err)
			}
			base, collections := f.client.run, 0
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if strings.Contains(args[5], "/issues/9/comments?") {
					collections++
				}
				return base(ctx, dir, payload, args...)
			}
			fenceErr := errors.New("original import targets changed during archive upload")
			fence := func() error {
				if mode == "fence" {
					return fenceErr
				}
				return nil
			}
			root, err := f.client.publishJournal(context.Background(), "/issues/9/comments", plan, nil, fence)
			if f.targetWrites != 0 || f.creationWrites != 0 {
				t.Fatal("archive publication wrote an import destination")
			}
			if mode != "" {
				if err == nil || root != nil || f.intentWrites != 0 {
					t.Fatal("unverified documents published a command root", err)
				}
				if mode == "fence" && !errors.Is(err, fenceErr) || mode == "part-denied" && !strings.Contains(err.Error(), "HTTP 403 part denied (original diagnostic)") {
					t.Fatal("original failure was hidden", err)
				}
				if mode != "part-uncertain" {
					return
				}
				if f.partWrites != 1 || len(f.parts) != 1 {
					t.Fatal("uncertain upload retried automatically")
				}
				f.mode, collections = "", 0
				root, err = f.client.publishJournal(context.Background(), "/issues/9/comments", plan, nil, nil)
			}
			if err != nil || !reflect.DeepEqual(root.ActionJournal, a) || f.partWrites != len(plan.importArchive.bodies) || collections != 3 || f.commentReads != 0 {
				t.Fatal("publication lost evidence, repeated chunks or made per-part reads", err, f.partWrites, collections, f.commentReads)
			}
			writes := f.partWrites + f.intentWrites
			if _, err := f.client.publishJournal(context.Background(), "/issues/9/comments", plan, nil, nil); err == nil || writes != f.partWrites+f.intentWrites {
				t.Fatal("existing command was published a second time", err)
			}
		})
	}
}
