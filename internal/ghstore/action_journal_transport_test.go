package ghstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func TestCompressedJournalLegacyRoundTripAndOriginalReceipt(t *testing.T) {
	a := journalFixture(t)
	for _, state := range []*ActionIssueState{a.Before, a.After} {
		state.Body = strings.Repeat("큰 본문 기록\n", 6000)
		state.Revision, _ = state.checksum()
	}
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionCompressedJournalStart) || len(body) > actionCommentBudget {
		t.Fatal(len(body), err)
	}
	c := journalCommentFixture(t, a)
	c.Body = body
	r, err := decodeActionJournal(c, "gh-1")
	if err != nil || !reflect.DeepEqual(r.ActionJournal, a) {
		t.Fatal("round trip", err)
	}
	// An earlier client can use a different gzip level. Preserve its exact
	// original bytes for the receipt fence rather than recompressing history.
	index := strings.Index(body, actionCompressedJournalStart)
	var env actionJournalEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSuffix(body[index+len(actionCompressedJournalStart):], markerEnd)), &env); err != nil {
		t.Fatal(err)
	}
	plain, _ := decodeJournalEnvelope(body[index:])
	raw := strings.TrimSuffix(strings.TrimPrefix(plain, actionJournalStart), markerEnd)
	var buf bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if _, err := writer.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	env.Encoding = "gzip+base64"
	env.Size = len(raw)
	env.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	env.Data = base64.StdEncoding.EncodeToString(buf.Bytes())
	data, _ := json.Marshal(env)
	c.Body = body[:index] + actionCompressedJournalStart + string(data) + markerEnd
	r, err = decodeActionJournal(c, "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	original, err := renderExistingActionJournal(r)
	if err != nil || original != c.Body {
		t.Fatal("receipt was recompressed", err)
	}
	c.Body = "forged summary\n" + c.Body[index:]
	if _, err := decodeActionJournal(c, "gh-1"); err == nil {
		t.Fatal("edited summary accepted")
	}
}

func TestCompressedJournalRejectsCorruptionAndUnboundedInflation(t *testing.T) {
	a := journalFixture(t)
	a.Before.Body = strings.Repeat("x", actionDecodedBudget+1)
	a.Before.Revision, _ = a.Before.checksum()
	if _, err := renderWriteActionJournal(a); err == nil {
		t.Fatal("unbounded writer")
	}
	a.Before.Body = strings.Repeat("한글", 12000)
	a.Before.Revision, _ = a.Before.checksum()
	body, err := renderWriteActionJournal(a)
	if err != nil {
		t.Fatal(err)
	}
	block := body[strings.Index(body, actionCompressedJournalStart):]
	var base actionJournalEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(block, actionCompressedJournalStart), markerEnd)), &base); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*actionJournalEnvelope){
		"unknown schema":   func(e *actionJournalEnvelope) { e.Schema = "v99" },
		"recursive schema": func(e *actionJournalEnvelope) { e.Schema = "v9" },
		"encoding":         func(e *actionJournalEnvelope) { e.Encoding = "raw" },
		"checksum":         func(e *actionJournalEnvelope) { e.SHA256 = strings.Repeat("0", 64) },
		"size":             func(e *actionJournalEnvelope) { e.Size++ },
		"budget":           func(e *actionJournalEnvelope) { e.Size = actionDecodedBudget + 1 },
		"base64":           func(e *actionJournalEnvelope) { e.Data = "!" },
		"gzip":             func(e *actionJournalEnvelope) { e.Data = base64.StdEncoding.EncodeToString([]byte("invalid")) },
		"inflate": func(e *actionJournalEnvelope) {
			var b bytes.Buffer
			w := gzip.NewWriter(&b)
			if _, err := w.Write(bytes.Repeat([]byte("x"), actionDecodedBudget+1)); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			e.Data = base64.StdEncoding.EncodeToString(b.Bytes())
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := base
			change(&e)
			data, _ := json.Marshal(e)
			if _, err := decodeJournalEnvelope(actionCompressedJournalStart + string(data) + markerEnd); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	for _, invalid := range []string{strings.TrimSuffix(block, markerEnd), strings.Replace(block, "\"schema\":", "\"unknown\":1,\"schema\":", 1), strings.TrimSuffix(block, markerEnd) + "{}" + markerEnd} {
		if _, err := decodeJournalEnvelope(invalid); err == nil {
			t.Fatal("invalid framing accepted")
		}
	}
}

func TestLargeForwardRecoveryCompressedWithoutTargetReplay(t *testing.T) {
	f := newJournalWriteFixture(t, "receipt-denied")
	issue := f.issues[1]
	issue.Body = strings.Repeat("큰 원본 데이터\n", 4000)
	f.issues[1] = issue
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	title := "Requested title"
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Title: &title}); err == nil {
		t.Fatal("missing completion failure")
	}
	if !strings.Contains(f.comment.Body, actionCompressedJournalStart) {
		t.Fatal("large intent not compressed")
	}
	source := f.issues[1]
	f.mode = ""
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.Applied || f.targetWrites != 1 || !reflect.DeepEqual(source, f.issues[1]) || result.ForwardOutcome.TargetWriteVerified {
		t.Fatal(result, err)
	}
	if !strings.Contains(f.comment.Body, actionCompressedJournalStart) {
		t.Fatal("large observation not compressed")
	}
	receipt := *f.comment
	result, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.AlreadyReconciled || !reflect.DeepEqual(receipt, *f.comment) || f.targetWrites != 1 {
		t.Fatal("repeat replayed", err)
	}
}

func TestIncompressibleUpdateRejectedBeforeAnyWrite(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	random := rand.New(rand.NewSource(9))
	data := make([]byte, 80000)
	random.Read(data)
	issue := f.issues[1]
	issue.Body = base64.StdEncoding.EncodeToString(data)
	f.issues[1] = issue
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	title := "Requested title"
	_, err := f.client.Update(context.Background(), "gh-1", Changes{Title: &title})
	if err == nil || !strings.Contains(err.Error(), "capacity preflight") || f.targetWrites != 0 || f.intentWrites != 0 || f.carrierWrites != 0 {
		t.Fatal("capacity failure after writes", err, f.targetWrites, f.intentWrites, f.carrierWrites)
	}
}

func TestLargeDistinctTextRecoveryFitsStringArchive(t *testing.T) {
	f := newJournalWriteFixture(t, "receipt-denied")
	random := rand.New(rand.NewSource(10))
	data := make([]byte, 18000)
	random.Read(data)
	issue := f.issues[1]
	issue.Body = base64.StdEncoding.EncodeToString(data) + strings.Repeat("큰 문서 문단입니다.\n", 700)
	f.issues[1] = issue
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	title := "Requested title"
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Title: &title}); err == nil {
		t.Fatal("completion failure absent")
	}
	if !strings.Contains(f.comment.Body, actionStringEncoding) {
		t.Fatal("dictionary transport absent")
	}
	before := f.issues[1]
	f.mode = ""
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.Applied || f.targetWrites != 1 || !reflect.DeepEqual(before, f.issues[1]) {
		t.Fatal(result, err)
	}
}

func TestJournalStringArchivePreservesTokensAndBoundsReferences(t *testing.T) {
	// Opaque body text may itself resemble a dictionary reference.
	token, _ := json.Marshal(strings.Repeat(`body {"$td_string":9} \\" 한국어 `, 100))
	raw := []byte(`{"body":` + string(token) + `,"next":{"body":` + string(token) + `},"labels":["x"]}`)
	packed, err := packJournalStrings(raw)
	if err != nil {
		t.Fatal(err)
	}
	unpacked, err := unpackJournalStrings(packed)
	if err != nil || !bytes.Equal(raw, unpacked) {
		t.Fatal("token order or escaped contents changed", err)
	}
	huge, _ := json.Marshal(strings.Repeat("x", actionDecodedBudget/2+1))
	invalid := []actionStringArchive{
		{Strings: []json.RawMessage{token}, Data: json.RawMessage(`{"body":{"$td_string":1}}`)},
		{Strings: []json.RawMessage{token}, Data: json.RawMessage(`{"body":{"$td_string":-1}}`)},
		{Strings: []json.RawMessage{json.RawMessage(`1`)}, Data: json.RawMessage(`{}`)},
		{Strings: []json.RawMessage{huge}, Data: json.RawMessage(`[{"$td_string":0},{"$td_string":0}]`)},
	}
	for _, archive := range invalid {
		data, _ := json.Marshal(archive)
		if _, err := unpackJournalStrings(data); err == nil {
			t.Fatal("malformed or amplified archive accepted")
		}
	}
	for _, data := range []string{`{"strings":[],"data":{},"unknown":true}`, `{"strings":[],"data":{}} {}`} {
		if _, err := unpackJournalStrings([]byte(data)); err == nil {
			t.Fatal("unknown or trailing archive accepted")
		}
	}
}
