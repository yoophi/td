package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func portableJournalFixture(t *testing.T) *PortableJournalBundle {
	t.Helper()
	comment := journalCommentFixture(t, journalFixture(t))
	comment.UpdatedAt = comment.CreatedAt
	action, err := decodeActionJournal(comment, "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := newPortableJournal("owner/repo", []ActionJournalRecord{*action})
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestPortableJournalPreservesSourceIdentityAndSeparatesMappings(t *testing.T) {
	bundle := portableJournalFixture(t)
	original, err := json.Marshal(bundle.Sources[0])
	if err != nil {
		t.Fatal(err)
	}
	bundle.Bindings = append(bundle.Bindings, PortableJournalBinding{SourceSHA256: bundle.Sources[0].SHA256, TargetRepository: "other/project", Targets: map[string]string{"gh-1": "gh-92"}})
	bundle.SHA256, err = bundle.checksum()
	if err != nil {
		t.Fatal(err)
	}
	data, err := MarshalPortableJournal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePortableJournal(data)
	if err != nil || !equalPortableJournal(bundle, parsed) {
		t.Fatal(parsed, err)
	}
	after, _ := json.Marshal(parsed.Sources[0])
	if string(after) != string(original) || parsed.Sources[0].Actions[0].IssueID != "gh-1" || parsed.Bindings[0].Targets["gh-1"] != "gh-92" {
		t.Fatal("mapping rewrote source history")
	}
	action := parsed.Sources[0].Actions[0]
	if action.Author != bundle.Sources[0].Actions[0].Author || action.SessionID != "ses_actor" || action.Before.Revision == "" || action.After.Revision == "" {
		t.Fatal("source attribution or checksums lost", action)
	}
}

func TestPortableJournalRejectsCorruptionAndInvalidReferences(t *testing.T) {
	for _, mode := range []string{"bundle-hash", "source-hash", "state-hash", "native-id", "url", "duplicate-id", "version", "unknown-field", "unknown-source", "unknown-reference", "target-id", "duplicate-binding"} {
		t.Run(mode, func(t *testing.T) {
			bundle := portableJournalFixture(t)
			source := &bundle.Sources[0]
			bundle.Bindings = []PortableJournalBinding{{SourceSHA256: source.SHA256, TargetRepository: "other/repo", Targets: map[string]string{"gh-1": "gh-2"}}}
			switch mode {
			case "bundle-hash":
				bundle.SHA256 = "invalid"
			case "source-hash":
				source.SHA256 = "invalid"
			case "state-hash":
				source.Actions[0].Before.Title = "Changed without original checksum"
			case "native-id":
				source.Actions[0].ID = "ghc-01"
			case "url":
				source.Actions[0].URL = "https://github.com/another/repo/issues/1#issuecomment-10"
			case "duplicate-id":
				source.Actions = append(source.Actions, source.Actions[0])
			case "version":
				bundle.Version = 2
			case "unknown-source":
				bundle.Bindings[0].SourceSHA256 = strings.Repeat("f", 64)
			case "unknown-reference":
				bundle.Bindings[0].Targets = map[string]string{"gh-999": "gh-2"}
			case "target-id":
				bundle.Bindings[0].Targets["gh-1"] = "2"
			case "duplicate-binding":
				bundle.Bindings = append(bundle.Bindings, bundle.Bindings[0])
			}
			if mode != "source-hash" {
				source.SHA256, _ = source.checksum()
				if mode != "unknown-source" {
					bundle.Bindings[0].SourceSHA256 = source.SHA256
				}
			}
			if mode != "bundle-hash" {
				bundle.SHA256, _ = bundle.checksum()
			}
			data, _ := json.Marshal(bundle)
			if mode == "unknown-field" {
				data = append([]byte(`{"unsupported":true,`), data[1:]...)
			}
			if _, err := ParsePortableJournal(data); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
}

func TestPortableJournalExportUsesBulkCollectionsAndPreservesUncertainActions(t *testing.T) {
	issues := [][]apiIssue{{{Number: 1, Title: "Target", State: "closed"}}}
	comments := [][]apiComment{{}}
	for n := 1; n <= 60; n++ {
		a := journalFixture(t)
		a.OperationID = fmt.Sprintf("export-op-%d", n)
		if n%2 == 0 {
			a.Phase, a.After = "prepared", nil
		}
		comment := journalCommentFixture(t, a)
		comment.UpdatedAt = comment.CreatedAt
		comment.ID = int64(n)
		comment.URL = fmt.Sprintf("https://github.com/owner/repo/issues/1#issuecomment-%d", n)
		comment.IssueURL = "https://api.github.com/repos/owner/repo/issues/1"
		comments[0] = append(comments[0], comment)
	}
	client, calls := snapshotFixture(issues, comments, nil)
	bundle, err := client.ExportJournal(context.Background())
	if err != nil || len(*calls) != 2 || len(bundle.Sources[0].Actions) != 60 {
		t.Fatal("journal export failed or made non-bulk requests", err, *calls)
	}
	prepared := 0
	for _, action := range bundle.Sources[0].Actions {
		if action.Phase == "prepared" {
			prepared++
			if action.After != nil {
				t.Fatal("uncertain result became success")
			}
		}
	}
	if prepared != 30 {
		t.Fatal("uncertain actions omitted", prepared)
	}
}
