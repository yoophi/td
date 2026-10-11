package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

type ownedImportFixture struct {
	client   *Client
	items    []ImportItem
	force    bool
	native   map[int]map[string]any
	writes   *int
	comments *[]apiComment
	root     *apiComment
	parts    []apiComment
	extra    []apiComment
	ctx      context.Context
	journal  *ActionJournalRecord
}

func newOwnedImportFixture(t *testing.T, overwrite bool, beforeRoot ...func(*ownedImportFixture)) *ownedImportFixture {
	t.Helper()
	c, native, writes, comments := importBatchFixture(t)
	f := &ownedImportFixture{client: c, native: native, writes: writes, comments: comments, force: overwrite}
	clock := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	for _, issue := range native {
		issue["created_at"], issue["updated_at"], issue["user"] = clock, clock, map[string]string{"login": "original-author"}
	}
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		method, endpoint := args[4], args[5]
		all := func(carrierOnly bool) []apiComment {
			out := append([]apiComment{}, f.parts...)
			if f.root != nil {
				out = append(out, *f.root)
			}
			out = append(out, f.extra...)
			if !carrierOnly {
				out = append(out, (*comments)...)
			}
			return out
		}
		if method == "GET" && endpoint == "repos/owner/repo/issues/comments/1000000" {
			return json.Marshal(f.root)
		}
		if strings.Contains(endpoint, "/issues/comments?") {
			return json.Marshal([][]apiComment{all(false)})
		}
		if strings.Contains(endpoint, "/issues/4/comments?") {
			return json.Marshal([][]apiComment{all(true)})
		}
		data, err := base(ctx, dir, payload, args...)
		if err != nil {
			return data, err
		}
		if method == "POST" && strings.HasSuffix(endpoint, "/comments") {
			clock = clock.Add(time.Second)
			comment := &(*comments)[len(*comments)-1]
			comment.CreatedAt, comment.UpdatedAt, comment.User.Login = clock, clock, "github-author"
			n, err := c.commentIssueNumber(comment.IssueURL)
			if err != nil {
				t.Fatal(err)
			}
			native[n]["updated_at"] = clock
			return json.Marshal(comment)
		}
		if method == "POST" || method == "PATCH" {
			var issue apiIssue
			if err := json.Unmarshal(data, &issue); err != nil {
				t.Fatal(err)
			}
			clock = clock.Add(time.Second)
			native[issue.Number]["updated_at"] = clock
			if method == "POST" {
				native[issue.Number]["created_at"], native[issue.Number]["user"] = clock, map[string]string{"login": "github-author"}
			}
			return fixtureIssueJSON(native[issue.Number])
		}
		return data, nil
	}
	first, second := importTestItem("td-first"), importTestItem("td-second")
	first.SourceNamespace, second.SourceNamespace = "owned-project", "owned-project"
	if overwrite {
		exported, err := c.ExportIssues(context.Background(), true, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range exported {
			if source.Record.ID == "gh-1" {
				first = ImportItem{Issue: source.Record.Issue, GitHub: &source}
			}
		}
	}
	carrierBody, err := encodeBody("Journal", metadata{EntityKind: "journal", Type: models.TypeTask, Priority: models.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	native[4] = map[string]any{"number": 4, "title": "Journal", "state": "open", "body": carrierBody, "created_at": clock, "updated_at": clock}
	f.items = []ImportItem{first, second}
	for _, setup := range beforeRoot {
		setup(f)
	}
	plan, err := c.PlanImport(context.Background(), f.items, f.force)
	if err != nil {
		t.Fatal(err)
	}
	events := map[string][]nativeStateEvent{}
	for _, row := range plan.Rows {
		if row.TargetID != "" {
			events[row.TargetID] = []nativeStateEvent{}
		}
	}
	input, err := prepareImportCommand(f.items, f.force, plan, events)
	if err != nil {
		t.Fatal(err)
	}
	a := ActionJournal{OperationID: "owned-import-operation", SessionID: "ses_owner", CarrierID: "gh-4", Action: "import", Phase: "prepared", ImportCommand: input}
	publication, err := planActionJournalPublication(a)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	for n, body := range publication.importArchive.bodies {
		part := apiComment{ID: int64(100 + n), Body: body, IssueURL: "https://api.github.com/repos/owner/repo/issues/4", CreatedAt: clock, UpdatedAt: clock}
		part.User.Login = "github-author"
		f.parts = append(f.parts, part)
	}
	rootPlan, err := publication.importArchive.bindParts(f.parts)
	if err != nil || len(rootPlan.partBodies) != 0 {
		t.Fatal("fixture root must be inline", err)
	}
	clock = clock.Add(time.Second)
	f.root = &apiComment{ID: 1000000, Body: rootPlan.body, IssueURL: "https://api.github.com/repos/owner/repo/issues/4", CreatedAt: clock, UpdatedAt: clock}
	f.root.User.Login = "github-author"
	native[4]["updated_at"] = clock
	f.journal, err = decodeMultipartActionJournal(*f.root, "gh-4", f.parts)
	if err != nil {
		t.Fatal(err)
	}
	f.ctx, err = withImportExecution(context.Background(), f.journal, "ses_owner", "github-author")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestImportOwnedExecutionUsesOriginalRootAcrossLostResponses(t *testing.T) {
	for _, stage := range []string{"none", "allocation", "intent", "apply", "completion"} {
		t.Run(stage, func(t *testing.T) {
			f := newOwnedImportFixture(t, false)
			base, lost := f.client.run, false
			failure := errors.New("fixture response lost after owned import write")
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				data, err := base(ctx, dir, payload, args...)
				match := stage == "allocation" && args[4] == "POST" && strings.HasSuffix(args[5], "/issues") || stage == "intent" && strings.Contains(string(payload), "td:import-intent:") || stage == "apply" && args[4] == "PATCH" || stage == "completion" && strings.Contains(string(payload), "td:import-receipt:")
				if !lost && match && err == nil {
					lost = true
					return nil, failure
				}
				return data, err
			}
			result, err := f.client.executeImport(f.ctx, f.items, false)
			if stage != "none" {
				if !errors.Is(err, failure) || !lost {
					t.Fatal("lost response was hidden", err)
				}
				result, err = f.client.executeImport(f.ctx, f.items, false)
			}
			if err != nil || result.Completed+result.Skipped != 2 || *f.writes != 4 {
				t.Fatal("owned resume repeated writes or lost input", result, err, *f.writes)
			}
			owner := importExecution(f.ctx).owner
			for _, row := range result.Plan.Rows {
				r, err := f.client.Get(context.Background(), row.TargetID)
				if err != nil || r.meta.ImportOwner == nil || *r.meta.ImportOwner != owner {
					t.Fatal("allocation lost atomic command ownership", r, err)
				}
			}
			for _, comment := range *f.comments {
				intent, err := decodeImportIntent(comment.Body)
				if err != nil {
					t.Fatal(err)
				}
				if intent != nil && (intent.Version != 2 || intent.Owner == nil || *intent.Owner != owner) {
					t.Fatal("intent lost command ownership", intent)
				}
				receipt, err := decodeImportReceipt(comment.Body)
				if err != nil {
					t.Fatal(err)
				}
				if receipt != nil && (receipt.Version != 2 || receipt.Owner == nil || *receipt.Owner != owner) {
					t.Fatal("completion lost command ownership", receipt)
				}
			}
			before := len(*f.comments)
			if _, err := f.client.executeImport(f.ctx, f.items, false); err != nil || *f.writes != 4 || len(*f.comments) != before {
				t.Fatal("owned repeat wrote again", err)
			}
		})
	}
}

func TestImportExecutionFenceUsesFreshCarrierCollection(t *testing.T) {
	for _, change := range []string{"none", "missing root", "changed root", "missing part", "changed part", "foreign part", "duplicate part"} {
		t.Run(change, func(t *testing.T) {
			f := newOwnedImportFixture(t, false)
			base := f.client.run
			pointReads, collections := 0, 0
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if args[4] != "GET" {
					t.Fatal("read fence attempted a write", args)
				}
				if args[5] == "repos/owner/repo/issues/comments/1000000" {
					pointReads++
				}
				if args[5] == "repos/owner/repo/issues/4/comments?per_page=100" {
					collections++
				}
				return base(ctx, dir, payload, args...)
			}
			if err := f.client.fenceImportExecution(f.ctx); err != nil {
				t.Fatal(err)
			}
			if pointReads != 0 || collections != 1 {
				t.Fatalf("fence duplicated root retrieval: point=%d collections=%d", pointReads, collections)
			}
			switch change {
			case "missing root":
				f.root = nil
			case "changed root":
				f.root.UpdatedAt = f.root.UpdatedAt.Add(time.Second)
			case "missing part":
				f.parts = f.parts[1:]
			case "changed part":
				f.parts[0].Body += "changed"
			case "foreign part":
				f.parts[0].User.Login = "another-user"
			case "duplicate part":
				f.extra = append(f.extra, f.parts[0])
			}
			err := f.client.fenceImportExecution(f.ctx)
			if (err != nil) != (change != "none") {
				t.Fatal("fresh fence ignored changed native evidence", change, err)
			}
			if pointReads != 0 || collections != 2 {
				t.Fatalf("fence reused a stale collection or duplicated root GET: point=%d collections=%d", pointReads, collections)
			}
		})
	}
}

func TestImportOwnedExecutionRejectsTransferChangedInputAndDuplicateRoots(t *testing.T) {
	for _, kind := range []string{"untracked", "other session", "other actor", "input", "force", "overlap", "native edit"} {
		t.Run(kind, func(t *testing.T) {
			f := newOwnedImportFixture(t, kind == "native edit")
			ctx, items, force := f.ctx, f.items, f.force
			var err error
			switch kind {
			case "untracked":
				ctx = context.Background()
			case "other session":
				ctx, err = withImportExecution(ctx, f.journal, "ses_other", "github-author")
			case "other actor":
				ctx, err = withImportExecution(ctx, f.journal, "ses_owner", "different-author")
			case "input":
				items, _ = freezeImportItems(items)
				items[1].Issue.Title = "Changed input"
			case "force":
				force = !force
			case "overlap":
				a, _ := copyCreationJournal(f.journal.ActionJournal)
				a.OperationID = "different-import-operation"
				other := *f.root
				other.ID++
				other.Body, err = renderActionJournal(a)
				if err != nil {
					t.Fatal(err)
				}
				f.extra = append(f.extra, other)
			case "native edit":
				f.native[1]["title"] = "Unrelated concurrent change"
			}
			if err == nil {
				_, err = f.client.executeImport(ctx, items, force)
			}
			if err == nil || *f.writes != 0 || len(*f.comments) != 0 {
				t.Fatal("invalid ownership caused writes", err, *f.writes, len(*f.comments))
			}
		})
	}
}

func TestImportOwnedRootEditStopsNextAllocation(t *testing.T) {
	f := newOwnedImportFixture(t, false)
	base, changed := f.client.run, false
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if !changed && err == nil && args[4] == "POST" && strings.HasSuffix(args[5], "/issues") {
			changed = true
			f.root.Body = strings.Replace(f.root.Body, `"force":false`, `"force":true`, 1)
			f.root.UpdatedAt = f.root.UpdatedAt.Add(time.Second)
		}
		return data, err
	}
	if _, err := f.client.executeImport(f.ctx, f.items, false); err == nil || !changed || *f.writes != 1 || len(*f.comments) != 0 {
		t.Fatal("changed root authorized another write", err, *f.writes)
	}
}

func TestImportOwnedOverwriteCaptureAndLegacyVersionContracts(t *testing.T) {
	f := newOwnedImportFixture(t, true)
	if _, err := f.client.executeImport(f.ctx, f.items, true); err != nil {
		t.Fatal(err)
	}
	owner := importExecution(f.ctx).owner
	found := false
	for _, comment := range *f.comments {
		capture, err := decodeImportCapture(comment.Body)
		if err != nil {
			t.Fatal(err)
		}
		if capture != nil {
			found = true
			if capture.Version != 2 || capture.Owner == nil || *capture.Owner != owner {
				t.Fatal("capture lost owner")
			}
		}
	}
	if !found {
		t.Fatal("overwrite did not archive its baseline")
	}
	for _, version := range []int{1, 2, 3} {
		for _, o := range []*ImportExecutionOwner{nil, &owner} {
			if got := validateImportCheckpointOwner(version, o); (got == nil) != (version == 1 && o == nil || version == 2 && o != nil) {
				t.Fatal("ownership downgrade accepted", version, o, got)
			}
		}
	}
	input := importTestItem("td-legacy")
	digest, _ := input.Digest()
	legacy := importReceipt{Version: 1, SourceKey: input.SourceKey(), SourceDigest: digest, TargetID: "gh-1", TargetBasis: strings.Repeat("a", 64)}
	body, err := renderImportReceipt(legacy)
	decoded, readErr := decodeImportReceipt(body)
	if err != nil || readErr != nil || !reflect.DeepEqual(*decoded, legacy) || !strings.Contains(body, importReceiptStart) {
		t.Fatal("legacy receipt changed", err, readErr)
	}
	owned := legacy
	owned.Version, owned.Owner = 2, &owner
	body, err = renderImportReceipt(owned)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeImportReceipt(strings.Replace(body, "td:import-receipt:v2", "td:import-receipt:v1", 1)); err == nil {
		t.Fatal("owned receipt disguised as legacy framing")
	}
	if _, err := encodeBody("bad", metadata{Type: models.TypeTask, Priority: models.PriorityP2, OperationID: "ordinary-create", ImportOwner: &owner}); err == nil {
		t.Fatal("ordinary task claimed an import allocation")
	}
}

// A matching JSON owner is not proof that a checkpoint was written by the
// original execution. These tests drive real replanning and target writes.
func TestImportOwnedRejectsForeignOverwriteIntent(t *testing.T) {
	f := newOwnedImportFixture(t, true)
	injectForeignImportIntent(t, f, "gh-1", 2)
	assertImportOwnershipRefusal(t, f)
}

func TestImportOwnedRejectsNewLegacyIntentAfterAllocation(t *testing.T) {
	f := newOwnedImportFixture(t, false)
	base := f.client.run
	failure := errors.New("allocation response lost")
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if err == nil && args[4] == "POST" && strings.HasSuffix(args[5], "/issues") {
			return nil, failure
		}
		return data, err
	}
	if _, err := f.client.executeImport(f.ctx, f.items, false); !errors.Is(err, failure) || *f.writes != 1 {
		t.Fatal("did not stop after the first allocation", err, *f.writes)
	}
	f.client.run = base
	injectForeignImportIntent(t, f, "gh-5", 1)
	assertImportOwnershipRefusal(t, f)
}

func injectForeignImportIntent(t *testing.T, f *ownedImportFixture, target string, version int) {
	t.Helper()
	n, _ := Number(target)
	f.native[n]["title"] = "External change must survive import resume"
	native, record, err := f.client.importNativeTarget(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	_, projection, err := importIssueProjection(f.items[0], native, record, importRelations{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := importTargetBasis(native)
	if err != nil {
		t.Fatal(err)
	}
	after, err := importTargetBasis(projection)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := f.items[0].Digest()
	if err != nil {
		t.Fatal(err)
	}
	intent := importIntent{Version: version, SourceKey: f.items[0].SourceKey(), SourceDigest: digest, TargetID: target, BeforeBasis: before, AfterBasis: after}
	if version == 2 {
		owner := importExecution(f.ctx).owner
		intent.Owner = &owner
	}
	body, err := renderImportIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	clock := f.root.CreatedAt.Add(time.Minute)
	comment := apiComment{ID: 20000, Body: body, IssueURL: fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", n), CreatedAt: clock, UpdatedAt: clock}
	comment.User.Login = "different-author"
	*f.comments = append(*f.comments, comment)
}

func assertImportOwnershipRefusal(t *testing.T, f *ownedImportFixture) {
	t.Helper()
	before, err := json.Marshal(f.native)
	if err != nil {
		t.Fatal(err)
	}
	writes, comments := *f.writes, len(*f.comments)
	_, err = f.client.executeImport(f.ctx, f.items, f.force)
	after, marshalErr := json.Marshal(f.native)
	if err == nil || !strings.Contains(err.Error(), "checkpoint") || *f.writes != writes || len(*f.comments) != comments || marshalErr != nil || string(before) != string(after) {
		t.Fatal("untrusted checkpoint authorized import effects", err, "writes", writes, *f.writes, "comments", comments, len(*f.comments))
	}
}

func TestImportOwnedRejectsUnverifiedCheckpointNativeEvidence(t *testing.T) {
	for _, kind := range []string{"intent", "capture", "receipt"} {
		for _, alteration := range []string{"author", "edited", "predates command"} {
			t.Run(kind+"/"+alteration, func(t *testing.T) {
				f := newOwnedImportFixture(t, kind == "capture")
				base := f.client.run
				prefix := "td:import-" + kind + ":"
				failure := errors.New("checkpoint response lost")
				f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
					data, err := base(ctx, dir, payload, args...)
					if err == nil && args[4] == "POST" && strings.Contains(string(payload), prefix) {
						return nil, failure
					}
					return data, err
				}
				if _, err := f.client.executeImport(f.ctx, f.items, f.force); !errors.Is(err, failure) {
					t.Fatal("did not stop after checkpoint publication", err)
				}
				f.client.run = base
				comment := &(*f.comments)[len(*f.comments)-1]
				if !strings.Contains(comment.Body, prefix) {
					t.Fatal("wrong checkpoint selected")
				}
				switch alteration {
				case "author":
					comment.User.Login = "different-author"
				case "edited":
					comment.UpdatedAt = comment.CreatedAt.Add(time.Second)
				case "predates command":
					comment.CreatedAt = f.root.CreatedAt.Add(-time.Second)
					comment.UpdatedAt = comment.CreatedAt
				}
				assertImportOwnershipRefusal(t, f)
			})
		}
	}
}

func legacyImportBeforeRoot(t *testing.T, kind string) func(*ownedImportFixture) {
	t.Helper()
	return func(f *ownedImportFixture) {
		base := f.client.run
		failure := errors.New("legacy checkpoint response lost")
		f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
			data, err := base(ctx, dir, payload, args...)
			if err == nil && args[4] == "POST" && strings.Contains(string(payload), "td:import-"+kind+":") {
				return nil, failure
			}
			return data, err
		}
		if _, err := f.client.executeImport(context.Background(), f.items, f.force); !errors.Is(err, failure) {
			t.Fatal("legacy setup did not stop at checkpoint", err)
		}
		f.client.run = base
		// An older author's edited v1 checkpoint is valid as a baseline only
		// because its exact native observation precedes this command root.
		comment := &(*f.comments)[len(*f.comments)-1]
		comment.User.Login = "legacy-author"
		comment.UpdatedAt = comment.CreatedAt.Add(time.Second)
	}
}

func TestImportOwnedContinuesExactLegacyBaseline(t *testing.T) {
	for _, kind := range []string{"intent", "capture", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			f := newOwnedImportFixture(t, kind == "capture", legacyImportBeforeRoot(t, kind))
			before := append([]apiComment(nil), (*f.comments)...)
			result, err := f.client.executeImport(f.ctx, f.items, f.force)
			if err != nil || result.Created != 0 || result.Completed+result.Skipped != 2 {
				t.Fatal("original legacy baseline could not continue", result, err)
			}
			if !reflect.DeepEqual(before, (*f.comments)[:len(before)]) {
				t.Fatal("continuation rewrote earlier native history")
			}
			for _, member := range f.journal.ImportCommand.Members {
				if member.Before == nil || member.Action == "create" {
					t.Fatal("continuation claimed a prior allocation", member)
				}
			}
			for _, row := range result.Plan.Rows {
				record, err := f.client.Get(context.Background(), row.TargetID)
				if err != nil || record.meta.ImportOwner != nil {
					t.Fatal("continuation changed original allocation ownership", err)
				}
			}
			writes, comments := *f.writes, len(*f.comments)
			if _, err := f.client.executeImport(f.ctx, f.items, f.force); err != nil || *f.writes != writes || len(*f.comments) != comments {
				t.Fatal("completed legacy continuation repeated effects", err)
			}
		})
	}
}

func TestImportOwnedRejectsChangedLegacyBaseline(t *testing.T) {
	for _, alteration := range []string{"edited", "missing"} {
		t.Run(alteration, func(t *testing.T) {
			f := newOwnedImportFixture(t, false, legacyImportBeforeRoot(t, "intent"))
			n := len(*f.comments) - 1
			if alteration == "edited" {
				(*f.comments)[n].Body = "Replaced legacy checkpoint with plain text"
			} else {
				*f.comments = (*f.comments)[:n]
			}
			assertImportOwnershipRefusal(t, f)
		})
	}
}

func TestImportOwnedRejectsPreexistingAllocationTarget(t *testing.T) {
	f := newOwnedImportFixture(t, false)
	owner := importExecution(f.ctx).owner
	body, err := encodeBody(f.items[0].Issue.Description, metadata{OperationID: importOperationID(f.items[0].SourceKey()), ImportOwner: &owner, Type: f.items[0].Issue.Type, Priority: f.items[0].Issue.Priority})
	if err != nil {
		t.Fatal(err)
	}
	// Native timestamps have second resolution. Even if an existing task and
	// root share author/time, changing its metadata cannot claim it as new.
	f.native[1]["body"], f.native[1]["user"] = body, map[string]string{"login": "github-author"}
	f.native[1]["created_at"] = f.root.CreatedAt
	if _, err := f.client.executeImport(f.ctx, f.items, false); err == nil || !strings.Contains(err.Error(), "existed before command") || *f.writes != 0 || len(*f.comments) != 0 {
		t.Fatal("original inventory task was accepted as a new allocation", err, *f.writes)
	}
}
