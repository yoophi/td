package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestImportDestinationCaptureResumesLargeBackupsWithoutRecapture(t *testing.T) {
	for _, lost := range []string{"capture", "first_backup", "middle_backup", "source"} {
		t.Run(lost, func(t *testing.T) {
			c, issues, writes, comments := importBatchFixture(t)
			originalTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			issues[1]["created_at"], issues[1]["updated_at"] = originalTime, originalTime
			for i := 1; i <= 4; i++ {
				*comments = append(*comments, apiComment{ID: int64(i), Body: strings.Repeat(fmt.Sprintf("history-%d ", i), 4000), IssueURL: "https://api.github.com/repos/owner/repo/issues/1", CreatedAt: originalTime, UpdatedAt: originalTime})
			}
			record, err := c.Get(context.Background(), "gh-1")
			if err != nil {
				t.Fatal(err)
			}
			source := ImportItem{Issue: record.Issue, GitHub: &ExportedIssue{Version: 1, Repository: c.repo, Record: *record, NativeState: "open", Activity: []models.Activity{}}}
			source.Issue.Title, source.GitHub.Record.Title = "Restored title", "Restored title"
			base := c.run
			failure := errors.New("saved capture/backup response lost")
			failed := false
			backupPosts := 0
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				data, err := base(ctx, dir, payload, args...)
				if err != nil || !slices.Contains(args, "POST") || !strings.HasSuffix(args[5], "/comments") {
					return data, err
				}
				issues[1]["updated_at"] = originalTime.Add(time.Duration(len(*comments)) * time.Minute)
				var fields map[string]string
				if err := json.Unmarshal(payload, &fields); err != nil {
					t.Fatal(err)
				}
				part, err := decodeImportProvenance(fields["body"])
				if err != nil {
					t.Fatal(err)
				}
				if part != nil && part.Kind == "destination" {
					backupPosts++
				}
				match := lost == "capture" && strings.Contains(fields["body"], importCapturePrefix) ||
					lost == "first_backup" && part != nil && part.Kind == "destination" && backupPosts == 1 ||
					lost == "middle_backup" && part != nil && part.Kind == "destination" && backupPosts == 2 ||
					lost == "source" && part != nil && part.Kind == "source"
				if !failed && match {
					failed = true
					return nil, failure
				}
				return data, nil
			}
			if _, err := c.executeImport(context.Background(), []ImportItem{source}, true); !errors.Is(err, failure) || !failed {
				t.Fatal("failure not preserved", err, failed)
			}
			plan, err := c.PlanImport(context.Background(), []ImportItem{source}, true)
			if err != nil || plan.Rows[0].ResumeState != "before_backup" {
				t.Fatal(plan, err)
			}
			changed := source
			githubCopy := *source.GitHub
			changed.GitHub = &githubCopy
			changed.Issue.Title, changed.GitHub.Record.Title = "Other version", "Other version"
			if _, err := c.PlanImport(context.Background(), []ImportItem{changed}, true); err == nil {
				t.Fatal("pending backup accepted changed input")
			}
			result, err := c.executeImport(context.Background(), []ImportItem{source}, true)
			if err != nil || result.Completed != 1 || *writes != 1 {
				t.Fatal(result, err, *writes)
			}
			captureCount := 0
			var capture *importCapture
			inventory := importProvenanceInventory{}
			for _, comment := range *comments {
				got, err := decodeImportCapture(comment.Body)
				if err != nil {
					t.Fatal(err)
				}
				if got != nil {
					captureCount++
					capture = got
				}
				part, err := decodeImportProvenance(comment.Body)
				if err != nil {
					t.Fatal(err)
				}
				if part != nil {
					addImportParts(inventory, []importProvenancePart{*part})
				}
			}
			if captureCount != 1 || capture == nil || backupPosts != capture.Parts || capture.Parts < 3 {
				t.Fatal("backup was recaptured/reposted", captureCount, capture, backupPosts)
			}
			ref := importProvenancePart{SourceKey: capture.SourceKey, SourceDigest: capture.SourceDigest, TargetID: capture.TargetID, Kind: "destination", ContentDigest: capture.ArchiveDigest}
			bytes, err := reassembleImportProvenance(inventory[ref.key()])
			if err != nil {
				t.Fatal(err)
			}
			var archive importDestinationArchive
			if err := json.Unmarshal(bytes, &archive); err != nil || len(archive.Comments) != 4 || !archive.Issue.UpdatedAt.Equal(originalTime) {
				t.Fatal("original clocks/comments not retained", archive.Issue.UpdatedAt, len(archive.Comments), err)
			}
		})
	}
}

func TestImportCaptureRejectsChangedOriginalCommentsAndEditedDescriptor(t *testing.T) {
	source := importTestItem("td-source")
	digest, _ := source.Digest()
	native := apiIssue{Number: 1, State: "open", Title: "Original"}
	basis, _ := importTargetBasis(native)
	row := ImportPlanRow{SourceKey: source.SourceKey(), Digest: digest, TargetID: "gh-1", basis: basis}
	entry := snapshotEntry{Issue: native, Comments: []apiComment{{ID: 10, Body: "Original comment"}}}
	capture, _, err := newImportCapture("owner/repo", row, entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.Comments[0].Body = "Edited comment"
	if _, err := recoverImportDestinationCapture(capture, "owner/repo", row, entry); err == nil || !strings.Contains(err.Error(), "digest changed") {
		t.Fatal("modified original comments accepted", err)
	}
	entry.Comments = nil
	if _, err := recoverImportDestinationCapture(capture, "owner/repo", row, entry); err == nil {
		t.Fatal("deleted original comments accepted")
	}
	body, err := renderImportCapture(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, altered := range []string{body + " edited", strings.Replace(body, `"version":1`, `"version":1,"unknown":true`, 1), strings.Replace(body, "capture:v1", "capture:v2", 1)} {
		if _, err := decodeImportCapture(altered); err == nil {
			t.Fatal("edited capture accepted")
		}
	}
	activity, err := decodeActivity(apiComment{ID: 20, Body: body}, "gh-1")
	if err != nil || !activity.Native || activity.Kind != "comment" || activity.SessionID != "" {
		t.Fatal("capture granted workflow authority", activity, err)
	}
}
