package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func runBackendComments(original, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	allowed := " json work-dir help refresh "
	var invalid error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !strings.Contains(allowed, " "+f.Name+" ") {
			invalid = fmt.Errorf("td-backend does not support --%s for %s", f.Name, original.Name())
		}
	})
	if invalid != nil {
		return invalid
	}
	expected := 2
	if original == commentsCmd {
		expected = 1
	}
	if len(args) != expected {
		return fmt.Errorf("%s requires %d arguments", original.Name(), expected)
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	if original == commentsCmd {
		var comments []backendstore.Comment
		e = c.ReadSnapshot(cmd.Context(), func(rows []backendstore.Issue) error {
			id, e := resolveBackendID(rows, args[0])
			if e != nil {
				return e
			}
			detail, e := c.Detail(cmd.Context(), id)
			if e != nil {
				return e
			}
			comments = append([]backendstore.Comment{}, detail.Comments...)
			return nil
		})
		if e != nil {
			return e
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(comments)
		}
		for _, item := range comments {
			if _, e := fmt.Fprintf(cmd.OutOrStdout(), "[%s] (%s) %s\n", item.CreatedAt.Format("2006-01-02 15:04"), output.SanitizeIssueText(item.SessionID), output.SanitizeIssueText(item.Text)); e != nil {
				return e
			}
		}
		if len(comments) == 0 {
			_, e = fmt.Fprintln(cmd.OutOrStdout(), "No comments")
		}
		return e
	}
	text := args[1]
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("comment text is required")
	}
	var reply struct {
		Comment backendstore.Comment `json:"comment"`
	}
	e = c.RunIntent(cmd.Context(), backendInvocation(cmd, args, "comment"), func() (backendstore.MutationRequest, error) {
		rows, e := c.Client.ListIssues(cmd.Context(), c.ProjectID, c.SessionID, true)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		id, e := resolveBackendID(rows, args[0])
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		var revision string
		for _, row := range rows {
			if row.ID == id {
				revision = row.Revision
				break
			}
		}
		body, e := json.Marshal(map[string]string{"text": text})
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + id + "/comments", Revision: revision, Body: body}, nil
	}, &reply, func() error {
		if reply.Comment.ID == "" || reply.Comment.SessionID != c.SessionID || reply.Comment.Text != text || reply.Comment.CreatedAt.IsZero() {
			return fmt.Errorf("invalid backend comment response")
		}
		requested := strings.TrimSpace(args[0])
		if !strings.HasPrefix(requested, "td-") {
			requested = "td-" + requested
		}
		if !strings.HasPrefix(reply.Comment.IssueID, requested) {
			return fmt.Errorf("backend comment belongs to a different Issue")
		}
		return nil
	})
	if e != nil {
		return e
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "commented", "comment": reply.Comment})
	}
	_, e = fmt.Fprintf(cmd.OutOrStdout(), "COMMENT ADDED %s\n", reply.Comment.IssueID)
	return e
}
