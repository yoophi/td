package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func init() {
	for _, command := range []*cobra.Command{commentCmd, commentsCmd, commentsAddCmd, logCmd, handoffCmd} {
		local, original := command.RunE, command
		command.RunE = func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(getBaseDir())
			if err != nil {
				return err
			}
			kind, err := config.Store(cfg)
			if err != nil {
				return err
			}
			if kind == config.StoreSQLite {
				return local(cmd, args)
			}
			cmd.SilenceUsage = true
			client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
			if err != nil {
				return err
			}
			dir, err := gitHubContextDirectory()
			if err != nil {
				return err
			}
			scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
			if err != nil {
				return err
			}
			state, err := scope.Update(cmd.Context(), nil)
			if err != nil {
				return err
			}
			return runGitHubActivity(original, cmd, args, client, state, scope.Worktree)
		}
	}
}

func gitHubContextDirectory() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if workDirFlag != "" {
		dir = normalizeWorkDir(workDirFlag)
	}
	if baseDirOverride != nil {
		dir = *baseDirOverride
	}
	return dir, nil
}

func runGitHubActivity(original, cmd *cobra.Command, args []string, store issuestore.ActivityStore, state *ghcontext.State, worktree string) error {
	// Reject any newly introduced option until its semantics have been wired.
	allowed := []string{"json", "work-dir", "help"}
	if original == logCmd {
		allowed = append(allowed, "issue", "task", "type", "blocker", "decision", "hypothesis", "tried", "result")
	}
	if original == handoffCmd {
		allowed = append(allowed, "done", "remaining", "decision", "uncertain", "note", "message")
	}
	var flagErr error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !slices.Contains(allowed, f.Name) {
			flagErr = fmt.Errorf("gh-issue does not support --%s for %s", f.Name, original.Name())
		}
	})
	if flagErr != nil {
		return flagErr
	}
	if original == commentsCmd {
		if len(args) != 1 {
			return fmt.Errorf("requires one issue ID")
		}
		activity, err := store.ListActivity(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		comments := make([]models.Activity, 0)
		for _, a := range activity {
			if a.Kind == "comment" {
				comments = append(comments, a)
			}
		}
		if jsonMode(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(comments)
		}
		for _, a := range comments {
			identity := a.Author
			if a.SessionID != "" {
				identity += " / " + a.SessionID
			}
			cmd.Printf("[%s] (%s) %s\n", a.CreatedAt.Format("2006-01-02 15:04"), output.SanitizeIssueText(identity), output.SanitizeIssueText(a.Message))
		}
		if len(comments) == 0 {
			cmd.Println("No comments")
		}
		return nil
	}
	input := models.Activity{SessionID: state.Session.ID}
	id, action := "", ""
	switch original {
	case commentCmd, commentsAddCmd:
		if len(args) != 2 {
			return fmt.Errorf("requires issue ID and comment text")
		}
		id = args[0]
		input.Kind = "comment"
		input.Message = args[1]
		action = "comment_added"
	case logCmd:
		var err error
		id, input.Message, err = gitHubActivityTarget(args, cmd, state.Focus, true)
		if err != nil {
			return err
		}
		input.Kind = "log"
		input.LogType = models.LogTypeProgress
		if typ, _ := cmd.Flags().GetString("type"); typ != "" {
			input.LogType = models.LogType(typ)
		} else {
			for _, typ := range []string{"blocker", "decision", "hypothesis", "tried", "result"} {
				if set, _ := cmd.Flags().GetBool(typ); set {
					input.LogType = models.LogType(typ)
					break
				}
			}
		}
		if input.Message == "" {
			data, err := readGitHubStdin(cmd)
			if err != nil {
				return err
			}
			input.Message = strings.TrimSpace(data)
		}
		action = "logged"
	case handoffCmd:
		var err error
		id, input.Message, err = gitHubActivityTarget(args, cmd, state.Focus, false)
		if err != nil {
			return err
		}
		input.Kind = "handoff"
		if err := readGitHubHandoff(cmd, &input); err != nil {
			return err
		}
		snapshot, err := gitHubSnapshot(cmd.Context(), worktree)
		if err != nil {
			return fmt.Errorf("capture handoff Git snapshot: %w", err)
		}
		snapshot.IssueID = id
		snapshot.Event = "handoff"
		input.Snapshot = snapshot
		action = "handoff_recorded"
	}
	if id == "" {
		return fmt.Errorf("no issue specified and no focused issue")
	}
	created, err := store.AppendActivity(cmd.Context(), id, input)
	if err != nil {
		return err
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": action, "id": created.IssueID, "activity": created})
	}
	cmd.Printf("%s %s\n", strings.ToUpper(strings.ReplaceAll(action, "_", " ")), created.IssueID)
	return nil
}

func isGitHubIssueID(value string) bool {
	value = strings.TrimPrefix(strings.TrimPrefix(value, "gh-"), "#")
	n, err := strconv.Atoi(value)
	return err == nil && n > 0
}

func gitHubActivityTarget(args []string, cmd *cobra.Command, focus string, log bool) (id, message string, err error) {
	if len(args) > 2 {
		return "", "", fmt.Errorf("requires at most issue ID and message")
	}
	if len(args) == 2 {
		id, message = args[0], args[1]
		if log && !isGitHubIssueID(id) && isGitHubIssueID(message) {
			id, message = message, id
		}
	} else if len(args) == 1 {
		if isGitHubIssueID(args[0]) {
			id = args[0]
		} else {
			message = args[0]
		}
	}
	if log {
		for _, name := range []string{"issue", "task"} {
			if value, _ := cmd.Flags().GetString(name); value != "" {
				if id != "" && strings.TrimPrefix(strings.TrimPrefix(id, "gh-"), "#") != strings.TrimPrefix(strings.TrimPrefix(value, "gh-"), "#") {
					return "", "", fmt.Errorf("conflicting issue IDs")
				}
				id = value
			}
		}
	}
	if id == "" {
		id = focus
	}
	if id == "" {
		return "", "", fmt.Errorf("no issue specified and no focused issue")
	}
	return id, message, nil
}

func readGitHubStdin(cmd *cobra.Command) (string, error) {
	reader := cmd.InOrStdin()
	if file, ok := reader.(*os.File); ok {
		info, err := file.Stat()
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeCharDevice != 0 {
			return "", nil
		}
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return string(data), nil
}

func readGitHubHandoff(cmd *cobra.Command, a *models.Activity) error {
	explicit := a.Message != ""
	stdinUsed := false
	for _, section := range []struct {
		name   string
		target *[]string
	}{{"done", &a.Done}, {"remaining", &a.Remaining}, {"decision", &a.Decisions}, {"uncertain", &a.Uncertain}} {
		values, _ := cmd.Flags().GetStringArray(section.name)
		explicit = explicit || len(values) > 0
		for _, value := range values {
			var data string
			switch {
			case value == "-":
				if stdinUsed {
					return fmt.Errorf("stdin can only be used once in handoff flags")
				}
				var err error
				data, err = readGitHubStdin(cmd)
				if err != nil {
					return err
				}
				stdinUsed = true
			case strings.HasPrefix(value, "@"):
				bytes, err := os.ReadFile(strings.TrimPrefix(value, "@"))
				if err != nil {
					return fmt.Errorf("read --%s file: %w", section.name, err)
				}
				data = string(bytes)
			default:
				*section.target = append(*section.target, value)
				continue
			}
			count := 0
			for _, line := range strings.Split(data, "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					*section.target = append(*section.target, line)
					count++
				}
			}
			if count == 0 {
				return fmt.Errorf("--%s input contains no items", section.name)
			}
		}
	}
	for _, name := range []string{"note", "message"} {
		if value, _ := cmd.Flags().GetString(name); value != "" {
			a.Done = append(a.Done, value)
			explicit = true
		}
	}
	if a.Message != "" {
		a.Done = append(a.Done, a.Message)
		a.Message = ""
	}
	if explicit {
		return nil
	}
	data, err := readGitHubStdin(cmd)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var target *[]string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch line {
		case "done:":
			target = &a.Done
		case "remaining:":
			target = &a.Remaining
		case "decisions:":
			target = &a.Decisions
		case "uncertain:":
			target = &a.Uncertain
		default:
			if target == nil || (!strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "* ")) {
				return fmt.Errorf("invalid handoff input: expected done/remaining/decisions/uncertain section and list items")
			}
			*target = append(*target, strings.TrimSpace(line[2:]))
		}
	}
	return scanner.Err()
}

func gitHubSnapshot(ctx context.Context, dir string) (*models.GitSnapshot, error) {
	run := func(args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "git", args...)
		command.Dir = dir
		return command.Output()
	}
	sha, err := run("rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return nil, err
		}
		// An initialized repository can have no commits yet. Preserve its
		// branch and dirty state with an empty commit, rather than inventing SHA.
		branch, branchErr := run("symbolic-ref", "--quiet", "--short", "HEAD")
		if branchErr != nil {
			return nil, fmt.Errorf("HEAD is unreadable: %w", branchErr)
		}
		status, statusErr := run("status", "--porcelain=v1", "-z")
		if statusErr != nil {
			return nil, statusErr
		}
		return &models.GitSnapshot{Branch: strings.TrimSpace(string(branch)), DirtyFiles: gitHubDirtyFiles(status), Timestamp: time.Now().UTC()}, nil
	}
	branch, err := run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	status, err := run("status", "--porcelain=v1", "-z")
	if err != nil {
		return nil, err
	}
	return &models.GitSnapshot{CommitSHA: strings.TrimSpace(string(sha)), Branch: strings.TrimSpace(string(branch)), DirtyFiles: gitHubDirtyFiles(status), Timestamp: time.Now().UTC()}, nil
}

func gitHubDirtyFiles(status []byte) int {
	files := 0
	entries := strings.Split(string(status), "\x00")
	for i := 0; i < len(entries); i++ {
		if len(entries[i]) < 3 {
			continue
		}
		files++
		if strings.ContainsAny(entries[i][:2], "RC") {
			i++
		}
	}
	return files
}
