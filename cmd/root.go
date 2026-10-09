// Package cmd implements all td CLI commands using cobra.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/session"
	"github.com/marcus/td/internal/suggest"
	"github.com/marcus/td/internal/workdir"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	versionStr      string
	baseDir         string
	baseDirOverride *string // For testing
	workDirFlag     string  // --work-dir flag value
	cmdStartTime    time.Time
	executedCmd     *cobra.Command // Captured for analytics logging
)

// SetVersion sets the version string and enables --version flag
func SetVersion(v string) {
	versionStr = v
	rootCmd.Version = v
}

var rootCmd = &cobra.Command{
	Use:   "td",
	Short: "Local task and session management CLI",
	Long: `td - A minimalist local task and session management CLI designed for AI-assisted development workflows.

Optimized for session continuity—capturing working state so new context windows can resume where previous ones stopped.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if os.Getenv("TD_GH_DEBUG") == "1" {
			ctx, _ := ghstore.WithAPICost(cmd.Context())
			cmd.SetContext(ctx)
		}
		if refresh, _ := cmd.Flags().GetBool("refresh"); refresh {
			cmd.SetContext(ghstore.WithFreshSnapshot(cmd.Context()))
		}
		cmdStartTime = time.Now()
		executedCmd = cmd
		runGatedSyncStartupHook(cmd)
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		// Capture executed command for analytics (logged in Execute() to avoid double logging)
		executedCmd = cmd
		runGatedSyncMutationHook(cmd)
	},
}

// initLogFile redirects slog to a file if TD_LOG_FILE is set.
// Useful for debugging auto-sync errors while running td monitor.
func initLogFile() *os.File {
	path := os.Getenv("TD_LOG_FILE")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return f
}

// Execute runs the root command
// errSilentExit marks an error whose message has already been delivered by the
// command itself. Execute sets the exit code and prints nothing further.
var errSilentExit = errors.New("td: silent exit")

func Execute() {
	if f := initLogFile(); f != nil {
		defer func() { _ = f.Close() }()
	}

	cmdStartTime = time.Now()
	executedCmd = nil // Reset for this execution

	command, err := rootCmd.ExecuteC()
	if command != nil {
		ghstore.LogAPICostContext(command.Context())
	}
	if executedCmd == nil && command != nil && command != rootCmd {
		executedCmd = command
	}

	// Log analytics once (handles both success and failure)
	logAnalytics(err)

	if err != nil {
		args := os.Args[1:]

		// Some commands report their failure fully through their own output
		// (per-issue errors, JSON envelopes) and return a sentinel purely to
		// set the exit code. Printing anything more would emit a second JSON
		// envelope — making --json output unparseable — or repeat a message the
		// command already tailored.
		if errors.Is(err, errSilentExit) {
			os.Exit(1)
		}

		// Log agent error for analysis
		logAgentError(args, err.Error())

		// JSON callers always get a JSON error envelope. Check the parsed
		// persistent flag, falling back to a raw os.Args scan in case flag
		// parsing itself failed on an unknown flag. This must run before the
		// unknown-flag/workflow-hint handling so json callers never get the
		// human-oriented hint text.
		//
		// The envelope reports the error's own code when it carries one (see
		// withErrorCode); invalid_input is the fallback for uncoded errors,
		// which are dominated by cobra usage failures.
		if jsonErrorRequested() {
			emitTopLevelJSONError(err)
			os.Exit(1)
		}

		// Check if this is an unknown flag error and provide suggestions
		if handleUnknownFlagError(err.Error(), args) {
			os.Exit(1)
		}

		// Check if this is an unknown command that we can provide workflow hints for
		if len(args) > 0 && handleWorkflowHint(args[0]) {
			os.Exit(1)
		}
		// Print the error for non-workflow unknown commands
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// jsonErrorRequested reports whether the top-level error path should emit a
// JSON error envelope. It prefers the parsed persistent --json flag but falls
// back to scanning os.Args, since flag parsing may have failed before the flag
// was recorded (e.g. when the error is itself an unknown flag).
func jsonErrorRequested() bool {
	if jsonRequested, err := rootCmd.PersistentFlags().GetBool("json"); err == nil && jsonRequested {
		return true
	}
	return slices.Contains(os.Args, "--json")
}

// logAnalytics logs command usage analytics once after execution completes
func logAnalytics(err error) {
	if !db.AnalyticsEnabled() {
		return
	}

	dir := getBaseDir()
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if dir == "" {
		return
	}

	// Build event using captured command (set in PersistentPostRun) or args fallback
	event := buildCommandEvent(executedCmd, err)
	event.SessionID = diagnosticSessionID(dir)

	// If no command was captured (e.g., unknown command), find first non-flag arg
	if event.Command == "" {
		event.Command = firstNonFlagArg(os.Args[1:])
	}

	_ = db.LogCommandUsage(dir, event)
}

func firstNonFlagArg(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

// logAgentError logs a failed command for agent UX analysis
func logAgentError(args []string, errMsg string) {
	dir := getBaseDir()
	if dir == "" {
		// Fallback: get cwd directly (OnInitialize may not have run for unknown commands)
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return
		}
	}

	// Try to get session ID (may fail if not initialized)
	sessionID := diagnosticSessionID(dir)

	// Log the error (silently fails if project not initialized)
	_ = db.LogAgentError(dir, args, errMsg, sessionID)
}

// handleUnknownFlagError checks if error is an unknown flag and suggests alternatives
// Returns true if handled (printed suggestion)
func handleUnknownFlagError(errMsg string, args []string) bool {
	// Match "unknown flag: --foo" or "unknown shorthand flag: 'f'"
	unknownFlagRe := regexp.MustCompile(`unknown (?:shorthand )?flag: ['\-]*([a-zA-Z0-9\-_]+)`)
	matches := unknownFlagRe.FindStringSubmatch(errMsg)
	if len(matches) < 2 {
		return false
	}

	unknownFlag := matches[1]

	// Check for common alias hint first
	if hint := suggest.GetFlagHint(unknownFlag); hint != "" {
		fmt.Fprintf(os.Stderr, "Error: unknown flag: --%s\n", unknownFlag)
		fmt.Fprintf(os.Stderr, "  Hint: %s\n", hint)
		return true
	}

	// Find the command being run to get its valid flags
	cmdName := ""
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			cmdName = arg
			break
		}
	}

	// Get valid flags for the command
	validFlags := getValidFlagsForCommand(cmdName)
	if len(validFlags) == 0 {
		return false
	}

	// Find suggestions
	suggestions := suggest.Flag(unknownFlag, validFlags)

	fmt.Fprintf(os.Stderr, "Error: unknown flag: --%s\n", unknownFlag)
	if len(suggestions) > 0 {
		fmt.Fprintf(os.Stderr, "  Did you mean: %s\n", strings.Join(suggestions, ", "))
	}
	fmt.Fprintf(os.Stderr, "  Run 'td %s --help' to see available flags.\n", cmdName)
	return true
}

// getValidFlagsForCommand returns the valid flag names for a command
func getValidFlagsForCommand(cmdName string) []string {
	var flags []string

	// Find the command
	cmd, _, err := rootCmd.Find([]string{cmdName})
	if err != nil || cmd == nil {
		return flags
	}

	// Collect all flag names
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		flags = append(flags, "--"+f.Name)
		if f.Shorthand != "" {
			flags = append(flags, "-"+f.Shorthand)
		}
	})

	return flags
}

// handleWorkflowHint checks if the command is a common workflow alias and shows a hint
// Returns true if a hint was shown
func handleWorkflowHint(cmd string) bool {
	switch cmd {
	case "done", "complete", "submit":
		showWorkflowHint(cmd, "review",
			"Or use 'td close <id>' to close directly without review.")
		return true
	case "finish":
		showWorkflowHint(cmd, "close",
			"Use 'td close <id>' for direct close, or 'td review' → 'td approve' for reviewed completion.")
		return true
	case "story":
		// "story" is an alias for "feature" type - provide helpful hint
		fmt.Fprintf(os.Stderr, "\nUnknown command: 'story'\n\n")
		fmt.Fprintf(os.Stderr, "In td, 'story' maps to type 'feature'. Use:\n")
		fmt.Fprintf(os.Stderr, "  td create --type feature \"Title\"   Create a feature/story\n")
		fmt.Fprintf(os.Stderr, "  td list --type feature             List all features/stories\n\n")
		fmt.Fprintf(os.Stderr, "Or use 'td task' for tasks:\n")
		fmt.Fprintf(os.Stderr, "  td task create \"Title\"             Create a task\n")
		fmt.Fprintf(os.Stderr, "  td task list                       List all tasks\n")
		return true
	}
	return false
}

// nameWithAliases returns "name, alias1, alias2" if aliases exist, else just "name"
func nameWithAliases(cmd *cobra.Command) string {
	if len(cmd.Aliases) > 0 {
		return cmd.Name() + ", " + strings.Join(cmd.Aliases, ", ")
	}
	return cmd.Name()
}

func init() {
	cobra.OnInitialize(initBaseDir)
	rootCmd.PersistentFlags().StringVarP(&workDirFlag, "work-dir", "w", "", "project directory (resolves .td-root and git worktrees from this path)")
	rootCmd.PersistentFlags().Bool("json", false, "Output result as JSON")
	rootCmd.PersistentFlags().Bool("refresh", false, "Bypass reusable GitHub issue/comment snapshots for this invocation")

	// Add custom template function for showing aliases
	cobra.AddTemplateFunc("nameWithAliases", nameWithAliases)

	// Custom usage template that shows aliases inline
	usageTemplate := `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad (nameWithAliases .) (add .NamePadding 8)}} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad (nameWithAliases .) (add .NamePadding 8)}} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad (nameWithAliases .) (add .NamePadding 8)}} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`

	// Need to add the 'add' function for padding calculation
	cobra.AddTemplateFunc("add", func(a, b int) int { return a + b })

	rootCmd.SetUsageTemplate(usageTemplate)

	// Define command groups for organized help output
	rootCmd.AddGroup(
		&cobra.Group{ID: "core", Title: "Core Commands:"},
		&cobra.Group{ID: "workflow", Title: "Workflow Commands:"},
		&cobra.Group{ID: "query", Title: "Query Commands:"},
		&cobra.Group{ID: "shortcuts", Title: "Shortcuts:"},
		&cobra.Group{ID: "session", Title: "Session Commands:"},
		&cobra.Group{ID: "files", Title: "File Commands:"},
		&cobra.Group{ID: "system", Title: "System Commands:"},
	)

	// Assign built-in commands to system group
	rootCmd.SetHelpCommandGroupID("system")
	rootCmd.SetCompletionCommandGroupID("system")

	// Don't print Cobra's default error message - we handle it ourselves
	rootCmd.SilenceErrors = true
}

func initBaseDir() {
	var err error
	var startDir string

	// Priority: --work-dir flag > TD_WORK_DIR env > cwd
	if workDirFlag != "" {
		startDir = normalizeWorkDir(workDirFlag)
	} else if envDir := os.Getenv("TD_WORK_DIR"); envDir != "" {
		startDir = normalizeWorkDir(envDir)
	} else {
		startDir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot determine working directory: %v\n", err)
			os.Exit(1)
		}
	}

	baseDir = workdir.ResolveBaseDir(startDir)
}

// normalizeWorkDir converts a work-dir value to an absolute, clean path.
// If the path ends in ".todos", it strips that suffix to get the project root.
func normalizeWorkDir(dir string) string {
	if filepath.Base(dir) == ".todos" {
		dir = filepath.Dir(dir)
	}
	if !filepath.IsAbs(dir) {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot determine working directory: %v\n", err)
			os.Exit(1)
		}
		dir = filepath.Join(cwd, dir)
	}
	return filepath.Clean(dir)
}

// getBaseDir returns the base directory for the project
func getBaseDir() string {
	if baseDirOverride != nil {
		return *baseDirOverride
	}
	return baseDir
}

// ValidateIssueID checks if an issue ID is valid (non-empty, non-whitespace)
// Returns an error with helpful usage info if invalid
func ValidateIssueID(id string, cmdUsage string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("issue ID required. Usage: td %s", cmdUsage)
	}
	return nil
}

// ValidateIssueIDs validates multiple issue IDs
func ValidateIssueIDs(ids []string, cmdUsage string) error {
	for _, id := range ids {
		if err := ValidateIssueID(id, cmdUsage); err != nil {
			return err
		}
	}
	return nil
}

// showWorkflowHint prints a helpful hint when a user tries an unknown workflow command
func showWorkflowHint(attempted, suggested, hint string) {
	fmt.Fprintf(os.Stderr, "\nUnknown command: '%s'\n\n", attempted)
	fmt.Fprintf(os.Stderr, "Did you mean: td %s <id>\n\n", suggested)
	fmt.Fprintf(os.Stderr, "td workflow:\n")
	fmt.Fprintf(os.Stderr, "  1. td start <id>     - Begin work\n")
	fmt.Fprintf(os.Stderr, "  2. td handoff <id>   - Capture state (required)\n")
	fmt.Fprintf(os.Stderr, "  3. td review <id>    - Submit for review\n")
	fmt.Fprintf(os.Stderr, "  4. td approve <id>   - Complete (independent reviewer records approval)\n\n")
	fmt.Fprintf(os.Stderr, "%s\n\n", hint)
	fmt.Fprintf(os.Stderr, "Run 'td usage -q' for full reference.\n")
}

// buildCommandEvent creates a CommandUsageEvent from the current command state
func buildCommandEvent(cmd *cobra.Command, err error) db.CommandUsageEvent {
	event := db.CommandUsageEvent{
		Timestamp:  cmdStartTime,
		DurationMs: time.Since(cmdStartTime).Milliseconds(),
		Success:    err == nil,
	}

	if err != nil {
		event.Error = err.Error()
	}

	if cmd != nil {
		path := []string{cmd.Name()}
		for parent := cmd.Parent(); parent != nil && parent.Name() != "td"; parent = parent.Parent() {
			path = append([]string{parent.Name()}, path...)
		}
		event.Command = path[0]
		if len(path) > 1 {
			event.Subcommand = strings.Join(path[1:], " ")
		}
		event.Flags = extractFlags(cmd)
	}

	return event
}

// Diagnostics stay local even if gh is missing or the network is unavailable.
// Failure to resolve identity never replaces the original command's outcome.
func diagnosticSessionID(dir string) string {
	cfg, err := config.Load(dir)
	if err != nil {
		return ""
	}
	kind, err := config.Store(cfg)
	if err != nil {
		return ""
	}
	if kind == config.StoreGitHub {
		if cfg.GitHub == nil || cfg.GitHub.Repo == "" {
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		worktree, err := gitHubContextDirectory()
		if err != nil {
			return ""
		}
		scope, err := ghcontext.Resolve(ctx, worktree, cfg.GitHub.Repo)
		if err != nil {
			return ""
		}
		state, err := scope.Update(ctx, nil)
		if err != nil {
			return ""
		}
		return state.Session.ID
	}
	if database, err := db.Open(dir); err == nil {
		defer func() { _ = database.Close() }()
		if sess, err := session.Get(database); err == nil {
			return sess.ID
		}
	}
	return ""
}

// extractFlags extracts changed flags from a command and sanitizes them
func extractFlags(cmd *cobra.Command) map[string]string {
	flags := make(map[string]string)
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Changed {
			flags[f.Name] = f.Value.String()
		}
	})
	return db.SanitizeFlags(flags)
}
