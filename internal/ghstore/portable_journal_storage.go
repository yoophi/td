package ghstore

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	journalArchivePrefix     = "<!-- td:journal-archive:"
	journalArchivePartPrefix = "<!-- td:journal-archive-part:"
	journalArchivePartBytes  = 48_000
	journalArchiveMaxParts   = 128
)

type journalArchiveIdentity struct {
	BundleSHA256 string `json:"bundle_sha256"`
	Repository   string `json:"repository"`
	CarrierID    string `json:"carrier_id"`
	SessionID    string `json:"session_id"`
}

type journalArchivePart struct {
	journalArchiveIdentity
	Index int    `json:"index"`
	Count int    `json:"count"`
	Data  string `json:"data"`
}

type journalArchiveManifest struct {
	journalArchiveIdentity
	Encoding      string                 `json:"encoding"`
	DecodedSize   int                    `json:"decoded_size"`
	EncodedSize   int                    `json:"encoded_size"`
	EncodedSHA256 string                 `json:"encoded_sha256"`
	Parts         []actionJournalPartRef `json:"parts"`
}

type journalArchive struct {
	identity journalArchiveIdentity
	author   string
	parts    map[int]apiComment
	values   map[int]journalArchivePart
	root     *apiComment
	manifest *journalArchiveManifest
	bundle   *PortableJournalBundle
}

func validPortableHash(hash string) bool {
	data, err := hex.DecodeString(hash)
	return err == nil && len(data) == 32 && hash == strings.ToLower(hash)
}

func (id journalArchiveIdentity) validate() error {
	probe := ActionJournal{OperationID: "archive", SessionID: id.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
	if !validPortableHash(id.BundleSHA256) || !repositoryPattern.MatchString(id.Repository) || !canonicalPortableIssue(id.CarrierID) || probe.validate() != nil {
		return fmt.Errorf("invalid journal archive identity")
	}
	return nil
}

func renderJournalArchive(prefix string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	body := "Preserved td history. Source IDs, authors and events are archival data; this is not an executable undo or review receipt.\n\n" + prefix + "v1\n" + string(data) + markerEnd
	if len(body) > actionCommentBudget {
		return "", fmt.Errorf("journal archive exceeds the comment capacity")
	}
	return body, nil
}

func parseJournalArchive(body, prefix string, value any) error {
	start := strings.Index(body, prefix)
	if start < 0 || len(body) > actionCommentBudget || strings.Count(body, prefix) != 1 {
		return fmt.Errorf("invalid journal archive framing or size")
	}
	block := body[start:]
	if !strings.HasPrefix(block, prefix+"v1\n") || !strings.HasSuffix(block, markerEnd) {
		return fmt.Errorf("unsupported journal archive version or framing")
	}
	if err := decodeImportJSON([]byte(strings.TrimSuffix(strings.TrimPrefix(block, prefix+"v1\n"), markerEnd)), value); err != nil {
		return err
	}
	canonical, err := renderJournalArchive(prefix, value)
	if err != nil || canonical != body {
		return fmt.Errorf("noncanonical journal archive content")
	}
	return nil
}

func encodePortableJournal(bundle *PortableJournalBundle) (string, int, error) {
	data, err := MarshalPortableJournal(bundle)
	if err != nil {
		return "", 0, err
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(data); err != nil {
		return "", 0, err
	}
	if err := writer.Close(); err != nil {
		return "", 0, err
	}
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())
	if len(data) > maxPortableJournalBytes || len(encoded) > journalArchivePartBytes*journalArchiveMaxParts {
		return "", 0, fmt.Errorf("journal archive exceeds capacity (64 MiB decoded, 128 parts of 48000 encoded bytes); no destination write attempted")
	}
	return encoded, len(data), nil
}

// Collection reads validate immutable native comments once, without per-part
// GETs. Unfinished parts remain data and cannot become active action journals.
func (s *Snapshot) journalArchives(repository string) (map[string]*journalArchive, error) {
	if !s.fullHistory {
		return nil, fmt.Errorf("journal archive read requires complete history")
	}
	archives := map[string]*journalArchive{}
	for _, entry := range s.entries {
		for _, comment := range entry.Comments {
			partMarker := strings.Contains(comment.Body, journalArchivePartPrefix)
			rootMarker := strings.Contains(comment.Body, journalArchivePrefix)
			if !partMarker && !rootMarker {
				continue
			}
			record, err := entry.Issue.record()
			if err != nil || record.meta.EntityKind != "journal" || partMarker == rootMarker || !validJournalPartNative(comment) || !journalNativeCarrierMatches(comment, record.ID) {
				return nil, fmt.Errorf("journal archive comment %d has invalid carrier, identity or edited content", comment.ID)
			}
			var id journalArchiveIdentity
			var part journalArchivePart
			var manifest journalArchiveManifest
			if partMarker {
				if err := parseJournalArchive(comment.Body, journalArchivePartPrefix, &part); err != nil {
					return nil, err
				}
				id = part.journalArchiveIdentity
				if part.Count < 1 || part.Count > journalArchiveMaxParts || part.Index < 0 || part.Index >= part.Count || len(part.Data) < 1 || len(part.Data) > journalArchivePartBytes || (part.Index < part.Count-1 && len(part.Data) != journalArchivePartBytes) {
					return nil, fmt.Errorf("invalid journal archive part index, count or size")
				}
			} else {
				if err := parseJournalArchive(comment.Body, journalArchivePrefix, &manifest); err != nil {
					return nil, err
				}
				id = manifest.journalArchiveIdentity
				if manifest.Encoding != "gzip+base64" || manifest.DecodedSize < 1 || manifest.DecodedSize > maxPortableJournalBytes || manifest.EncodedSize < 1 || manifest.EncodedSize > journalArchivePartBytes*journalArchiveMaxParts || !validPortableHash(manifest.EncodedSHA256) || len(manifest.Parts) != (manifest.EncodedSize+journalArchivePartBytes-1)/journalArchivePartBytes {
					return nil, fmt.Errorf("invalid journal archive manifest encoding, size or references")
				}
			}
			if err := id.validate(); err != nil || !strings.EqualFold(id.Repository, repository) || id.CarrierID != record.ID {
				return nil, fmt.Errorf("journal archive comment %d has mismatched destination identity", comment.ID)
			}
			archive, found := archives[id.BundleSHA256]
			if !found {
				archive = &journalArchive{identity: id, author: comment.User.Login, parts: map[int]apiComment{}, values: map[int]journalArchivePart{}}
				archives[id.BundleSHA256] = archive
			}
			if archive.identity != id || !strings.EqualFold(archive.author, comment.User.Login) {
				return nil, fmt.Errorf("journal archive %s has conflicting writer or carrier identities", id.BundleSHA256)
			}
			if partMarker {
				if _, duplicate := archive.parts[part.Index]; duplicate {
					return nil, fmt.Errorf("journal archive %s has duplicate part %d; inspect before retrying", id.BundleSHA256, part.Index)
				}
				archive.parts[part.Index], archive.values[part.Index] = comment, part
			} else {
				if archive.root != nil {
					return nil, fmt.Errorf("journal archive %s has duplicate manifests; inspect before retrying", id.BundleSHA256)
				}
				archive.root, archive.manifest = &comment, &manifest
			}
		}
	}
	for _, archive := range archives {
		if archive.manifest != nil {
			if err := archive.decode(); err != nil {
				return nil, err
			}
		}
	}
	return archives, nil
}

func (a *journalArchive) decode() error {
	m := a.manifest
	if len(a.parts) != len(m.Parts) {
		return fmt.Errorf("journal archive %s has missing or extra parts", m.BundleSHA256)
	}
	var joined strings.Builder
	for index, ref := range m.Parts {
		comment, found := a.parts[index]
		part := a.values[index]
		if !found || comment.ID != ref.ID || !comment.CreatedAt.Equal(ref.CreatedAt) || comment.CreatedAt.After(a.root.CreatedAt) || journalTransportHash(comment.Body) != ref.SHA256 || part.Count != len(m.Parts) {
			return fmt.Errorf("journal archive %s part %d differs from native manifest reference", m.BundleSHA256, index)
		}
		joined.WriteString(part.Data)
	}
	encoded := joined.String()
	if len(encoded) != m.EncodedSize || journalTransportHash(encoded) != m.EncodedSHA256 {
		return fmt.Errorf("journal archive encoded checksum or size mismatch")
	}
	compressed, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return err
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, int64(m.DecodedSize)+1))
	if err != nil || len(data) != m.DecodedSize {
		return fmt.Errorf("journal archive decoded data or size invalid: %v", err)
	}
	bundle, err := ParsePortableJournal(data)
	if err != nil {
		return err
	}
	if bundle.SHA256 != m.BundleSHA256 {
		return fmt.Errorf("journal archive bundle differs from manifest checksum")
	}
	a.bundle = bundle
	return nil
}
