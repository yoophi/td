package ghstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func multipartJournalFixture(t *testing.T) ActionJournal {
	t.Helper()
	a := journalFixture(t)
	data := make([]byte, 80000)
	_, _ = rand.New(rand.NewSource(67)).Read(data)
	a.Before.Body = base64.StdEncoding.EncodeToString(data)
	a.Before.Revision, _ = a.Before.checksum()
	return a
}

func multipartNativeFixture(t *testing.T, a ActionJournal) (*journalPublication, apiComment, []apiComment) {
	t.Helper()
	plan, err := planActionJournalPublication(a)
	if err != nil || len(plan.partBodies) < 2 {
		t.Fatal("multipart fixture", err)
	}
	root := journalCommentFixture(t, a)
	if a.CarrierID != "" {
		number, _ := Number(a.CarrierID)
		root.IssueURL = fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", number)
	}
	root.ID = 1000
	root.UpdatedAt = root.CreatedAt.Add(time.Minute)
	parts := make([]apiComment, len(plan.partBodies))
	for i, body := range plan.partBodies {
		parts[i] = root
		parts[i].ID, parts[i].Body = int64(100+i), body
		parts[i].UpdatedAt = parts[i].CreatedAt
	}
	root.Body, err = plan.bindParts(parts)
	if err != nil {
		t.Fatal(err)
	}
	return plan, root, parts
}

func TestMultipartJournalRoundTripPreservesNativeManifest(t *testing.T) {
	a := multipartJournalFixture(t)
	_, root, parts := multipartNativeFixture(t, a)
	record, err := decodeMultipartActionJournal(root, "gh-1", parts)
	if err != nil || !reflect.DeepEqual(record.ActionJournal, a) || record.ID != "ghc-1000" || record.Author != root.User.Login || !record.UpdatedAt.Equal(root.UpdatedAt) {
		t.Fatal("lost payload or native identity", err)
	}
	body, err := renderExistingActionJournal(record)
	if err != nil || body != root.Body {
		t.Fatal("native manifest was reconstructed for the receipt fence", err)
	}
	// A previous phase's root may predate all parts uploaded for the next phase.
	root.CreatedAt = root.CreatedAt.Add(-time.Hour)
	if _, err := decodeMultipartActionJournal(root, "gh-1", parts); err != nil {
		t.Fatal("updated manifest cannot use later uploaded parts", err)
	}
	// Decoding with the single-comment API cannot claim a complete observation.
	if _, err := decodeActionJournal(root, "gh-1"); err == nil {
		t.Fatal("manifest accepted without observing its parts")
	}
}

func TestMultipartJournalRejectsIncompleteChangedAndDuplicateParts(t *testing.T) {
	_, root, parts := multipartNativeFixture(t, multipartJournalFixture(t))
	for name, change := range map[string]func(*apiComment, *[]apiComment){
		"missing":      func(_ *apiComment, p *[]apiComment) { *p = (*p)[1:] },
		"duplicate id": func(_ *apiComment, p *[]apiComment) { *p = append(*p, (*p)[0]) },
		"duplicate logical part": func(_ *apiComment, p *[]apiComment) {
			copy := (*p)[0]
			copy.ID = 2000
			*p = append(*p, copy)
		},
		"edited bytes":             func(_ *apiComment, p *[]apiComment) { (*p)[0].Body += "\n" },
		"edited native clock":      func(_ *apiComment, p *[]apiComment) { (*p)[0].UpdatedAt = (*p)[0].UpdatedAt.Add(time.Second) },
		"different native author":  func(_ *apiComment, p *[]apiComment) { (*p)[0].User.Login = "another" },
		"different native carrier": func(_ *apiComment, p *[]apiComment) { (*p)[0].IssueURL += "2" },
		"different native birth": func(_ *apiComment, p *[]apiComment) {
			(*p)[0].CreatedAt = (*p)[0].CreatedAt.Add(-time.Second)
			(*p)[0].UpdatedAt = (*p)[0].CreatedAt
		},
		"part later than manifest": func(r *apiComment, _ *[]apiComment) {
			r.CreatedAt = r.CreatedAt.Add(-time.Hour)
			r.UpdatedAt = r.CreatedAt
		},
		"invalid root identity": func(r *apiComment, _ *[]apiComment) { r.ID = 0 },
		"root author":           func(r *apiComment, _ *[]apiComment) { r.User.Login = "another" },
		"root clock":            func(r *apiComment, _ *[]apiComment) { r.CreatedAt = r.UpdatedAt.Add(time.Second) },
		"different declared carrier": func(r *apiComment, p *[]apiComment) {
			r.IssueURL = "https://api.github.com/repos/owner/repo/issues/2"
			for i := range *p {
				(*p)[i].IssueURL = r.IssueURL
			}
		},
		"non-GitHub owner URL": func(r *apiComment, p *[]apiComment) {
			r.IssueURL = "https://example.com/repos/owner/repo/issues/1"
			for i := range *p {
				(*p)[i].IssueURL = r.IssueURL
			}
		},
		"root summary":       func(r *apiComment, _ *[]apiComment) { r.Body = "tampered\n" + r.Body },
		"root trailing data": func(r *apiComment, _ *[]apiComment) { r.Body += "\n" },
	} {
		t.Run(name, func(t *testing.T) {
			r, p := root, append([]apiComment(nil), parts...)
			change(&r, &p)
			if _, err := decodeMultipartActionJournal(r, "gh-1", p); err == nil {
				t.Fatal("unsafe multipart history accepted")
			}
		})
	}
}

func TestMultipartJournalManifestBoundsAndBindings(t *testing.T) {
	plan, root, parts := multipartNativeFixture(t, multipartJournalFixture(t))
	var manifest actionJournalManifest
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.Split(root.Body, actionMultipartJournalStart)[1], markerEnd)), &manifest); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*actionJournalManifest){
		"unknown schema":      func(m *actionJournalManifest) { m.Envelope.Schema = "v99" },
		"recursive schema":    func(m *actionJournalManifest) { m.Envelope.Schema = "v20" },
		"encoding":            func(m *actionJournalManifest) { m.Envelope.Encoding = "raw" },
		"decoded overflow":    func(m *actionJournalManifest) { m.Envelope.Size = actionDecodedBudget + 1 },
		"encoded overflow":    func(m *actionJournalManifest) { m.EncodedSize = actionJournalEncodedBudget + 1 },
		"negative size":       func(m *actionJournalManifest) { m.EncodedSize = -1 },
		"extra embedded data": func(m *actionJournalManifest) { m.Envelope.Data = "AA==" },
		"wrong packed hash":   func(m *actionJournalManifest) { m.Envelope.SHA256 = strings.Repeat("0", 64) },
		"wrong transfer":      func(m *actionJournalManifest) { m.Transfer = strings.Repeat("0", 64) },
		"wrong part hash":     func(m *actionJournalManifest) { m.Parts[0].SHA256 = strings.Repeat("0", 64) },
		"missing reference":   func(m *actionJournalManifest) { m.Parts = m.Parts[1:] },
		"repeated reference":  func(m *actionJournalManifest) { m.Parts[1] = m.Parts[0] },
		"root as part":        func(m *actionJournalManifest) { m.Parts[0].ID = root.ID },
		"permuted references": func(m *actionJournalManifest) { m.Parts[0], m.Parts[1] = m.Parts[1], m.Parts[0] },
		"other operation":     func(m *actionJournalManifest) { m.OperationID = "td-op-Other" },
		"other carrier":       func(m *actionJournalManifest) { m.CarrierID = "gh-2" },
	} {
		t.Run(name, func(t *testing.T) {
			m, r := manifest, root
			m.Parts = append([]actionJournalPartRef(nil), manifest.Parts...)
			change(&m)
			r.Body, _ = renderJournalManifest(plan.summary, m)
			if _, err := decodeMultipartActionJournal(r, "gh-1", parts); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	root.Body = strings.Replace(root.Body, `"parts":`, `"unknown":true,"parts":`, 1)
	if _, err := decodeMultipartActionJournal(root, "gh-1", parts); err == nil {
		t.Fatal("unknown manifest field accepted")
	}
}

func TestMultipartJournalCannotBindPartialOrUnverifiedUpload(t *testing.T) {
	plan, _, parts := multipartNativeFixture(t, multipartJournalFixture(t))
	if _, err := plan.bindParts(parts[:1]); err == nil {
		t.Fatal("incomplete upload published a manifest")
	}
	parts[1].ID = parts[0].ID
	if _, err := plan.bindParts(parts); err == nil {
		t.Fatal("duplicate native identity published a manifest")
	}
}

func TestMultipartJournalKeepsInlineAndDecodedBounds(t *testing.T) {
	a := journalFixture(t)
	for _, body := range []string{"short", strings.Repeat("compressible ", 20000)} {
		a.Before.Body = body
		a.Before.Revision, _ = a.Before.checksum()
		plan, err := planActionJournalPublication(a)
		if err != nil || len(plan.partBodies) != 0 {
			t.Fatal("unnecessary parts", err)
		}
		expected, _ := renderWriteActionJournal(a)
		got, err := plan.bindParts(nil)
		if err != nil || got != expected {
			t.Fatal("inline bytes changed", err)
		}
	}
	a.Before.Body = strings.Repeat("x", actionDecodedBudget+1)
	a.Before.Revision, _ = a.Before.checksum()
	if _, err := planActionJournalPublication(a); err == nil {
		t.Fatal("multipart must not bypass decoded memory bounds")
	}
}

func TestMultipartJournalPreservesActivityGroupSchema(t *testing.T) {
	a := activityGroupModelFixture(t)
	for i := range a.Group.Members {
		data := make([]byte, 28000)
		_, _ = rand.New(rand.NewSource(int64(67 + i))).Read(data)
		state := &a.Group.Members[i].Before
		state.Body = base64.StdEncoding.EncodeToString(data)
		state.Revision, _ = state.checksum()
	}
	plan, root, parts := multipartNativeFixture(t, a)
	if plan.manifest.Envelope.Schema != "v19" {
		t.Fatal("inner action schema changed")
	}
	record, err := decodeMultipartActionJournal(root, a.CarrierID, parts)
	if err != nil || !reflect.DeepEqual(record.ActionJournal, a) {
		t.Fatal("v19 group identity/input lost", err)
	}
}

func TestMultipartJournalPlansIncompressibleHistory(t *testing.T) {
	plan, err := planActionJournalPublication(multipartJournalFixture(t))
	if err != nil {
		t.Fatalf("large valid history must use multiple bounded comments: %v", err)
	}
	if len(plan.partBodies) < 2 {
		t.Fatal("large history did not produce multipart storage")
	}
	for _, body := range plan.partBodies {
		if len(body) > actionCommentBudget {
			t.Fatal("part exceeds the per-comment publication budget")
		}
	}
}
