package backendstore

import (
	"context"
	"fmt"
	"slices"
)

func (c *Client) RequireCapability(ctx context.Context, name string) error {
	var out struct {
		Version      string   `json:"api_version"`
		Capabilities []string `json:"capabilities"`
	}
	if e := c.request(ctx, "GET", "/v1/capabilities", "", "", nil, &out); e != nil {
		return e
	}
	if out.Version != "v1" || !slices.Contains(out.Capabilities, name) {
		return fmt.Errorf("backend does not support required capability %s", name)
	}
	return nil
}
