package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/dateparse"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Output controls are not part of the durable command identity. A retry may
// change JSON/human output without changing its original mutation payload.
func backendInvocation(cmd *cobra.Command, args []string, operation string) any {
	flags := map[string]string{}
	cmd.Flags().Visit(func(f *pflag.Flag) {
		switch f.Name {
		case "json", "format", "long", "short", "no-pager", "work-dir", "refresh", "help":
			return
		}
		flags[f.Name] = f.Value.String()
	})
	return struct {
		Operation string
		Args      []string
		Flags     map[string]string
	}{operation, append([]string{}, args...), flags}
}

func runBackendCreate(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cmd.SetOut(cmd.OutOrStdout())
	title, typ, e := resolveCreateInput(cmd, args, "")
	if e != nil {
		return e
	}
	if typ == "" {
		if extracted, stripped := parseTypeFromTitle(title); extracted != "" {
			typ, title = string(extracted), stripped
		}
	}
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("issue title is required")
	}
	if typ == "" {
		typ = string(models.TypeTask)
	}
	kind := models.NormalizeType(typ)
	if !models.IsValidType(kind) {
		return fmt.Errorf("invalid type %q", typ)
	}
	priority, _ := cmd.Flags().GetString("priority")
	if priority == "" {
		priority = string(models.PriorityP2)
	}
	rank := models.NormalizePriority(priority)
	if !models.IsValidPriority(rank) {
		return fmt.Errorf("invalid priority %q", priority)
	}
	points, _ := cmd.Flags().GetInt("points")
	if points != 0 && !models.IsValidPoints(points) {
		return fmt.Errorf("invalid points %d", points)
	}
	blocks, _ := cmd.Flags().GetStringArray("blocks")
	if len(mergeMultiValueFlag(blocks)) > 0 {
		return fmt.Errorf("td-backend creation with --blocks is not supported yet; no Issue created")
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var reply struct {
		Issue backendstore.Issue `json:"issue"`
	}
	e = c.RunIntent(cmd.Context(), backendInvocation(cmd, args, "create"), func() (backendstore.MutationRequest, error) {
		project, e := c.Client.GetProject(cmd.Context(), c.ProjectID, c.SessionID)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		length := utf8.RuneCountInString(strings.TrimSpace(title))
		if length < project.TitleMinLength || length > project.TitleMaxLength {
			return backendstore.MutationRequest{}, fmt.Errorf("backend title must contain %d..%d characters", project.TitleMinLength, project.TitleMaxLength)
		}
		description, _, stdinUsed, e := resolveRichTextField(cmd, []string{"description", "desc", "body", "notes"}, "description-file", false)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		acceptance, _, _, e := resolveRichTextField(cmd, []string{"acceptance"}, "acceptance-file", stdinUsed)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		labels := []string{}
		for _, flag := range []string{"labels", "label", "tags", "tag"} {
			if cmd.Flags().Changed(flag) {
				values, _ := cmd.Flags().GetStringArray(flag)
				labels = mergeMultiValueFlag(values)
				if len(labels) > 0 {
					break
				}
			}
		}
		minor, _ := cmd.Flags().GetBool("minor")
		body := map[string]any{"title": strings.TrimSpace(title), "type": kind, "priority": rank, "points": points, "labels": labels, "description": description, "acceptance": acceptance, "minor": minor, "created_branch": c.Branch}
		for _, flag := range []string{"due", "defer"} {
			value, _ := cmd.Flags().GetString(flag)
			if value != "" {
				parsed, e := dateparse.ParseDate(value)
				if e != nil {
					return backendstore.MutationRequest{}, fmt.Errorf("invalid --%s date: %w", flag, e)
				}
				field := "due_date"
				if flag == "defer" {
					field = "defer_until"
				}
				body[field] = parsed
			}
		}
		parentRaw, epicRaw := "", ""
		if cmd.Flags().Changed("parent") {
			parentRaw, _ = cmd.Flags().GetString("parent")
		}
		if cmd.Flags().Changed("epic") {
			epicRaw, _ = cmd.Flags().GetString("epic")
		}
		depends, _ := cmd.Flags().GetStringArray("depends-on")
		depends = mergeMultiValueFlag(depends)
		if parentRaw != "" || epicRaw != "" || len(depends) > 0 {
			e = c.ReadSnapshot(cmd.Context(), func(rows []backendstore.Issue) error {
				parent, epic := "", ""
				var e error
				if parentRaw != "" {
					parent, e = resolveBackendID(rows, parentRaw)
					if e != nil {
						return e
					}
				}
				if epicRaw != "" {
					epic, e = resolveBackendID(rows, epicRaw)
					if e != nil {
						return e
					}
				}
				if parent != "" && epic != "" && parent != epic {
					return fmt.Errorf("--parent and --epic specify different parents")
				}
				if parent == "" {
					parent = epic
				}
				if parent != "" {
					body["parent_id"] = parent
				}
				resolved := []string{}
				for _, raw := range depends {
					id, e := resolveBackendID(rows, raw)
					if e != nil {
						return e
					}
					resolved = append(resolved, id)
				}
				body["depends_on"] = resolved
				return nil
			})
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
		}
		raw, e := json.Marshal(body)
		return backendstore.MutationRequest{Method: "POST", Resource: "/issues", Body: raw}, e
	}, &reply, func() error {
		if e := backendstore.ValidateIssue(reply.Issue); e != nil {
			return e
		}
		if reply.Issue.Title != strings.TrimSpace(title) || reply.Issue.Type != kind || reply.Issue.Priority != rank || reply.Issue.Status != models.StatusOpen {
			return fmt.Errorf("backend creation response does not match the requested Issue")
		}
		if reply.Issue.CreatorSession != c.SessionID {
			return fmt.Errorf("backend creation returned a different creator Session")
		}
		return nil
	})
	if e != nil {
		return e
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"id": reply.Issue.ID, "status": reply.Issue.Status, "action": "created", "issue": reply.Issue})
	}
	cmd.Printf("CREATED %s: %s\n", reply.Issue.ID, output.SanitizeIssueText(reply.Issue.Title))
	return nil
}
