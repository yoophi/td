package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

type JournalRestoreMapping struct {
	SourceKey string `json:"source_key"`
	TargetID  string `json:"target_id"`
}

type JournalRestorePlan struct {
	Repository      string                  `json:"repository"`
	BundleSHA256    string                  `json:"bundle_sha256"`
	DryRun          bool                    `json:"dry_run"`
	Sources         int                     `json:"sources"`
	Actions         int                     `json:"actions"`
	Parts           int                     `json:"parts"`
	PartsPresent    int                     `json:"parts_present"`
	AlreadyRestored bool                    `json:"already_restored"`
	ArchiveID       string                  `json:"archive_id,omitempty"`
	ResumeSession   string                  `json:"resume_session,omitempty"`
	Mappings        []JournalRestoreMapping `json:"mappings"`
	Unmapped        []string                `json:"unmapped"`
	Warnings        []string                `json:"warnings"`
}

type JournalRestoreResult struct {
	Plan            JournalRestorePlan `json:"plan"`
	ArchiveID       string             `json:"archive_id"`
	WrittenParts    int                `json:"written_parts"`
	AlreadyRestored bool               `json:"already_restored"`
}

type journalRestorePlan struct {
	public      JournalRestorePlan
	bundle      *PortableJournalBundle
	encoded     string
	decodedSize int
	archive     *journalArchive
}

func (c *Client) PlanJournalRestore(ctx context.Context, bundle *PortableJournalBundle) (*JournalRestorePlan, error) {
	plan, err := c.planJournalRestore(ctx, bundle)
	if err != nil {
		return nil, err
	}
	plan.public.DryRun = true
	return &plan.public, nil
}

func (c *Client) planJournalRestore(ctx context.Context, bundle *PortableJournalBundle) (*journalRestorePlan, error) {
	if bundle == nil {
		return nil, fmt.Errorf("portable journal is required")
	}
	encoded, size, err := encodePortableJournal(bundle)
	if err != nil {
		return nil, err
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	archives, err := snapshot.journalArchives(c.repo)
	if err != nil {
		return nil, err
	}
	all := []*PortableJournalBundle{bundle}
	for _, archive := range archives {
		if archive.bundle != nil {
			all = append(all, archive.bundle)
		}
	}
	if _, err := mergePortableJournals(all...); err != nil {
		return nil, err
	}
	plan := &journalRestorePlan{bundle: bundle, encoded: encoded, decodedSize: size, archive: archives[bundle.SHA256], public: JournalRestorePlan{
		Repository: c.repo, BundleSHA256: bundle.SHA256, Sources: len(bundle.Sources), Parts: (len(encoded) + journalArchivePartBytes - 1) / journalArchivePartBytes,
		Mappings: []JournalRestoreMapping{}, Unmapped: []string{}, Warnings: []string{
			"Restored history is archival data: no source command, native event, review authority or undo authority is recreated. Destination-native actions keep their normal undo rules.",
			"Checksums detect corruption; they do not authenticate source authors. Original IDs, authors, sessions, clocks and snapshots remain source-qualified.",
			"Mappings annotate structured references only. Unmapped IDs and embedded text/JSON remain source-qualified; matching numbers are never assumed to be the same issue.",
		},
	}}
	targets := map[string]bool{}
	carriers := 0
	for _, record := range snapshot.records {
		targets[record.ID] = true
		if record.meta.EntityKind == "journal" {
			carriers++
		}
	}
	if carriers > 1 {
		return nil, fmt.Errorf("multiple td journal carriers exist; inspect GitHub before restoring history; no write attempted")
	}
	if carriers == 0 {
		plan.public.Warnings = append(plan.public.Warnings, "Restoration will create the shared td journal carrier before storing archive comments.")
	}
	mapped, unmapped := map[string]string{}, map[string]bool{}
	for _, source := range bundle.Sources {
		plan.public.Actions += len(source.Actions)
		refs, err := source.references()
		if err != nil {
			return nil, err
		}
		for ref := range refs {
			unmapped[journalSourceKey(source.Repository, ref)] = true
		}
		for _, binding := range bundle.Bindings {
			if binding.SourceSHA256 != source.SHA256 || !strings.EqualFold(binding.TargetRepository, c.repo) {
				continue
			}
			for ref, target := range binding.Targets {
				if !targets[target] {
					return nil, fmt.Errorf("journal mapping target %s is absent from %s; no archive write attempted", target, c.repo)
				}
				key := journalSourceKey(source.Repository, ref)
				if old, exists := mapped[key]; exists && old != target {
					return nil, fmt.Errorf("journal source snapshots have conflicting mapping for %s", key)
				}
				mapped[key] = target
			}
		}
	}
	for key, target := range mapped {
		plan.public.Mappings = append(plan.public.Mappings, JournalRestoreMapping{SourceKey: key, TargetID: target})
		delete(unmapped, key)
	}
	for key := range unmapped {
		plan.public.Unmapped = append(plan.public.Unmapped, key)
	}
	sort.Slice(plan.public.Mappings, func(i, j int) bool { return plan.public.Mappings[i].SourceKey < plan.public.Mappings[j].SourceKey })
	sort.Strings(plan.public.Unmapped)
	if a := plan.archive; a != nil {
		plan.public.PartsPresent, plan.public.ResumeSession = len(a.parts), a.identity.SessionID
		for index, part := range a.values {
			if part.Count != plan.public.Parts || index >= plan.public.Parts || part.Data != plan.chunk(index) {
				return nil, fmt.Errorf("existing journal archive part differs from input; inspect %s before retrying", bundle.SHA256)
			}
		}
		if a.bundle != nil {
			if !equalPortableJournal(a.bundle, bundle) {
				return nil, fmt.Errorf("existing journal archive differs from input")
			}
			plan.public.AlreadyRestored = true
			plan.public.ArchiveID = fmt.Sprintf("ghc-%d", a.root.ID)
		}
	}
	return plan, nil
}

func (p *journalRestorePlan) chunk(index int) string {
	return p.encoded[index*journalArchivePartBytes : min((index+1)*journalArchivePartBytes, len(p.encoded))]
}

// RestoreJournal only adds passive archive comments. It never applies imported
// snapshots to destination issues, creates source events or grants review/undo.
// A later explicit invocation can reuse verified parts from the same real
// session/author, but an uncertain POST is never automatically repeated.
func (c *Client) RestoreJournal(ctx context.Context, bundle *PortableJournalBundle) (result *JournalRestoreResult, err error) {
	plan, err := c.planJournalRestore(ctx, bundle)
	if err != nil {
		return nil, err
	}
	result = &JournalRestoreResult{Plan: plan.public, ArchiveID: plan.public.ArchiveID, AlreadyRestored: plan.public.AlreadyRestored}
	if result.AlreadyRestored {
		return result, nil
	}
	if c.journal == nil {
		return nil, fmt.Errorf("journal restore requires the actual destination session")
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var actor struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(profile, &actor); err != nil || actor.Login == "" {
		return nil, fmt.Errorf("journal restore GitHub author is unverified")
	}
	if a := plan.archive; a != nil && (a.identity.SessionID != c.journal.SessionID || !strings.EqualFold(a.author, actor.Login)) {
		return nil, fmt.Errorf("unfinished journal archive belongs to session %s / author %s; inspect it before proceeding; no write attempted", a.identity.SessionID, a.author)
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("journal restore %s incomplete or uncertain (%d part writes verified in this invocation); archive comments/carrier may remain; no source action was executed and no automatic retry was attempted; inspect GitHub before rerunning the same input: %w", bundle.SHA256, result.WrittenParts, err)
		}
	}()
	if err = c.EnableActionJournal(ctx, c.journal.SessionID); err != nil {
		return result, err
	}
	// Bootstrap or a concurrent writer may have changed the archive collection.
	plan, err = c.planJournalRestore(ctx, bundle)
	if err != nil {
		return result, err
	}
	if plan.public.AlreadyRestored {
		result.Plan, result.ArchiveID, result.AlreadyRestored = plan.public, plan.public.ArchiveID, true
		return result, nil
	}
	id := journalArchiveIdentity{BundleSHA256: bundle.SHA256, Repository: c.repo, CarrierID: c.journal.Carrier.ID, SessionID: c.journal.SessionID}
	// Large archives can otherwise issue dozens of consecutive comment POSTs.
	// Pace this invocation and honor cancellation; never retry a failed write.
	nextWrite := time.Time{}
	post := func(body string) (*apiComment, error) {
		if delay := time.Until(nextWrite); delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		nextWrite = time.Now().Add(time.Second)
		return c.postJournalArchive(ctx, body, actor.Login)
	}
	observed := map[int]apiComment{}
	if a := plan.archive; a != nil {
		if a.identity != id || !strings.EqualFold(a.author, actor.Login) {
			return result, fmt.Errorf("journal archive writer identity changed")
		}
		for index, part := range a.parts {
			observed[index] = part
		}
	}
	for index := 0; index < plan.public.Parts; index++ {
		if _, exists := observed[index]; exists {
			continue
		}
		part := journalArchivePart{journalArchiveIdentity: id, Index: index, Count: plan.public.Parts, Data: plan.chunk(index)}
		body, renderErr := renderJournalArchive(journalArchivePartPrefix, part)
		if renderErr != nil {
			return result, renderErr
		}
		comment, postErr := post(body)
		if postErr != nil {
			return result, postErr
		}
		observed[index] = *comment
		result.WrittenParts++
	}
	// Verify every part from one fresh collection before publishing the manifest.
	verified, err := c.planJournalRestore(ctx, bundle)
	if err != nil {
		return result, err
	}
	if verified.archive == nil || len(verified.archive.parts) != len(observed) {
		return result, fmt.Errorf("journal archive part collection changed before manifest")
	}
	for index, comment := range observed {
		if !reflect.DeepEqual(comment, verified.archive.parts[index]) {
			return result, fmt.Errorf("journal archive part %d changed before manifest", index)
		}
	}
	if verified.public.AlreadyRestored {
		result.Plan, result.ArchiveID, result.AlreadyRestored = verified.public, verified.public.ArchiveID, true
		return result, nil
	}
	manifest := journalArchiveManifest{journalArchiveIdentity: id, Encoding: "gzip+base64", DecodedSize: plan.decodedSize, EncodedSize: len(plan.encoded), EncodedSHA256: journalTransportHash(plan.encoded), Parts: make([]actionJournalPartRef, plan.public.Parts)}
	for index, comment := range observed {
		manifest.Parts[index] = actionJournalPartRef{ID: comment.ID, CreatedAt: comment.CreatedAt, SHA256: journalTransportHash(comment.Body)}
	}
	body, err := renderJournalArchive(journalArchivePrefix, manifest)
	if err != nil {
		return result, err
	}
	root, err := post(body)
	if err != nil {
		return result, err
	}
	final, err := c.planJournalRestore(ctx, bundle)
	if err != nil {
		return result, err
	}
	if !final.public.AlreadyRestored || !reflect.DeepEqual(*root, *final.archive.root) {
		return result, fmt.Errorf("journal archive manifest readback is unverified")
	}
	result.ArchiveID = final.public.ArchiveID
	return result, nil
}

func (c *Client) postJournalArchive(ctx context.Context, body, author string) (*apiComment, error) {
	data, err := c.request(ctx, "POST", fmt.Sprintf("/issues/%d/comments", c.journal.Carrier.Number), map[string]string{"body": body}, false)
	if err != nil {
		return nil, err
	}
	var comment apiComment
	if err := json.Unmarshal(data, &comment); err != nil {
		return nil, err
	}
	number, err := c.commentIssueNumber(comment.IssueURL)
	if err != nil || number != c.journal.Carrier.Number || !validJournalPartNative(comment) || comment.Body != body || !strings.EqualFold(comment.User.Login, author) {
		return nil, fmt.Errorf("journal archive POST response identity/content is unverified; inspect native comment %d", comment.ID)
	}
	return &comment, nil
}
