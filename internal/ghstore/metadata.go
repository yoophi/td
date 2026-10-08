package ghstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/marcus/td/internal/models"
)

const markerPrefix = "<!-- td:issue:"
const markerStart = markerPrefix + "v1\n"
const markerEnd = "\n-->"

type metadata struct {
	Type            models.Type     `json:"type"`
	Priority        models.Priority `json:"priority"`
	Points          int             `json:"points"`
	Acceptance      string          `json:"acceptance,omitempty"`
	LastStateReason string          `json:"last_state_reason,omitempty"`
}

func encodeBody(description string, meta metadata) (string, error) {
	if strings.Contains(description, markerPrefix) {
		return "", fmt.Errorf("description contains reserved td metadata marker")
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	return description + "\n\n" + markerStart + string(data) + markerEnd, nil
}

func decodeBody(body string) (string, metadata, bool, error) {
	meta := metadata{Type: models.TypeTask, Priority: models.PriorityP2}
	i := strings.Index(body, markerPrefix)
	if i < 0 {
		return body, meta, false, nil
	}
	block := strings.TrimSpace(body[i:])
	if strings.Count(body, markerPrefix) != 1 || !strings.HasPrefix(block, markerStart) || !strings.HasSuffix(block, markerEnd) {
		return "", meta, false, fmt.Errorf("unsupported or malformed td issue metadata; refusing to overwrite it")
	}
	data := strings.TrimSuffix(strings.TrimPrefix(block, markerStart), markerEnd)
	if !strings.HasPrefix(strings.TrimSpace(data), "{") {
		return "", meta, false, fmt.Errorf("td issue metadata must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return "", meta, false, fmt.Errorf("invalid td issue metadata: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", meta, false, fmt.Errorf("invalid trailing td issue metadata")
	}
	if !models.IsValidType(meta.Type) || !models.IsValidPriority(meta.Priority) || (meta.Points != 0 && !models.IsValidPoints(meta.Points)) {
		return "", meta, false, fmt.Errorf("invalid td issue metadata fields")
	}
	return strings.TrimSuffix(body[:i], "\n\n"), meta, true, nil
}
