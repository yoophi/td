package backendstore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

type Operation struct {
	ID             string          `json:"id"`
	SessionID      string          `json:"session_id"`
	Kind           string          `json:"kind"`
	Method         string          `json:"method"`
	Path           string          `json:"path"`
	Status         string          `json:"status"`
	HasEffects     bool            `json:"has_effects"`
	Undone         bool            `json:"undone"`
	ResponseStatus int             `json:"response_status"`
	Response       json.RawMessage `json:"response"`
	CreatedAt      time.Time       `json:"created_at"`
}

func (c *Connection) Operations(ctx context.Context) ([]Operation, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return nil, e
	}
	out := []Operation{}
	seen := map[string]bool{}
	for offset := 0; ; offset += 200 {
		query := url.Values{"session_id": {c.SessionID}, "limit": {"200"}, "offset": {strconv.Itoa(offset)}}
		var page struct {
			Operations []Operation `json:"operations"`
		}
		if e = c.Client.request(ctx, "GET", path+"/operations?"+query.Encode(), c.SessionID, "", nil, &page); e != nil {
			return nil, e
		}
		if len(page.Operations) > 200 {
			return nil, fmt.Errorf("backend operation page exceeds limit")
		}
		for _, op := range page.Operations {
			if !operationPattern.MatchString(op.ID) || op.SessionID != c.SessionID || op.Kind == "" || op.Status != "committed" || op.CreatedAt.IsZero() || seen[op.ID] {
				return nil, fmt.Errorf("invalid backend operation history")
			}
			seen[op.ID] = true
			out = append(out, op)
		}
		if len(page.Operations) < 200 {
			return out, nil
		}
	}
}
