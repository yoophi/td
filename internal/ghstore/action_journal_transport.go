package ghstore

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	actionCompressedJournalStart = actionJournalPrefix + "v9\n"
	// Conservative transport bounds, not a promise of an API maximum.
	actionCommentBudget = 60_000
	actionDecodedBudget = 4 * 1024 * 1024
)

type actionJournalEnvelope struct {
	Schema   string `json:"schema"`
	Encoding string `json:"encoding"`
	Size     int    `json:"size"`
	SHA256   string `json:"sha256"`
	Data     string `json:"data"`
}

func actionSchemaStart(schema string) (string, bool) {
	switch schema {
	case "v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8", "v10", "v11", "v12", "v13", "v14", "v15", "v16", "v17", "v18", "v19", "v21", "v22", "v23", "v24":
		return actionJournalPrefix + schema + "\n", true
	default:
		return "", false
	}
}

// New writes may use a bounded compressed envelope. Keep the plain renderer
// for original legacy bodies, so adding compression cannot rewrite history.
func renderWriteActionJournal(a ActionJournal) (string, error) {
	body, err := renderEncodedActionJournal(a)
	if err == nil && len(body) > actionCommentBudget {
		return "", fmt.Errorf("compressed action journal exceeds the %d-byte single-comment transport budget; multipart transport is required", actionCommentBudget)
	}
	return body, err
}

// The encoded representation may need multiple comments. The legacy writer
// above continues to enforce a single comment until publication is requested
// through a multipart-aware path.
func renderEncodedActionJournal(a ActionJournal) (string, error) {
	body, err := renderActionJournal(a)
	if err != nil || len(body) <= actionCommentBudget {
		return body, err
	}
	index := strings.Index(body, actionJournalPrefix)
	block := body[index:]
	line := strings.IndexByte(block, '\n')
	schema := strings.TrimPrefix(block[:line], actionJournalPrefix)
	raw := []byte(strings.TrimSuffix(block[line+1:], markerEnd))
	if len(raw) > actionDecodedBudget {
		return "", fmt.Errorf("action journal exceeds the decoded transport budget")
	}
	packed, err := packJournalStrings(raw)
	if err != nil {
		return "", err
	}
	if len(packed) > actionDecodedBudget {
		return "", fmt.Errorf("journal string archive exceeds decoded transport budget")
	}
	raw = packed
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	envelope := actionJournalEnvelope{Schema: schema, Encoding: actionStringEncoding, Size: len(raw), SHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), Data: base64.StdEncoding.EncodeToString(compressed.Bytes())}
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	body = body[:index] + actionCompressedJournalStart + string(data) + markerEnd
	return body, nil
}

func decodeJournalEnvelope(block string) (string, error) {
	return decodeJournalEnvelopeBounded(block, actionCommentBudget)
}

func decodeJournalEnvelopeBounded(block string, encodedBudget int) (string, error) {
	if !strings.HasPrefix(block, actionCompressedJournalStart) || !strings.HasSuffix(block, markerEnd) || len(block) > encodedBudget {
		return "", fmt.Errorf("compressed action journal framing or size is invalid")
	}
	data := strings.TrimSuffix(strings.TrimPrefix(block, actionCompressedJournalStart), markerEnd)
	var envelope actionJournalEnvelope
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return "", fmt.Errorf("invalid compressed journal envelope: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("compressed journal envelope has trailing data")
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || string(canonical) != data {
		return "", fmt.Errorf("compressed journal envelope is noncanonical")
	}
	start, known := actionSchemaStart(envelope.Schema)
	if !known || (envelope.Encoding != "gzip+base64" && envelope.Encoding != actionStringEncoding) || envelope.Size <= 0 || envelope.Size > actionDecodedBudget {
		return "", fmt.Errorf("compressed journal schema, encoding or decoded size is invalid")
	}
	compressed, err := base64.StdEncoding.DecodeString(envelope.Data)
	if err != nil {
		return "", fmt.Errorf("invalid journal base64: %w", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", fmt.Errorf("invalid journal gzip: %w", err)
	}
	defer func() { _ = reader.Close() }()
	raw, err := io.ReadAll(io.LimitReader(reader, int64(actionDecodedBudget+1)))
	if err != nil || len(raw) != envelope.Size || len(raw) > actionDecodedBudget || fmt.Sprintf("%x", sha256.Sum256(raw)) != envelope.SHA256 {
		return "", fmt.Errorf("compressed journal data has invalid size, checksum or gzip contents")
	}
	if envelope.Encoding == actionStringEncoding {
		raw, err = unpackJournalStrings(raw)
		if err != nil {
			return "", err
		}
	}
	return start + string(raw) + markerEnd, nil
}

func renderExistingActionJournal(a *ActionJournalRecord) (string, error) {
	if a.compressedBody != "" {
		return a.compressedBody, nil
	}
	return renderActionJournal(a.ActionJournal)
}

// Check both completion and later observation capacity before an update can
// write its target. These are sizing probes, never saved server observations.
func checkUpdateArchiveCapacity(a ActionJournal) error {
	if a.UpdateIntent == nil || a.Before == nil {
		return nil
	}
	state, err := a.UpdateIntent.Target.contents()
	if err != nil {
		return err
	}
	state.UpdatedAt = a.Before.UpdatedAt.Add(time.Second).Truncate(time.Second).Add(999999999 * time.Nanosecond)
	state.Revision, err = state.checksum()
	if err != nil {
		return err
	}
	probe := a
	probe.Phase, probe.After = "applied", &state
	if err := checkJournalSizingProbe(probe); err != nil {
		return err
	}
	// Reserve observer metadata without forging a future native event ID or
	// server clock. A synthetic event is used only by the private sizing probe.
	observation := &ActionUpdateObservation{SessionID: a.SessionID, Actor: strings.Repeat("x", 39), ObservedAt: state.UpdatedAt, Proof: forwardObservationProof, State: &state}
	if a.UpdateIntent.NativeEvents != nil {
		events := append([]nativeStateEvent{}, (*a.UpdateIntent.NativeEvents)...)
		if a.Before.State != a.UpdateIntent.Target.State {
			kind := "closed"
			if a.UpdateIntent.Target.State == "open" {
				kind = "reopened"
			}
			events = append(events, nativeStateEvent{ID: 1<<63 - 1, Event: kind})
		}
		observation.NativeEvents = &events
	}
	probe.UpdateObservation = observation
	return checkJournalSizingProbe(probe)
}

// Reserve transport headroom for the eventual carrier ID, observer session,
// native clocks and compression overhead. This is a private sizing estimate.
func checkJournalSizingProbe(a ActionJournal) error {
	return checkJournalSizingHeadroom(a, 512)
}

func checkJournalSizingHeadroom(a ActionJournal, headroom int) error {
	publication, err := planActionJournalPublication(a)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if headroom < 0 || headroom > actionDecodedBudget || len(raw) > actionDecodedBudget-headroom || publication.manifest.Envelope.Size > actionDecodedBudget-headroom {
		return fmt.Errorf("action journal lacks completion headroom within the decoded transport budget")
	}
	return nil
}

func strictJournalJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid journal string archive: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("journal string archive has trailing data")
	}
	return nil
}
