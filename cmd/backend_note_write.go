package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/config"
	"github.com/spf13/cobra"
)

var errBackendNoteUnchanged = errors.New("note already has requested state")

func init() {
	for _, command := range []*cobra.Command{noteAddCmd, noteEditCmd, noteDeleteCmd, notePinCmd, noteUnpinCmd, noteArchiveCmd, noteUnarchiveCmd} {
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
				return runBackendNoteWrite(target, cmd, args)
			}
			return local(cmd, args)
		}
	}
}

func runBackendNoteWrite(target, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var selected backendstore.Note
	var reply struct {
		Note    backendstore.Note `json:"note"`
		Deleted bool              `json:"deleted"`
	}
	e = c.RunIntent(cmd.Context(), backendInvocation(cmd, args, "note."+target.Name()), func() (backendstore.MutationRequest, error) {
		request := backendstore.MutationRequest{Method: "POST", Resource: "/notes"}
		var payload any
		if target == noteAddCmd {
			content, _ := cmd.Flags().GetString("content")
			if !cmd.Flags().Changed("content") {
				content, e = openEditorForContent("")
				if e != nil {
					return request, e
				}
			}
			payload = map[string]any{"title": args[0], "content": content, "pinned": false}
		} else {
			rows, e := c.Notes(cmd.Context())
			if e != nil {
				return request, e
			}
			selected, e = backendstore.ResolveNote(rows, args[0])
			if e != nil {
				return request, e
			}
			request.Resource += "/" + selected.ID
			request.Revision = selected.Revision
			switch target {
			case noteDeleteCmd:
				request.Method = "DELETE"
			case noteEditCmd:
				changes := map[string]any{}
				if cmd.Flags().Changed("title") {
					value, _ := cmd.Flags().GetString("title")
					changes["title"] = value
				}
				if cmd.Flags().Changed("content") {
					value, _ := cmd.Flags().GetString("content")
					changes["content"] = value
				}
				if len(changes) == 0 {
					value, e := openEditorForContent(selected.Content)
					if e != nil {
						return request, e
					}
					changes["content"] = value
				}
				request.Method, payload = "PATCH", changes
			case notePinCmd, noteUnpinCmd:
				want := target == notePinCmd
				if selected.Pinned == want {
					return request, errBackendNoteUnchanged
				}
				request.Method, payload = "PATCH", map[string]any{"pinned": want}
			case noteArchiveCmd, noteUnarchiveCmd:
				want := target == noteArchiveCmd
				if selected.Archived == want {
					return request, errBackendNoteUnchanged
				}
				request.Method, payload = "PATCH", map[string]any{"archived": want}
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
		if target == noteDeleteCmd {
			if !reply.Deleted {
				return fmt.Errorf("backend did not confirm note deletion")
			}
			return nil
		}
		if e := backendstore.ValidateNote(reply.Note); e != nil {
			return e
		}
		if selected.ID != "" && reply.Note.ID != selected.ID {
			return fmt.Errorf("backend returned a different note")
		}
		return nil
	})
	if errors.Is(e, errBackendNoteUnchanged) {
		e = nil
		reply.Note = selected
	}
	if e != nil {
		return e
	}
	if jsonMode(cmd) {
		result := map[string]any{"id": reply.Note.ID}
		if target == noteDeleteCmd {
			result = map[string]any{"id": args[0], "deleted": true}
		} else {
			result["note"] = reply.Note
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	verb := "UPDATED"
	switch target {
	case noteAddCmd:
		verb = "CREATED"
	case noteDeleteCmd:
		verb = "DELETED"
	case notePinCmd:
		verb = "PINNED"
	case noteUnpinCmd:
		verb = "UNPINNED"
	case noteArchiveCmd:
		verb = "ARCHIVED"
	case noteUnarchiveCmd:
		verb = "UNARCHIVED"
	}
	id := reply.Note.ID
	if target == noteDeleteCmd {
		id = args[0]
	}
	_, e = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", verb, id)
	return e
}
