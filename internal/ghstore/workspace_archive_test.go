package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/transferlimit"
)

func TestWorkspaceExportCollectsPassiveArchiveWithoutNPlusOneOrActiveHistory(t *testing.T) {
	_, root, parts, data := workspaceArchiveFixture(t)
	body, err := encodeBody("storage", metadata{EntityKind: "journal", Type: models.TypeTask, Priority: models.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	carrier := apiIssue{Number: 9, Title: "journal", Body: body, State: "open", CreatedAt: root.CreatedAt, UpdatedAt: root.CreatedAt}
	carrier.User.Login = "publisher"
	comments := append(append([]apiComment{}, parts...), root)
	client, calls := snapshotFixture([][]apiIssue{{carrier}}, [][]apiComment{comments}, nil)
	workspace, err := client.ExportWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || len(workspace.Tasks) != 0 || len(workspace.Archives) != 1 || string(workspace.Archives[0].SourceEnvelope) != string(data) || len(workspace.Native[0].Comments) != len(comments) {
		t.Fatal("passive archive omitted or N+1 queries", *calls)
	}
	for _, source := range workspace.Journal.Sources {
		if len(source.Actions) != 0 {
			t.Fatal("archive became executable journal history")
		}
	}
	client, _ = snapshotFixture([][]apiIssue{{carrier}}, [][]apiComment{append(parts[1:], root)}, nil)
	if workspace, err := client.ExportWorkspace(context.Background()); err == nil || workspace != nil {
		t.Fatal("missing source parts returned partial workspace")
	}
}

func workspaceArchiveFixture(t *testing.T) (*WorkspaceArchivePlan, apiComment, []apiComment, []byte) {
	t.Helper()
	data := []byte(`{"version":1,"store":"sqlite","namespace":"original","sha256":"` + strings.Repeat("a", 64) + `","payload":{"original":"` + strings.Repeat("history", 11000) + `"}}`)
	p, err := PlanWorkspaceArchive("owner/repo", "gh-9", "td-workspace-op", data, map[string]string{"source:task": "gh-10"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	root := apiComment{ID: 1000, IssueURL: "https://api.github.com/repos/owner/repo/issues/9", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)}
	root.User.Login = "publisher"
	parts := make([]apiComment, len(p.bodies))
	for i, body := range p.bodies {
		parts[i] = root
		parts[i].ID = int64(100 + i)
		parts[i].Body = body
		parts[i].CreatedAt = now
		parts[i].UpdatedAt = now
	}
	root.Body, err = p.bindParts(parts)
	if err != nil {
		t.Fatal(err)
	}
	return p, root, parts, data
}

func TestWorkspaceArchiveRoundTripIsPassiveAndUsesOneCollection(t *testing.T) {
	p, root, parts, data := workspaceArchiveFixture(t)
	if p.PartCount() < 2 {
		t.Fatal("fixture is not multipart")
	}
	collection, err := collectJournalParts(parts)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := decodeWorkspaceArchive(root, "owner/repo", "gh-9", collection, 0)
	if err != nil || string(archive.SourceEnvelope) != string(data) || archive.Mappings["source:task"] != "gh-10" || archive.ID != root.ID || archive.Author != "publisher" {
		t.Fatal("archive bytes/mappings/native evidence lost", err)
	}
	// Archived source metadata does not become an executable action journal.
	if action, err := decodeJournalFromCollection(root, "gh-9", collection); err != nil || action != nil {
		t.Fatal("passive source became an action", err)
	}
	if result, err := decodeWorkspaceArchive(root, "other/repo", "gh-9", collection, 0); err == nil || result != nil {
		t.Fatal("foreign repository accepted")
	}
	if result, err := decodeWorkspaceArchive(root, "owner/repo", "gh-9", collection, 1); !errors.Is(err, transferlimit.ErrCapacity) || result != nil {
		t.Fatal("capacity returned partial archive", err)
	}
}

func TestWorkspaceArchiveRejectsMissingChangedForeignOrDuplicateParts(t *testing.T) {
	for _, stage := range []string{"missing", "edited", "foreign_author", "foreign_repo", "duplicate_part", "duplicate_ref", "root_edited", "source_hash"} {
		t.Run(stage, func(t *testing.T) {
			_, root, parts, _ := workspaceArchiveFixture(t)
			switch stage {
			case "missing":
				parts = parts[1:]
			case "edited":
				parts[0].UpdatedAt = parts[0].UpdatedAt.Add(time.Second)
			case "foreign_author":
				parts[0].User.Login = "other"
			case "foreign_repo":
				parts[0].IssueURL = "https://api.github.com/repos/other/repo/issues/9"
			case "duplicate_part":
				duplicate := parts[0]
				duplicate.ID = 800
				parts = append(parts, duplicate)
			case "root_edited":
				root.UpdatedAt = root.UpdatedAt.Add(time.Second)
			case "duplicate_ref", "source_hash":
				block := strings.TrimSuffix(strings.Split(root.Body, workspaceArchiveStart)[1], markerEnd)
				var m workspaceArchiveManifest
				if err := json.Unmarshal([]byte(block), &m); err != nil {
					t.Fatal(err)
				}
				if stage == "duplicate_ref" {
					m.Parts[1] = m.Parts[0]
				} else {
					m.SourceSHA256 = strings.Repeat("b", 64)
				}
				var err error
				root.Body, err = renderWorkspaceArchiveManifest(m)
				if err != nil {
					t.Fatal(err)
				}
			}
			collection, err := collectJournalParts(parts)
			if err != nil {
				t.Fatal(err)
			}
			if record, err := decodeWorkspaceArchive(root, "owner/repo", "gh-9", collection, 0); err == nil || record != nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
}

func TestWorkspaceArchivePlanningAndBindingLimits(t *testing.T) {
	p, _, parts, data := workspaceArchiveFixture(t)
	if body, err := p.bindParts(parts[:1]); err == nil || body != "" {
		t.Fatal("partial native upload published root")
	}
	if plan, err := PlanWorkspaceArchive("owner/repo", "gh-9", "op", data, nil, 1); !errors.Is(err, transferlimit.ErrCapacity) || plan != nil {
		t.Fatal("oversize source accepted", err)
	}
	large := []byte(`{"sha256":"` + strings.Repeat("a", 64) + `","payload":"` + strings.Repeat("x", actionJournalEncodedBudget) + `"}`)
	if plan, err := PlanWorkspaceArchive("owner/repo", "gh-9", "op", large, nil, 0); !errors.Is(err, transferlimit.ErrCapacity) || plan != nil {
		t.Fatal("transport capacity silently exceeded", err)
	}
}
