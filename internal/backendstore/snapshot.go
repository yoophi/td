package backendstore

import (
	"context"
	"fmt"
	"net/url"
)

func (c *Connection) ChangeToken(ctx context.Context) (string, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return "", e
	}
	var out struct {
		Token string `json:"change_token"`
	}
	if e = c.Client.request(ctx, "GET", path+"/changes", c.SessionID, "", nil, &out); e != nil {
		return "", e
	}
	if out.Token == "" {
		return "", fmt.Errorf("backend changes response has no token")
	}
	return out.Token, nil
}
func (c *Connection) QueryIDs(ctx context.Context, expression string) ([]string, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return nil, e
	}
	var out struct {
		IDs []string `json:"ids"`
	}
	if e = c.Client.request(ctx, "GET", path+"/query?q="+url.QueryEscape(expression), c.SessionID, "", nil, &out); e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	for _, id := range out.IDs {
		if !issueIDPattern.MatchString(id) || seen[id] {
			return nil, fmt.Errorf("invalid backend query issue IDs")
		}
		seen[id] = true
	}
	if out.IDs == nil {
		out.IDs = []string{}
	}
	return out.IDs, nil
}

// ReadSnapshot runs a read-only computation against complete rows and any lazy
// detail requests. A changed token discards the entire computation, including
// transient not-found errors caused by concurrent deletion. Callbacks must not
// print or mutate: publish only after this method returns nil.
func (c *Connection) ReadSnapshot(ctx context.Context, read func([]Issue) error) error {
	return c.ReadConsistent(ctx, func() error {
		rows, e := c.List(ctx, true)
		if e != nil {
			return e
		}
		return read(rows)
	})
}

// ReadConsistent validates a compound read without fetching issue rows itself.
func (c *Connection) ReadConsistent(ctx context.Context, read func() error) error {
	for attempt := 0; attempt < 3; attempt++ {
		before, e := c.ChangeToken(ctx)
		if e != nil {
			return e
		}
		readErr := read()
		if e := ctx.Err(); e != nil {
			return e
		}
		after, e := c.ChangeToken(ctx)
		if e != nil {
			return e
		}
		if before == after {
			return readErr
		}
	}
	return fmt.Errorf("backend changed repeatedly during read; retry the command")
}
