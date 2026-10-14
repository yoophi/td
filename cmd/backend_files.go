package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var errBackendFilesUnchanged = errors.New("linked files already have requested state")

func runBackendFiles(original, cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	cmd.SetOut(cmd.OutOrStdout())
	if original != filesCmd {
		return changeBackendFiles(original, cmd, args)
	}
	if len(args) != 1 {
		return fmt.Errorf("one issue ID required")
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var detail backendstore.Detail
	e = c.ReadSnapshot(cmd.Context(), func(rows []backendstore.Issue) error {
		id, e := resolveBackendID(rows, args[0])
		if e != nil {
			return e
		}
		detail, e = c.Detail(cmd.Context(), id)
		return e
	})
	if e != nil {
		return e
	}
	record := &ghstore.Record{Issue: detail.Issue.Issue, Details: &ghstore.IssueDetails{Files: detail.Files}}
	return listGitHubFiles(cmd, record, c.Worktree)
}

func changeBackendFiles(original, cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("issue ID required")
	}
	if original == linkCmd {
		dependency, _ := cmd.Flags().GetString("depends-on")
		if dependency != "" {
			if len(args) != 1 || cmd.Flags().Changed("role") || cmd.Flags().Changed("recursive") {
				return fmt.Errorf("--depends-on cannot be combined with file patterns, --role or --recursive")
			}
			return changeBackendDependencies(linkCmd, cmd, args)
		}
		if len(args) < 2 {
			return fmt.Errorf("need file patterns or --depends-on flag")
		}
	} else if len(args) != 2 {
		return fmt.Errorf("unlink requires an issue ID and pattern")
	}
	allowed := " json work-dir help refresh "
	if original == linkCmd {
		allowed += "role recursive "
	}
	var invalid error
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		if !strings.Contains(allowed, " "+flag.Name+" ") {
			invalid = fmt.Errorf("td-backend %s does not support --%s", original.Name(), flag.Name)
		}
	})
	if invalid != nil {
		return invalid
	}
	role := ""
	if original == linkCmd {
		role, _ = cmd.Flags().GetString("role")
		if role == "" {
			role = string(models.FileRoleImplementation)
		}
		if !slices.Contains([]string{"implementation", "test", "reference", "config"}, role) {
			return fmt.Errorf("invalid file role %q", role)
		}
	}
	c, e := openBackendCommand(cmd)
	if e != nil {
		return e
	}
	var reply struct {
		Issue backendstore.Issue `json:"issue"`
	}
	var source string
	operation := "link"
	if original == unlinkCmd {
		operation = "unlink"
	}
	e = c.RunIntent(cmd.Context(), backendInvocation(cmd, args, operation), func() (backendstore.MutationRequest, error) {
		rows, e := c.List(cmd.Context(), true)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		id, e := resolveBackendID(rows, args[0])
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		source = id
		detail, e := c.Detail(cmd.Context(), id)
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		files := append([]models.IssueFile{}, detail.Files...)
		changed := false
		if original == linkCmd {
			dir, e := gitHubContextDirectory()
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			recursive, _ := cmd.Flags().GetBool("recursive")
			matches, e := matchGitHubFiles(c.Worktree, dir, args[1:], recursive)
			if e != nil {
				return backendstore.MutationRequest{}, e
			}
			for _, file := range matches {
				sha, e := computeFileSHA(filepath.Join(c.Worktree, filepath.FromSlash(file)))
				if e != nil {
					return backendstore.MutationRequest{}, fmt.Errorf("hash %s: %w", file, e)
				}
				index := slices.IndexFunc(files, func(f models.IssueFile) bool { return f.FilePath == file })
				if index >= 0 && files[index].Role == models.FileRole(role) && files[index].LinkedSHA == sha {
					continue
				}
				record := models.IssueFile{IssueID: id, FilePath: file, Role: models.FileRole(role), LinkedSHA: sha}
				if index >= 0 {
					files[index] = record
				} else {
					files = append(files, record)
				}
				changed = true
			}
		} else {
			pattern := filepath.ToSlash(args[1])
			if filepath.IsAbs(args[1]) {
				pattern, e = repositoryFilePath(c.Worktree, args[1], false)
				if e != nil {
					return backendstore.MutationRequest{}, e
				}
			}
			if _, e := filepath.Match(pattern, ""); e != nil {
				return backendstore.MutationRequest{}, fmt.Errorf("invalid unlink pattern: %w", e)
			}
			kept := []models.IssueFile{}
			for _, file := range files {
				match, _ := filepath.Match(pattern, file.FilePath)
				if match || pattern == file.FilePath {
					changed = true
				} else {
					kept = append(kept, file)
				}
			}
			files = kept
		}
		if !changed {
			return backendstore.MutationRequest{}, errBackendFilesUnchanged
		}
		input := make([]map[string]string, 0, len(files))
		for _, file := range files {
			input = append(input, map[string]string{"file_path": file.FilePath, "role": string(file.Role), "linked_sha": file.LinkedSHA})
		}
		body, e := json.Marshal(map[string]any{"files": input})
		if e != nil {
			return backendstore.MutationRequest{}, e
		}
		return backendstore.MutationRequest{Method: "PUT", Resource: "/issues/" + id + "/files", Revision: detail.Issue.Revision, Body: body}, nil
	}, &reply, func() error {
		if e := backendstore.ValidateIssue(reply.Issue); e != nil {
			return e
		}
		requested := strings.TrimSpace(args[0])
		if !strings.HasPrefix(requested, "td-") {
			requested = "td-" + requested
		}
		if !strings.HasPrefix(reply.Issue.ID, requested) || source != "" && reply.Issue.ID != source {
			return fmt.Errorf("backend file response differs from request")
		}
		return nil
	})
	noop := errors.Is(e, errBackendFilesUnchanged)
	if noop {
		e = nil
	}
	if e != nil {
		return e
	}
	if source == "" {
		// A pending request may replay without calling prepare.
		if reply.Issue.ID != "" {
			source = reply.Issue.ID
		} else {
			rows, e := c.List(cmd.Context(), true)
			if e != nil {
				return e
			}
			source, e = resolveBackendID(rows, args[0])
			if e != nil {
				return e
			}
		}
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": operation + "ed", "issue": source, "noop": noop})
	}
	_, e = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", strings.ToUpper(operation+"ed"), source)
	return e
}
