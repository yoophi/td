package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestNotesCRUDIsolationPrivateObservationAndRepeatedFlags(t *testing.T) {
	ctx := context.Background()
	c, issues, writes := hierarchyFixture(t)
	n, err := c.CreateNote(ctx, " 메모 한글 ", "줄 1\n줄 2 🌱\n", "actual-notes")
	if err != nil || n.ID != "nt-gh-4" || n.Title != "메모 한글" || n.Content != "줄 1\n줄 2 🌱\n" {
		t.Fatal(n, err)
	}
	if issues[4]["title"] != n.Title {
		t.Fatal("unexpected prefix")
	}
	if _, err := c.Get(ctx, "gh-4"); err == nil {
		t.Fatal("note exposed as task")
	}
	listed, err := c.List(ctx, true)
	if err != nil || len(listed) != 3 {
		t.Fatal("note mixed into tasks", listed, err)
	}
	issues[4]["state"] = "closed"
	issues[4]["labels"] = []string{"native-label"}
	n, err = c.GetNote(ctx, n.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	pinned := true
	n, _, err = c.UpdateNoteObserved(ctx, n, NoteChanges{Pinned: &pinned}, "actual-notes")
	if err != nil || !n.Pinned {
		t.Fatal(n, err)
	}
	before := *writes
	n, changed, err := c.UpdateNoteObserved(ctx, n, NoteChanges{Pinned: &pinned}, "actual-notes")
	if err != nil || changed || *writes != before {
		t.Fatal("repeated pin wrote", err)
	}
	// Public fields are display values, not replacements for stored metadata.
	n.Pinned = false
	n.Archived = true
	n.Content = "public draft"
	title := "새 제목"
	n, _, err = c.UpdateNoteObserved(ctx, n, NoteChanges{Title: &title}, "second-actor")
	if err != nil || !n.Pinned || n.Archived || n.Content != "줄 1\n줄 2 🌱\n" {
		t.Fatal("private state lost", n, err)
	}
	if issues[4]["state"] != "closed" {
		t.Fatal("native state changed")
	}
	if !strings.Contains(issues[4]["body"].(string), "second-actor") {
		t.Fatal("actual actor missing")
	}
	deleted := true
	n, _, err = c.UpdateNoteObserved(ctx, n, NoteChanges{Deleted: &deleted}, "actual-notes")
	if err != nil || n.DeletedAt == nil {
		t.Fatal(n, err)
	}
	if _, err := c.GetNote(ctx, n.ID, false); err == nil {
		t.Fatal("deleted note exposed")
	}
	rows, err := c.ListNotes(ctx, false)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	rows, err = c.ListNotes(ctx, true)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	before = *writes
	if _, _, err := c.UpdateNoteObserved(ctx, n, NoteChanges{Title: &title}, "actual-notes"); err == nil || *writes != before {
		t.Fatal("deleted note edited")
	}
	deleted = false
	n, _, err = c.UpdateNoteObserved(ctx, n, NoteChanges{Deleted: &deleted}, "actual-notes")
	if err != nil || n.DeletedAt != nil || !n.Pinned {
		t.Fatal("restore lost flags", n, err)
	}
	if *writes != before+1 {
		t.Fatal("restore retried")
	}
	for _, bad := range []string{"gh-4", "4", "nt-gh-04", "nt-gh-0", "bd-gh-4", "nt-123"} {
		if _, err := c.GetNote(ctx, bad, true); err == nil {
			t.Fatal("invalid note id accepted", bad)
		}
	}
}

func TestNoteConflictBeforeAfterAndUncertainWrite(t *testing.T) {
	for _, outcome := range []string{"before", "after", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			c, issues, writes := hierarchyFixture(t)
			n, err := c.CreateNote(ctx, "Original", "content", "actor")
			if err != nil {
				t.Fatal(err)
			}
			baseline := *writes
			title := "Saved"
			if outcome == "before" {
				issues[n.Number]["title"] = "Peer"
			}
			run := c.run
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				data, err := run(ctx, dir, payload, args...)
				if slices.Contains(args, "PATCH") {
					if outcome == "after" {
						issues[n.Number]["title"] = "Peer"
					}
					if outcome == "unknown" {
						return nil, errors.New("connection lost after accepted PATCH")
					}
				}
				return data, err
			}
			_, _, err = c.UpdateNoteObserved(ctx, n, NoteChanges{Title: &title}, "actor")
			if err == nil {
				t.Fatal("unsafe outcome concealed")
			}
			if outcome == "unknown" {
				if !strings.Contains(err.Error(), "inspect GitHub") || *writes != baseline+1 || issues[n.Number]["title"] != "Saved" {
					t.Fatal(err, *writes)
				}
			} else {
				var conflict *ConflictError
				if !errors.As(err, &conflict) || conflict.AfterWrite != (outcome == "after") {
					t.Fatal(err)
				}
				want := baseline
				if outcome == "after" {
					want++
				}
				if *writes != want {
					t.Fatal("conflict wrote/retried")
				}
			}
		})
	}
}

func TestNoteMetadataAndObservationBoundaries(t *testing.T) {
	ctx := context.Background()
	c, issues, writes := hierarchyFixture(t)
	for _, meta := range []metadata{
		{EntityKind: "issue", Note: &NoteDetails{Version: 1}},
		{EntityKind: "note", Note: &NoteDetails{Version: 2}},
		{EntityKind: "note", Note: &NoteDetails{Version: 1}, Details: &IssueDetails{}},
	} {
		meta.Type = models.TypeTask
		meta.Priority = models.PriorityP2
		if _, err := encodeBody("text", meta); err == nil {
			t.Fatal("invalid entity envelope accepted")
		}
	}
	if _, _, _, err := decodeBody(markerStart + `{"entity_kind":"note","type":"task","priority":"P2","note":{"version":1,"future":true}}` + markerEnd); err == nil {
		t.Fatal("unknown note field accepted")
	}
	n, err := c.CreateNote(ctx, "Original", "content", "actor")
	if err != nil {
		t.Fatal(err)
	}
	before := *writes
	title := "Changed"
	foreign := *n
	foreign.observed.repository = "foreign/repo"
	wrong := *n
	wrong.Number++
	for _, bad := range []*NoteRecord{nil, {}, &foreign, &wrong} {
		if _, _, err := c.UpdateNoteObserved(ctx, bad, NoteChanges{Title: &title}, "actor"); err == nil || *writes != before {
			t.Fatal("invalid observation wrote")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := c.UpdateNoteObserved(cancelled, n, NoteChanges{Title: &title}, "actor"); !errors.Is(err, context.Canceled) || *writes != before {
		t.Fatal("cancelled write ran", err)
	}
	issues[n.Number]["body"] = "visible text\n\n" + markerStart + `{"entity_kind":"note","type":"task","priority":"P2","note":{"version":99}}` + markerEnd
	if _, err := c.ListNotes(ctx, true); err == nil {
		t.Fatal("future metadata ignored")
	}
}

func TestNoteCreateUncertainAndIgnoredPatch(t *testing.T) {
	for _, scenario := range []string{"create-unknown", "ignored-patch", "verification-denied"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			c, issues, writes := hierarchyFixture(t)
			var n *NoteRecord
			if scenario != "create-unknown" {
				var err error
				n, err = c.CreateNote(ctx, "Original", "body", "actor")
				if err != nil {
					t.Fatal(err)
				}
			}
			before := *writes
			run := c.run
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if scenario == "verification-denied" && slices.Contains(args, "GET") && *writes > before {
					return nil, errors.New("HTTP 403 verification denied")
				}
				if scenario == "ignored-patch" && slices.Contains(args, "PATCH") {
					var patch map[string]any
					if err := json.Unmarshal(payload, &patch); err != nil {
						t.Fatal(err)
					}
					patch["title"] = "Ignored title"
					payload, _ = json.Marshal(patch)
				}
				data, err := run(ctx, dir, payload, args...)
				if scenario == "create-unknown" && slices.Contains(args, "POST") {
					return nil, errors.New("connection lost after create")
				}
				return data, err
			}
			var err error
			if scenario == "create-unknown" {
				_, err = c.CreateNote(ctx, "New note", "body", "actor")
			} else {
				title := "Wanted title"
				_, _, err = c.UpdateNoteObserved(ctx, n, NoteChanges{Title: &title}, "actor")
			}
			if err == nil || !strings.Contains(err.Error(), "inspect GitHub") || *writes != before+1 {
				t.Fatal("uncertain/partial failure concealed or retried", err, *writes)
			}
			if scenario == "create-unknown" && len(issues) != 4 {
				t.Fatal("accepted create lost", issues)
			}
		})
	}
}
