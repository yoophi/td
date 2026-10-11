package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func completedImportFixture(t *testing.T, overwrite ...bool) (*ownedImportFixture, ActionJournal) {
	t.Helper()
	f := newOwnedImportFixture(t, len(overwrite) > 0 && overwrite[0])
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if args[4] == "GET" && strings.Contains(args[5], "/events?") {
			return []byte("[[]]"), nil // Both fixture imports retain native open state.
		}
		return base(ctx, dir, payload, args...)
	}
	if _, err := f.client.executeImport(f.ctx, f.items, f.force); err != nil {
		t.Fatal(err)
	}
	forward, err := f.client.observeImportCompletion(f.ctx, f.journal, f.items, f.force, CreationObservation{Proof: "verified", SessionID: f.journal.SessionID, Actor: f.journal.Author, ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := copyCreationJournal(f.journal.ActionJournal)
	if err != nil {
		t.Fatal(err)
	}
	a.Phase, a.ImportForward = "applied", forward
	if err := a.validate(); err != nil {
		t.Fatal(err)
	}
	return f, a
}

func TestImportForwardRejectsIncompleteAlteredAndForeignEvidence(t *testing.T) {
	f, original := completedImportFixture(t)
	for _, kind := range []string{"missing member", "wrong projection", "prepared result", "verified observer", "foreign checkpoint", "native event cycle"} {
		t.Run(kind, func(t *testing.T) {
			a, err := copyCreationJournal(original)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing member":
				a.ImportForward.Targets = a.ImportForward.Targets[:1]
			case "wrong projection":
				a.ImportForward.Targets[0].Issue.Title = "Foreign change"
			case "prepared result":
				a.Phase = "prepared"
			case "verified observer":
				a.ImportForward.Observation.SessionID = "ses_observer"
			case "foreign checkpoint":
				found := false
				for n := range a.ImportForward.Targets[0].Comments {
					comment := &a.ImportForward.Targets[0].Comments[n]
					if strings.Contains(comment.Body, importIntentPrefix) {
						comment.User.Login = "foreign-author"
						found = true
					}
				}
				if !found {
					t.Fatal("missing test checkpoint")
				}
			case "native event cycle":
				a.ImportForward.Targets[0].Events = []nativeStateEvent{{ID: 1, Event: "closed"}, {ID: 2, Event: "reopened"}}
			}
			native := *f.root
			native.UpdatedAt = time.Now().UTC()
			native.Body, err = renderActionJournal(a)
			if err == nil {
				_, err = decodeActionJournal(native, f.journal.CarrierID)
			}
			if err == nil {
				t.Fatal("invalid import result was accepted", kind)
			}
		})
	}
	observed, _ := copyCreationJournal(original)
	observed.ImportForward.Observation.Proof, observed.ImportForward.Observation.SessionID = "observed", "ses_observer"
	native := *f.root
	native.UpdatedAt = time.Now().UTC()
	var err error
	native.Body, err = renderActionJournal(observed)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeActionJournal(native, f.journal.CarrierID)
	if err != nil || decoded.ImportForward.Observation.Proof != "observed" || decoded.SessionID != original.SessionID {
		t.Fatal("observation transferred original ownership", decoded, err)
	}
}

func TestImportForwardArchiveKeepsOriginalDigestAndLargeResults(t *testing.T) {
	f, a := completedImportFixture(t)
	beforeDigest, err := importOriginalDigest(f.journal.ImportCommand)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve realistic individual comment sizes while testing result history
	// larger than the ordinary decoded root limit.
	target := &a.ImportForward.Targets[0]
	clock := time.Now().UTC().Add(-time.Minute)
	for n := 0; n < 80; n++ {
		comment := apiComment{ID: int64(2000000 + n), Body: strings.Repeat("native history ", 4000), IssueURL: fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", target.Issue.Number), CreatedAt: clock, UpdatedAt: clock}
		comment.User.Login = "history-author"
		target.Comments = append(target.Comments, comment)
	}
	target.Issue.UpdatedAt = clock
	publication, err := planActionJournalPublication(a)
	if err != nil {
		t.Fatal(err)
	}
	if publication.importArchive.originalDigest != beforeDigest {
		t.Fatal("forward result changed original envelope digest")
	}
	doc := publication.importArchive.manifest.Documents[len(publication.importArchive.manifest.Documents)-1]
	if doc.Name != "forward" || doc.Size <= actionDecodedBudget {
		t.Fatal("fixture did not exceed root size", doc.Size)
	}
	parts := make([]apiComment, len(publication.importArchive.bodies))
	for n, body := range publication.importArchive.bodies {
		parts[n] = apiComment{ID: int64(100 + n), Body: body, IssueURL: f.root.IssueURL, CreatedAt: clock, UpdatedAt: clock}
		parts[n].User.Login = f.root.User.Login
	}
	rootPlan, err := publication.importArchive.bindParts(parts)
	if err != nil || len(rootPlan.partBodies) != 0 {
		t.Fatal("unexpected outer multipart fixture", err)
	}
	native := *f.root
	native.Body, native.UpdatedAt = rootPlan.body, time.Now().UTC()
	decoded, err := decodeMultipartActionJournal(native, f.journal.CarrierID, parts)
	if err != nil {
		t.Fatal(err)
	}
	// Compare canonical copies because missing API raw JSON is archived as null.
	canonical, err := copyCreationJournal(a)
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, canonical) {
		t.Fatal("forward archive lost original input or result", err)
	}
}
