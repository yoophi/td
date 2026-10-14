package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/spf13/cobra"
)

func init() {
	for _, command := range []*cobra.Command{boardCreateCmd, boardEditCmd, boardDeleteCmd, boardMoveCmd, boardUnpositionCmd} {
		local, target := command.RunE, command
		command.RunE = func(cmd *cobra.Command, args []string) error {
			cfg, e := config.Load(getBaseDir())
			if e != nil {
				return e
			}
			kind, e := config.Store(cfg)
			if e != nil {
				return e
			}
			if kind == config.StoreBackend {
				return runBackendBoardWrite(target, cmd, args)
			}
			return local(cmd, args)
		}
	}
}

func runBackendBoardWrite(target, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var selected string
	var reply struct {
		Board      backendstore.Board `json:"board"`
		Deleted    bool               `json:"deleted"`
		Positioned bool               `json:"positioned"`
		Removed    bool               `json:"removed"`
	}
	e = c.RunIntent(cmd.Context(), backendInvocation(cmd, args, "board."+target.Name()), func() (backendstore.MutationRequest, error) {
		request := backendstore.MutationRequest{Method: "POST", Resource: "/boards"}
		var payload any
		if target == boardCreateCmd {
			query, _ := cmd.Flags().GetString("query")
			payload = map[string]any{"name": args[0], "query": query}
		} else {
			boards, e := c.Boards(cmd.Context())
			if e != nil {
				return request, e
			}
			board, e := backendstore.ResolveBoard(boards, args[0])
			if e != nil {
				return request, e
			}
			selected = board.ID
			request.Resource += "/" + board.ID
			request.Revision = board.Revision
			switch target {
			case boardEditCmd:
				changes := map[string]any{}
				if cmd.Flags().Changed("name") {
					name, _ := cmd.Flags().GetString("name")
					changes["name"] = name
				}
				if cmd.Flags().Changed("query") {
					query, _ := cmd.Flags().GetString("query")
					changes["query"] = query
				}
				if cmd.Flags().Changed("view-mode") {
					mode, _ := cmd.Flags().GetString("view-mode")
					changes["view_mode"] = mode
				}
				if len(changes) == 0 {
					return request, fmt.Errorf("board edit requires --name, --query or --view-mode")
				}
				request.Method, payload = "PATCH", changes
			case boardDeleteCmd:
				request.Method = "DELETE"
			case boardMoveCmd:
				position, e := strconv.Atoi(args[2])
				if e != nil || position < 1 {
					return request, fmt.Errorf("position must be a positive integer")
				}
				rows, e := c.Client.ListIssues(cmd.Context(), c.ProjectID, c.SessionID, false)
				if e != nil {
					return request, e
				}
				issueID, e := resolveBackendID(rows, args[1])
				if e != nil {
					return request, e
				}
				request.Resource += "/issues"
				payload = map[string]any{"issue_id": issueID, "position": position}
			case boardUnpositionCmd:
				rows, e := c.Client.ListIssues(cmd.Context(), c.ProjectID, c.SessionID, false)
				if e != nil {
					return request, e
				}
				issueID, e := resolveBackendID(rows, args[1])
				if e != nil {
					return request, e
				}
				request.Method = "DELETE"
				request.Resource += "/issues/" + issueID
			}
		}
		if payload != nil {
			request.Body, e = json.Marshal(payload)
			if e != nil {
				return request, e
			}
		}
		return request, nil
	}, &reply, func() error {
		switch target {
		case boardDeleteCmd:
			if !reply.Deleted {
				return fmt.Errorf("backend did not confirm board deletion")
			}
		case boardMoveCmd:
			if !reply.Positioned {
				return fmt.Errorf("backend did not confirm board position")
			}
		case boardUnpositionCmd:
			if !reply.Removed {
				return fmt.Errorf("backend did not confirm board unposition")
			}
		default:
			if e := backendstore.ValidateBoard(reply.Board); e != nil {
				return e
			}
			if selected != "" && reply.Board.ID != selected {
				return fmt.Errorf("backend returned a different board")
			}
		}
		return nil
	})
	if e != nil {
		return e
	}
	if jsonMode(cmd) {
		result := map[string]any{}
		switch target {
		case boardDeleteCmd:
			result["deleted"] = true
		case boardMoveCmd:
			result["positioned"] = true
		case boardUnpositionCmd:
			result["removed"] = true
		default:
			result["board"] = reply.Board
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	verb := "Updated"
	switch target {
	case boardCreateCmd:
		verb = "Created"
	case boardDeleteCmd:
		verb = "Deleted"
	case boardMoveCmd:
		verb = "Positioned"
	case boardUnpositionCmd:
		verb = "Unpositioned"
	}
	name, id := strings.TrimSpace(reply.Board.Name), reply.Board.ID
	if target == boardDeleteCmd || target == boardMoveCmd || target == boardUnpositionCmd {
		name, id = args[0], selected
	}
	_, e = fmt.Fprintf(cmd.OutOrStdout(), "%s board %s (%s)\n", verb, name, id)
	return e
}
