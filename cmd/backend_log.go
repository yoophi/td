package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type backendLog struct {
	ID            string         `json:"id"`
	IssueID       string         `json:"issue_id"`
	SessionID     string         `json:"session_id"`
	WorkSessionID string         `json:"work_session_id"`
	Message       string         `json:"message"`
	Type          models.LogType `json:"type"`
	Timestamp     time.Time      `json:"timestamp"`
}

func runBackendLog(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	allowed := " json work-dir help refresh issue task type blocker decision hypothesis tried result "
	var invalid error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !strings.Contains(allowed, " "+f.Name+" ") {
			invalid = fmt.Errorf("td-backend log does not support --%s", f.Name)
		}
	})
	if invalid != nil {
		return invalid
	}
	if len(args) > 2 {
		return fmt.Errorf("log accepts at most two arguments")
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	explicitID := ""
	if len(args) == 2 {
		explicitID = args[0]
		if strings.HasPrefix(args[1], "td-") && !strings.HasPrefix(args[0], "td-") {
			explicitID = args[1]
		}
	} else if len(args) == 1 && strings.HasPrefix(args[0], "td-") {
		explicitID = args[0]
	}
	if explicitID == "" {
		explicitID, _ = cmd.Flags().GetString("issue")
	}
	if explicitID == "" {
		explicitID, _ = cmd.Flags().GetString("task")
	}
	resolveInput := func() (string, string, error) {
		rawID, message := explicitID, ""
		if len(args) == 2 {
			message = args[1]
			if strings.HasPrefix(args[1], "td-") && !strings.HasPrefix(args[0], "td-") {
				message = args[0]
			}
		} else if len(args) == 1 && !strings.HasPrefix(args[0], "td-") {
			message = args[0]
		}
		if rawID == "" {
			var e error
			rawID, e = c.Focus(cmd.Context())
			if e != nil {
				return "", "", e
			}
		}
		if rawID == "" {
			return "", "", fmt.Errorf("no issue specified and no focused issue")
		}
		if strings.TrimSpace(message) == "" {
			reader := cmd.InOrStdin()
			read := reader != os.Stdin
			if !read {
				if stat, e := os.Stdin.Stat(); e == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
					read = true
				}
			}
			if read {
				raw, e := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
				if e != nil {
					return "", "", e
				}
				if len(raw) > 1<<20 {
					return "", "", fmt.Errorf("log input exceeds 1 MiB")
				}
				message = strings.TrimSpace(string(raw))
			}
		}
		if strings.TrimSpace(message) == "" {
			return "", "", fmt.Errorf("no message provided")
		}
		return rawID, message, nil
	}
	kind := models.LogTypeProgress
	if cmd.Flags().Changed("type") {
		value, _ := cmd.Flags().GetString("type")
		kind = models.LogType(value)
	} else {
		for _, name := range []string{"blocker", "decision", "hypothesis", "tried", "result"} {
			value, _ := cmd.Flags().GetBool(name)
			if value {
				kind = models.LogType(name)
				break
			}
		}
	}
	if !slices.Contains([]models.LogType{models.LogTypeProgress, models.LogTypeSecurity, models.LogTypeBlocker, models.LogTypeDecision, models.LogTypeHypothesis, models.LogTypeTried, models.LogTypeResult, models.LogTypeOrchestration}, kind) {
		return fmt.Errorf("invalid log type %q", kind)
	}
	var reply struct {
		Log backendLog `json:"log"`
	}
	e = c.RunIntent(cmd.Context(), backendInvocation(cmd, args, "log"), func() (backendstore.MutationRequest, error) {
		rawID, message, e := resolveInput()
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		rows, e := c.Client.ListIssues(cmd.Context(), c.ProjectID, c.SessionID, true)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		id, e := resolveBackendID(rows, rawID)
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
		body := map[string]string{"message": message, "type": string(kind)}
		ws, e := c.ActiveWorkSessionID(cmd.Context())
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		if ws != "" {
			body["work_session_id"] = ws
		}
		raw, e := json.Marshal(body)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		return backendstore.MutationRequest{Method: "POST", Resource: "/issues/" + id + "/logs", Revision: revision, Body: raw}, nil
	}, &reply, func() error {
		requested := strings.TrimSpace(explicitID)
		if requested != "" && !strings.HasPrefix(requested, "td-") {
			requested = "td-" + requested
		}
		if !backendstore.ValidIssueID(reply.Log.IssueID) || (requested != "" && !strings.HasPrefix(reply.Log.IssueID, requested)) {
			return fmt.Errorf("backend log belongs to a different Issue")
		}
		if reply.Log.ID == "" || reply.Log.SessionID != c.SessionID || strings.TrimSpace(reply.Log.Message) == "" || reply.Log.Type != kind || reply.Log.Timestamp.IsZero() {
			return fmt.Errorf("invalid backend log response")
		}
		return nil
	})
	if e != nil {
		return e
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": "logged", "id": reply.Log.IssueID, "log": reply.Log})
	}
	_, e = fmt.Fprintf(cmd.OutOrStdout(), "LOGGED %s [%s]\n", reply.Log.IssueID, reply.Log.Type)
	return e
}
