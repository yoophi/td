package storetransfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
)

func TestEnvelopeRoundTripAndSourceBinding(t *testing.T) {
	sqlite, err := NewSQLite("project-stable-id", &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion}, 0)
	if err != nil {
		t.Fatal(err)
	}
	github, err := NewGitHub(&ghstore.WorkspaceExport{Repository: "owner/repo"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []*Source{sqlite, github} {
		var output bytes.Buffer
		if err := source.Write(&output, 0); err != nil {
			t.Fatal(err)
		}
		restored, err := Read(bytes.NewReader(output.Bytes()), int64(output.Len()))
		if err != nil {
			t.Fatal(err)
		}
		if restored.Envelope.SHA256 != source.Envelope.SHA256 || restored.Envelope.Namespace != source.Envelope.Namespace {
			t.Fatal("source identity changed")
		}
		if (restored.SQLite != nil) != (source.SQLite != nil) || (restored.GitHub != nil) != (source.GitHub != nil) {
			t.Fatal("wrong source decoded")
		}
		changed := source.Envelope
		changed.Namespace = "different/source"
		if _, err := changed.decode(); err == nil {
			t.Fatal("identity change did not invalidate checksum")
		}
	}
}

func TestEnvelopeRejectsInvalidInputWithoutPartialSource(t *testing.T) {
	source, err := NewSQLite("source", &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := source.Write(&encoded, 0); err != nil {
		t.Fatal(err)
	}
	valid := encoded.String()
	for name, input := range map[string]string{
		"null": "null", "truncated": valid[:len(valid)/2], "extra_document": valid + "{}",
		"unknown_envelope_field": strings.Replace(valid, `"version":1`, `"version":1,"unexpected":true`, 1),
		"version":                strings.Replace(valid, `"version":1`, `"version":2`, 1),
		"payload_changed":        strings.Replace(valid, `"schema_version":38`, `"schema_version":0`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := Read(strings.NewReader(input), 0)
			if err == nil || result != nil {
				t.Fatalf("accepted invalid data: %v", err)
			}
		})
	}
	for _, test := range []struct{ store, namespace, payload string }{
		{"sqlite", "", `{"schema_version":38}`},
		{"unknown", "source", `{}`},
		{"sqlite", "source", `null`},
		{"sqlite", "source", `{"schema_version":0}`},
		{"sqlite", "source", `{"schema_version":38,"unexpected":1}`},
		{"gh-issue", "owner/repo", `{"repository":"other/repo"}`},
	} {
		envelope := Envelope{Version: FormatVersion, Store: test.store, Namespace: test.namespace, Payload: json.RawMessage(test.payload)}
		envelope.SHA256 = envelope.digest()
		if result, err := envelope.decode(); err == nil || result != nil {
			t.Fatalf("accepted %+v", test)
		}
	}
}

func TestEnvelopeCapacityAndIOFailures(t *testing.T) {
	source, err := NewSQLite("source", &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := source.Write(&output, 10); !errors.Is(err, ErrCapacity) || output.Len() != 0 {
		t.Fatalf("capacity wrote partial output: %v", err)
	}
	reader := &countedReader{remaining: 10000}
	if result, err := Read(reader, 10); !errors.Is(err, ErrCapacity) || result != nil || reader.read != 11 {
		t.Fatalf("unbounded or accepted read: %d %v", reader.read, err)
	}
	if _, err := NewSQLite("source", &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion}, 10); !errors.Is(err, ErrCapacity) {
		t.Fatalf("payload cap: %v", err)
	}
	cause := errors.New("original disk error")
	if _, err := Read(failedIO{cause}, 0); !errors.Is(err, cause) {
		t.Fatalf("lost read cause: %v", err)
	}
	if err := source.Write(failedIO{cause}, 0); !errors.Is(err, cause) {
		t.Fatalf("lost write cause: %v", err)
	}
	if _, err := Read(strings.NewReader("{}"), -1); err == nil {
		t.Fatal("negative byte limit accepted")
	}
}

type failedIO struct{ err error }

func (f failedIO) Read([]byte) (int, error)  { return 0, f.err }
func (f failedIO) Write([]byte) (int, error) { return 0, f.err }

type countedReader struct{ remaining, read int }

func (r *countedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.remaining)
	for i := range n {
		p[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}
