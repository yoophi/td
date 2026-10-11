package ghstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

type portableRestoreFixture struct {
	client   *Client
	issues   []apiIssue
	comments []apiComment
	calls    []string
	mode     string
	failed   bool
	posts    int
}

func newPortableRestoreFixture(t *testing.T) *portableRestoreFixture {
	t.Helper()
	body, err := encodeBody("Journal", metadata{EntityKind: "journal", Type: models.TypeTask, Priority: models.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	f := &portableRestoreFixture{issues: []apiIssue{{Number: 1, Title: "Numeric collision", State: "open"}, {Number: 2, Title: "Mapped destination", Body: "Keep original destination content", State: "open"}, {Number: 9, Title: "Journal", Body: body, State: "open"}}, comments: []apiComment{}}
	f.client = &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		method, path := args[4], strings.TrimPrefix(args[5], "repos/owner/repo")
		f.calls = append(f.calls, method+" "+path)
		switch {
		case method == "GET" && path == "user":
			return json.Marshal(map[string]string{"login": "destination-writer"})
		case method == "GET" && path == "/issues?state=all&per_page=100":
			return json.Marshal([][]apiIssue{f.issues})
		case method == "GET" && path == "/issues/comments?per_page=100":
			return json.Marshal([][]apiComment{f.comments})
		case method == "GET" && path == "/issues/9":
			return json.Marshal(f.issues[2])
		case method == "GET" && strings.HasPrefix(path, "/issues/comments/"):
			var id int64
			_, _ = fmt.Sscanf(path, "/issues/comments/%d", &id)
			for _, comment := range f.comments {
				if comment.ID == id {
					return json.Marshal(comment)
				}
			}
			return nil, fmt.Errorf("missing comment %d", id)
		case method == "POST" && path == "/issues/9/comments":
			f.posts++
			var input struct{ Body string }
			if err := json.Unmarshal(payload, &input); err != nil {
				return nil, err
			}
			kind := "part"
			if strings.Contains(input.Body, journalArchivePrefix) {
				kind = "manifest"
			}
			fail := !f.failed && strings.HasPrefix(f.mode, kind+"-")
			if fail && f.mode == kind+"-denied" {
				f.failed = true
				return nil, errors.New("HTTP 403 archive permission denied")
			}
			id := int64(500 + len(f.comments))
			now := time.Date(2026, 10, 11, 1, 0, len(f.comments), 0, time.UTC)
			comment := apiComment{ID: id, IssueURL: "https://api.github.com/repos/owner/repo/issues/9", URL: fmt.Sprintf("https://github.com/owner/repo/issues/9#issuecomment-%d", id), Body: input.Body, CreatedAt: now, UpdatedAt: now}
			comment.User.Login = "destination-writer"
			if fail && f.mode == kind+"-author" {
				comment.User.Login = "different-writer"
			}
			f.comments = append(f.comments, comment)
			if fail {
				f.failed = true
				if f.mode == kind+"-lost" {
					return nil, errors.New("connection lost after accepted POST")
				}
			}
			return json.Marshal(comment)
		default:
			return nil, fmt.Errorf("unexpected request or target mutation: %s %s", method, path)
		}
	}}
	if err := f.client.TrackActions("ses_destination"); err != nil {
		t.Fatal(err)
	}
	return f
}

func portableForeignBundle(t *testing.T, large bool) *PortableJournalBundle {
	t.Helper()
	bundle := portableJournalFixture(t)
	source := &bundle.Sources[0]
	source.Repository = "source/project"
	source.Actions[0].URL = strings.ReplaceAll(source.Actions[0].URL, "owner/repo", source.Repository)
	if large {
		random := rand.New(rand.NewSource(66))
		body := make([]byte, 200_000)
		for i := range body {
			body[i] = byte(' ' + random.Intn(90))
		}
		source.Actions[0].Before.Body = string(body) + "\n<!-- td:action:v1\nopaque original body"
		source.Actions[0].Before.Revision, _ = source.Actions[0].Before.checksum()
	}
	source.SHA256, _ = source.checksum()
	bundle.SHA256, _ = bundle.checksum()
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestPortableJournalRestoreRoundTripMappingsNoExecutionAndBulkReads(t *testing.T) {
	f := newPortableRestoreFixture(t)
	original := portableForeignBundle(t, true)
	bundle, err := BindJournal(original, "owner/repo", JournalMapping{"github:source/project:gh-1": "gh-2"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(f.issues)
	plan, err := f.client.PlanJournalRestore(context.Background(), bundle)
	if err != nil || !plan.DryRun || plan.Parts < 2 || plan.Actions != 1 || len(plan.Mappings) != 1 || plan.Mappings[0].TargetID != "gh-2" || f.posts != 0 || len(f.calls) != 2 {
		t.Fatal(plan, f.calls, err)
	}
	result, err := f.client.RestoreJournal(context.Background(), bundle)
	if err != nil || result.AlreadyRestored || result.WrittenParts != plan.Parts || result.ArchiveID == "" {
		t.Fatal(result, err)
	}
	after, _ := json.Marshal(f.issues)
	if string(before) != string(after) || len(f.comments) != plan.Parts+1 {
		t.Fatal("restore modified tasks or duplicated parts")
	}
	for _, comment := range f.comments {
		if strings.Contains(comment.Body, actionJournalPrefix) || comment.User.Login != "destination-writer" {
			t.Fatal("source journal promoted to active native receipt")
		}
	}
	f.calls = nil
	exported, err := f.client.ExportJournal(context.Background())
	if err != nil || len(f.calls) != 2 || len(exported.Sources) != 2 || len(exported.Bindings) != 1 {
		t.Fatal("export omitted archives or made per-part reads", err, f.calls)
	}
	found := false
	for _, source := range exported.Sources {
		if source.SHA256 == original.Sources[0].SHA256 {
			found = reflect.DeepEqual(source, original.Sources[0])
		}
	}
	if !found {
		t.Fatal("original attribution/snapshots/IDs changed")
	}
	snapshot, err := f.client.ReadSnapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	history, err := snapshot.ActionHistory("")
	if err != nil || len(history) != 0 {
		t.Fatal("archive became active history", err)
	}
	posts := f.posts
	if _, err := f.client.UndoAction(context.Background(), result.ArchiveID, "ses_destination"); err == nil || !strings.Contains(err.Error(), "archival data") {
		t.Fatal("archive permitted undo", err)
	}
	if _, err := f.client.ReconcileAction(context.Background(), result.ArchiveID, "ses_destination", true); err == nil || !strings.Contains(err.Error(), "archival data") {
		t.Fatal("archive permitted replay", err)
	}
	f.calls = nil
	repeated, err := f.client.RestoreJournal(context.Background(), bundle)
	if err != nil || !repeated.AlreadyRestored || repeated.ArchiveID != result.ArchiveID || f.posts != posts || len(f.calls) != 2 {
		t.Fatal("repeat was not read-only", repeated, err, f.calls)
	}
	// A second hop keeps original sources flat, preserving both destination maps.
	third, err := BindJournal(exported, "third/repo", JournalMapping{"github:source/project:gh-1": "gh-77"})
	if err != nil || len(third.Sources) != 2 || len(third.Bindings) != 2 {
		t.Fatal("re-export cannot be imported again", err)
	}
}

func TestPortableJournalRestoreUncertainWritesStopAndExplicitRetryObserves(t *testing.T) {
	for _, mode := range []string{"part-lost", "manifest-lost", "part-denied", "manifest-denied"} {
		t.Run(mode, func(t *testing.T) {
			f := newPortableRestoreFixture(t)
			f.mode = mode
			bundle := portableForeignBundle(t, false)
			_, err := f.client.RestoreJournal(context.Background(), bundle)
			wantPosts := 1
			if strings.HasPrefix(mode, "manifest") {
				wantPosts = 2
			}
			if err == nil || !strings.Contains(err.Error(), "no automatic retry") || f.posts != wantPosts || (!strings.Contains(err.Error(), "connection lost") && !strings.Contains(err.Error(), "HTTP 403")) {
				t.Fatal("uncertain write replayed or original error hidden", err, f.posts)
			}
			beforePosts, beforeComments := f.posts, len(f.comments)
			f.mode = ""
			result, err := f.client.RestoreJournal(context.Background(), bundle)
			if err != nil || len(f.comments) != 2 || result.ArchiveID == "" {
				t.Fatal(result, err)
			}
			if f.posts-beforePosts != 2-beforeComments {
				t.Fatal("retry repeated an observed accepted POST", mode, f.posts, beforePosts)
			}
			if mode == "manifest-lost" && !result.AlreadyRestored {
				t.Fatal("lost manifest response not recognized read-only")
			}
		})
	}
}

func TestPortableJournalArchiveCorruptionAndForeignResumeAreRejected(t *testing.T) {
	for _, mode := range []string{"edited", "deleted", "duplicate", "foreign-author", "hash", "version", "size", "reference"} {
		t.Run(mode, func(t *testing.T) {
			f := newPortableRestoreFixture(t)
			if _, err := f.client.RestoreJournal(context.Background(), portableForeignBundle(t, false)); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "edited":
				f.comments[0].UpdatedAt = f.comments[0].UpdatedAt.Add(time.Second)
			case "deleted":
				f.comments = f.comments[1:]
			case "duplicate":
				copy := f.comments[0]
				copy.ID += 1000
				f.comments = append(f.comments, copy)
			case "foreign-author":
				f.comments[0].User.Login = "someone-else"
			case "version":
				f.comments[1].Body = strings.Replace(f.comments[1].Body, journalArchivePrefix+"v1", journalArchivePrefix+"v2", 1)
			default:
				var manifest journalArchiveManifest
				if err := parseJournalArchive(f.comments[1].Body, journalArchivePrefix, &manifest); err != nil {
					t.Fatal(err)
				}
				if mode == "hash" {
					manifest.EncodedSHA256 = strings.Repeat("0", 64)
				}
				if mode == "size" {
					manifest.DecodedSize++
				}
				if mode == "reference" {
					manifest.Parts[0].ID++
				}
				f.comments[1].Body, _ = renderJournalArchive(journalArchivePrefix, manifest)
			}
			if _, err := f.client.ExportJournal(context.Background()); err == nil {
				t.Fatal("corrupt archive exported")
			}
			posts := f.posts
			if _, err := f.client.RestoreJournal(context.Background(), portableForeignBundle(t, false)); err == nil || f.posts != posts {
				t.Fatal("corruption overwritten", err)
			}
		})
	}
	f := newPortableRestoreFixture(t)
	f.mode = "manifest-denied"
	if _, err := f.client.RestoreJournal(context.Background(), portableForeignBundle(t, false)); err == nil {
		t.Fatal("expected interruption")
	}
	f.client.journal = nil
	if err := f.client.TrackActions("ses_other"); err != nil {
		t.Fatal(err)
	}
	posts := f.posts
	if _, err := f.client.RestoreJournal(context.Background(), portableForeignBundle(t, false)); err == nil || !strings.Contains(err.Error(), "unfinished journal archive belongs") || f.posts != posts {
		t.Fatal("foreign session adopted incomplete archive", err)
	}
	if _, err := f.client.ExportJournal(context.Background()); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatal("incomplete history silently exported", err)
	}
}

func TestPortableJournalMappingConflictsAndMissingTargetsFailBeforeWrites(t *testing.T) {
	f := newPortableRestoreFixture(t)
	bundle := portableForeignBundle(t, false)
	for _, input := range []string{`null`, `[]`, `{"gh-1":"gh-2"}`, `{"github:source/project:gh-1":"2"}`} {
		if _, err := ParseJournalMapping([]byte(input)); err == nil {
			t.Fatal("invalid mapping accepted", input)
		}
	}
	if _, err := BindJournal(bundle, "owner/repo", JournalMapping{"github:source/project:gh-999": "gh-2"}); err == nil {
		t.Fatal("unknown reference accepted")
	}
	bound, err := BindJournal(bundle, "owner/repo", JournalMapping{"github:source/project:gh-1": "gh-2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BindJournal(bound, "owner/repo", JournalMapping{"github:source/project:gh-1": "gh-1"}); err == nil {
		t.Fatal("existing mapping overwritten")
	}
	missing, err := BindJournal(bundle, "owner/repo", JournalMapping{"github:source/project:gh-1": "gh-999"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.RestoreJournal(context.Background(), missing); err == nil || f.posts != 0 {
		t.Fatal("missing target accepted", err)
	}
	if _, err := f.client.RestoreJournal(context.Background(), bound); err != nil {
		t.Fatal(err)
	}
	conflicting, err := BindJournal(bundle, "owner/repo", JournalMapping{"github:source/project:gh-1": "gh-1"})
	if err != nil {
		t.Fatal(err)
	}
	posts := f.posts
	if _, err := f.client.RestoreJournal(context.Background(), conflicting); err == nil || f.posts != posts {
		t.Fatal("conflicting archived mapping accepted", err)
	}
}

func TestPortableJournalCapacityAndAmbiguousCarrierStopBeforeWrites(t *testing.T) {
	f := newPortableRestoreFixture(t)
	bundle := portableForeignBundle(t, false)
	random := make([]byte, 5<<20)
	if _, err := rand.New(rand.NewSource(66)).Read(random); err != nil {
		t.Fatal(err)
	}
	source := &bundle.Sources[0]
	source.Actions[0].Before.Body = base64.StdEncoding.EncodeToString(random)
	source.Actions[0].Before.Revision, _ = source.Actions[0].Before.checksum()
	source.SHA256, _ = source.checksum()
	bundle.SHA256, _ = bundle.checksum()
	if _, err := f.client.RestoreJournal(context.Background(), bundle); err == nil || !strings.Contains(err.Error(), "exceeds capacity") || len(f.calls) != 0 {
		t.Fatal("oversized actual payload reached GitHub", err, len(f.calls))
	}
	duplicate := f.issues[2]
	duplicate.Number = 10
	f.issues = append(f.issues, duplicate)
	if _, err := f.client.PlanJournalRestore(context.Background(), portableForeignBundle(t, false)); err == nil || !strings.Contains(err.Error(), "multiple td journal carriers") || f.posts != 0 {
		t.Fatal("ambiguous carrier accepted", err)
	}
}

func TestPortableJournalCancelledRestoreDoesNotPublishManifest(t *testing.T) {
	f := newPortableRestoreFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := original(ctx, dir, payload, args...)
		if args[4] == "POST" {
			cancel()
		}
		return data, err
	}
	if _, err := f.client.RestoreJournal(ctx, portableForeignBundle(t, false)); !errors.Is(err, context.Canceled) || f.posts != 1 || len(f.comments) != 1 {
		t.Fatal("cancelled restore continued writing", err, f.posts)
	}
}

func TestPortableJournalRejectsManyToOneMappingsAcrossSourceSnapshots(t *testing.T) {
	first := portableForeignBundle(t, false)
	second := portableForeignBundle(t, false)
	source := &second.Sources[0]
	action := &source.Actions[0]
	action.IssueID, action.ID, action.OperationID = "gh-3", "ghc-999", "another-source-operation"
	action.URL = "https://github.com/source/project/issues/3#issuecomment-999"
	source.SHA256, _ = source.checksum()
	second.SHA256, _ = second.checksum()
	a, err := BindJournal(first, "owner/repo", JournalMapping{"github:source/project:gh-1": "gh-2"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := BindJournal(second, "owner/repo", JournalMapping{"github:source/project:gh-3": "gh-2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mergePortableJournals(a, b); err == nil || !strings.Contains(err.Error(), "many-to-one") {
		t.Fatal("source snapshots bypassed one-to-one mapping", err)
	}
}
