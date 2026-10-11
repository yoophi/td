// Package storetransfer models explicit, passive store-transfer inputs. It
// never opens a destination or grants imported actors workflow authority.
package storetransfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
)

const FormatVersion = 1
const DefaultMaxBytes int64 = 64 << 20

var ErrCapacity = errors.New("store-transfer input exceeds byte limit")

// Envelope binds the source namespace to the immutable serialized source data.
// SQLite namespaces must be explicitly chosen and retained across retries;
// GitHub namespaces are the repository name. Paths are not stable identities.
// Checksums detect accidental changes, not authenticity or authorization.
type Envelope struct {
	Version   int             `json:"version"`
	Store     string          `json:"store"`
	Namespace string          `json:"namespace"`
	Payload   json.RawMessage `json:"payload"`
	SHA256    string          `json:"sha256"`
}

type Source struct {
	Envelope Envelope
	SQLite   *db.SQLiteWorkspaceExport
	GitHub   *ghstore.WorkspaceExport
}

func NewSQLite(namespace string, source *db.SQLiteWorkspaceExport, maxBytes int64) (*Source, error) {
	if source == nil {
		return nil, errors.New("nil SQLite transfer source")
	}
	return newSource("sqlite", namespace, source, maxBytes)
}

func NewGitHub(source *ghstore.WorkspaceExport, maxBytes int64) (*Source, error) {
	if source == nil {
		return nil, errors.New("nil GitHub transfer source")
	}
	return newSource("gh-issue", source.Repository, source, maxBytes)
}

func newSource(store, namespace string, payload any, maxBytes int64) (*Source, error) {
	limit, err := byteLimit(maxBytes)
	if err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	if err := json.NewEncoder(&boundedWriter{writer: &buffer, remaining: limit}).Encode(payload); err != nil {
		return nil, fmt.Errorf("encode transfer payload: %w", err)
	}
	envelope := Envelope{Version: FormatVersion, Store: store, Namespace: namespace, Payload: bytes.TrimSpace(buffer.Bytes())}
	envelope.SHA256 = envelope.digest()
	return envelope.decode()
}

// Read consumes at most limit+1 bytes. Validation finishes before returning any
// source. Destination planning must additionally validate entity identities,
// references and compatibility before attempting writes.
func Read(reader io.Reader, maxBytes int64) (*Source, error) {
	limit, err := byteLimit(maxBytes)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read transfer input: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w (%d bytes)", ErrCapacity, limit)
	}
	var envelope Envelope
	if err := strictJSON(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode transfer envelope: %w", err)
	}
	return envelope.decode()
}

// Write validates before writing, and pre-encodes within the byte limit so
// capacity errors cannot leave partially serialized output. IO failures can
// still produce partial output; the future file controller must use atomic
// replacement and report the original error.
func (s *Source) Write(writer io.Writer, maxBytes int64) error {
	if s == nil {
		return errors.New("nil transfer source")
	}
	if _, err := s.Envelope.decode(); err != nil {
		return err
	}
	limit, err := byteLimit(maxBytes)
	if err != nil {
		return err
	}
	var buffer bytes.Buffer
	if err := json.NewEncoder(&boundedWriter{writer: &buffer, remaining: limit}).Encode(s.Envelope); err != nil {
		return fmt.Errorf("encode transfer envelope: %w", err)
	}
	_, err = io.Copy(writer, &buffer)
	return err
}

func (e Envelope) decode() (*Source, error) {
	if e.Version != FormatVersion {
		return nil, fmt.Errorf("unsupported store-transfer format version %d", e.Version)
	}
	if e.Namespace == "" || strings.TrimSpace(e.Namespace) != e.Namespace || strings.ContainsAny(e.Namespace, "\r\n\x00") {
		return nil, errors.New("transfer source requires a nonempty stable namespace")
	}
	if len(e.SHA256) != 64 || e.SHA256 != e.digest() {
		return nil, errors.New("transfer source checksum mismatch")
	}
	source := &Source{Envelope: e}
	switch e.Store {
	case "sqlite":
		source.SQLite = new(db.SQLiteWorkspaceExport)
		if err := strictJSON(e.Payload, source.SQLite); err != nil {
			return nil, fmt.Errorf("invalid SQLite transfer payload: %w", err)
		}
		if source.SQLite.SchemaVersion != db.SchemaVersion {
			return nil, fmt.Errorf("unsupported SQLite source schema %d", source.SQLite.SchemaVersion)
		}
	case "gh-issue":
		source.GitHub = new(ghstore.WorkspaceExport)
		if err := strictJSON(e.Payload, source.GitHub); err != nil {
			return nil, fmt.Errorf("invalid GitHub transfer payload: %w", err)
		}
		parts := strings.Split(e.Namespace, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || source.GitHub.Repository != e.Namespace {
			return nil, errors.New("GitHub transfer repository and source namespace must match owner/repo")
		}
	default:
		return nil, fmt.Errorf("unsupported transfer source store %q", e.Store)
	}
	return source, nil
}

func (e Envelope) digest() string {
	hash := sha256.New()
	// Include the source identity and format, not just the payload. The JSON
	// encoder's whitespace normalization does not alter string contents.
	_ = json.NewEncoder(hash).Encode(struct {
		Version   int             `json:"version"`
		Store     string          `json:"store"`
		Namespace string          `json:"namespace"`
		Payload   json.RawMessage `json:"payload"`
	}{e.Version, e.Store, e.Namespace, e.Payload})
	return hex.EncodeToString(hash.Sum(nil))
}

func strictJSON(data []byte, target any) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("null transfer data")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing transfer data")
	}
	return nil
}

func byteLimit(value int64) (int64, error) {
	if value == 0 {
		return DefaultMaxBytes, nil
	}
	if value < 0 || value > 1<<30 {
		return 0, errors.New("transfer byte limit must be between 1 and 1073741824")
	}
	return value, nil
}

type boundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, ErrCapacity
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}
