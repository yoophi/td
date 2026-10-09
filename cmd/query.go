package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/output"
	"github.com/marcus/td/internal/query"
	"github.com/marcus/td/internal/session"
	"github.com/spf13/cobra"
)

var queryCmd = &cobra.Command{
	Use:   "query [expression]",
	Short: "Search issues with TDQ query language",
	Long: `Search issues using TDQ (Todo Query Language), a powerful query syntax.

QUICK START:
  td query "status = open"            All open issues
  td query "type = bug AND is(open)"  Open bugs
  td query "created >= -7d"           Created in last 7 days

BASIC SYNTAX:
  field = value         Exact match
  field != value        Not equal
  field ~ "text"        Contains (case-insensitive)
  field !~ "text"       Does not contain
  field < value         Less than
  field > value         Greater than
  field <= value        Less than or equal
  field >= value        Greater than or equal

BOOLEAN OPERATORS:
  expr AND expr         Both must match
  expr OR expr          Either matches
  NOT expr              Negation
  -field = value        Shorthand for NOT
  (expr)                Grouping

FIELDS:
  status      open, in_progress, blocked, in_review, closed
  type        bug, feature, task, epic, chore
  priority    P0, P1, P2, P3, P4 (supports <=, >=)
  points      1, 2, 3, 5, 8, 13, 21
  labels      comma-separated tags
  title       issue title
  description issue description
  created     creation date (supports relative: -7d, today, this_week)
  updated     last update date
  closed      closure date
  due/defer   shared calendar dates (due_date/defer_until aliases)
  defer_count number of later deferrals
  implementer session that started work
  reviewer    session that reviewed
  parent      direct parent issue ID
  epic        ancestor epic ID (recursive)

CROSS-ENTITY SEARCH:
  log.message ~ "text"     Search log messages
  log.type = blocker       Filter by log type
  comment.text ~ "text"    Search comments
  handoff.remaining ~ "x"  Search handoff data
  file.role = test         Issues with test files

FUNCTIONS:
  has(field)             Field is not empty
  is(status)             Shorthand: is(open) = status = open
  any(field, v1, v2)     Field matches any value
  blocks(id)             Issues that block given id
  blocked_by(id)         Issues blocked by given id
  descendant_of(id)      All children of epic (recursive)
  rework()               Issues rejected and awaiting rework

SPECIAL VALUES:
  @me                    Current session ID
  EMPTY                  Empty/null field

RELATIVE DATES:
  today, yesterday, this_week, last_week, this_month
  -7d (7 days ago), -2w (2 weeks ago), -1m (1 month ago)

DATE COMPARISONS:
  A date names a whole calendar day, in your local timezone:
    created < 2026-05-09     Created before the 9th
    created <= 2026-05-09    Created on or before the 9th (includes the 9th)
    created > 2026-05-09     Created after the 9th (excludes the 9th)
    created = 2026-05-09     Created on the 9th
  An hour offset (-6h) compares to the instant instead of the day.
  An issue with no closed date matches no closed comparison.

RESULT LIMITS:
  Results are capped at 50 by default. When the cap drops matches, td says so
  on stderr; use -n 0 for every match.

EXAMPLES:
  td query "status = open"
  td query "type = bug AND priority <= P1"
  td query "created >= -7d"
  td query "implementer = @me AND is(in_progress)"
  td query "log.type = blocker"
  td query "title ~ auth OR description ~ auth"
  td query "rework()"

BOARDS:
  Save queries as reusable boards with td board:
    td board create "My Bugs" "type = bug AND implementer = @me"
    td board show "My Bugs"
    td board list                    See all saved boards

  Run 'td board --help' for full board commands.`,
	GroupID: "query",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		helpers := 0
		for _, name := range []string{"examples", "fields", "explain"} {
			if enabled, _ := cmd.Flags().GetBool(name); enabled {
				helpers++
			}
		}
		if helpers > 1 {
			return fmt.Errorf("choose only one query helper: --examples, --fields or --explain")
		}
		if helpers != 0 {
			for _, name := range []string{"output", "limit", "max-scan", "sort"} {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("--%s applies to query execution, not offline query helpers", name)
				}
			}
		}
		// Handle special flags
		if showExamples, _ := cmd.Flags().GetBool("examples"); showExamples {
			if len(args) != 0 {
				return fmt.Errorf("query --examples does not accept an expression")
			}
			if jsonMode(cmd) {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"examples": queryExamples()})
			}
			printQueryExamples()
			return nil
		}

		if showFields, _ := cmd.Flags().GetBool("fields"); showFields {
			if len(args) != 0 {
				return fmt.Errorf("query --fields does not accept an expression")
			}
			if jsonMode(cmd) {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(queryFieldInventory())
			}
			printQueryFields()
			return nil
		}

		if len(args) == 0 {
			if explain, _ := cmd.Flags().GetBool("explain"); explain {
				return fmt.Errorf("query --explain requires an expression")
			}
			return cmd.Help()
		}

		queryStr := args[0]

		// Parse and validate query first (for --explain)
		parsedQuery, err := query.Parse(queryStr)
		if err != nil {
			output.Error("Parse error: %v", err)
			printQuerySyntaxHelp()
			return err
		}

		if errs := parsedQuery.Validate(); len(errs) > 0 {
			output.Error("Validation errors:")
			for _, e := range errs {
				output.Error("  - %v", e)
			}
			return errs[0]
		}

		// Show explain if requested
		if explain, _ := cmd.Flags().GetBool("explain"); explain {
			if jsonMode(cmd) {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"query": queryStr, "parsed": parsedQuery.String(), "executed": false})
			}
			fmt.Printf("Query: %s\n", queryStr)
			fmt.Printf("Parsed: %s\n", parsedQuery.String())
			return nil
		}

		// Execute query
		baseDir := getBaseDir()
		database, err := db.Open(baseDir)
		if err != nil {
			output.Error("%v", err)
			return err
		}
		defer func() { _ = database.Close() }()

		sess, _ := session.GetOrCreate(database)
		sessionID := ""
		if sess != nil {
			sessionID = sess.ID
		}

		limit, _ := cmd.Flags().GetInt("limit")
		sortBy, _ := cmd.Flags().GetString("sort")
		sortDesc := false
		if strings.HasPrefix(sortBy, "-") {
			sortDesc = true
			sortBy = strings.TrimPrefix(sortBy, "-")
		}

		maxScan, _ := cmd.Flags().GetInt("max-scan")
		if maxScan <= 0 {
			maxScan = query.DefaultMaxResults
		}

		opts := query.ExecuteOptions{
			Limit:      limit,
			SortBy:     sortBy,
			SortDesc:   sortDesc,
			MaxResults: maxScan,
		}

		res, err := query.ExecuteDetailed(database, queryStr, sessionID, opts)
		if err != nil {
			output.Error("Query error: %v", err)
			return err
		}
		results := res.Issues

		// Output
		outputFormat, _ := cmd.Flags().GetString("output")
		// The global --json flag is an alias for -o json rather than a second
		// JSON shape: both emit the bare issue array `td list --json` emits.
		if jsonMode(cmd) {
			outputFormat = "json"
		}
		switch outputFormat {
		case "json":
			return output.JSON(jsonList(results))
		case "ids":
			for _, issue := range results {
				fmt.Println(issue.ID)
			}
		case "count":
			// A count must never be a silently truncated one.
			fmt.Printf("%d\n", res.Matched)
		default: // "table"
			for _, issue := range results {
				fmt.Println(output.FormatIssueShort(&issue))
			}
		}

		if len(results) == 0 && outputFormat != "count" {
			fmt.Printf("No issues matching query\n")
		}

		// Say so whenever the output is not the whole answer. Written to stderr
		// so machine-readable output stays clean.
		if res.Truncated {
			output.Warning("showing %d of %d matches (--limit %d; use -n 0 for all)", len(results), res.Matched, limit)
		}
		if res.ScanLimited {
			output.Warning("only the first %d issues were scanned; anything beyond that was not considered (raise with --max-scan)", maxScan)
		}

		return nil
	},
}

func printQuerySyntaxHelp() {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "TDQ Syntax: field operator value")
	fmt.Fprintln(os.Stderr, "  Operators: = != < > <= >= ~ !~")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Examples:")
	fmt.Fprintln(os.Stderr, "  td query \"status = open\"")
	fmt.Fprintln(os.Stderr, "  td query \"type = bug AND priority <= P1\"")
	fmt.Fprintln(os.Stderr, "  td query \"title ~ auth\"")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Run 'td query --examples' for more examples")
}

type queryExampleDoc struct {
	Query       string `json:"query"`
	Description string `json:"description"`
}

func queryExamples() []queryExampleDoc {
	return []queryExampleDoc{
		// Basic queries
		{"status = open", "All open issues"},
		{"type = bug", "All bugs"},
		{"priority <= P1", "High priority (P0 or P1)"},
		{"points >= 5", "Large issues (5+ points)"},

		// Text search
		{`title ~ "auth"`, "Title contains 'auth'"},
		{`"authentication"`, "Text search in title/description/id"},

		// Boolean logic
		{"type = bug AND status = open", "Open bugs"},
		{"status = open OR status = blocked", "Open or blocked issues"},
		{"NOT status = closed", "Not closed"},
		{"-status = closed", "Same as NOT status = closed"},
		{"(type = bug OR type = feature) AND priority = P0", "Critical bugs or features"},

		// Date queries
		{"created >= -7d", "Created in last 7 days"},
		{"updated >= today", "Updated today"},
		{"created >= this_week", "Created this week"},
		{"updated < -30d AND status = open", "Stale open issues"},

		// Session queries
		{"implementer = @me", "Issues I'm implementing"},
		{"implementer = @me AND is(in_progress)", "My current work"},
		{"status = in_review AND implementer != @me", "Issues I can review"},

		// Functions
		{"has(labels)", "Issues with any labels"},
		{"is(open)", "Shorthand for status = open"},
		{"is(blocked)", "Blocked issues"},
		{"any(type, bug, feature)", "Bugs or features"},
		{"descendant_of(td-epic1)", "All tasks in epic"},
		{"rework()", "Issues rejected and awaiting rework"},

		// Cross-entity queries
		{"log.type = blocker", "Issues with blocker logs"},
		{`log.message ~ "fixed"`, "Logs mentioning 'fixed'"},
		{`comment.text ~ "approved"`, "Comments with 'approved'"},
		{`handoff.remaining ~ "TODO"`, "Handoffs with remaining TODOs"},
		{"file.role = test", "Issues with test files linked"},

		// Complex queries
		{"type = bug AND priority <= P1 AND created >= -7d", "Recent high-priority bugs"},
		{"is(open) AND has(labels) AND labels ~ urgent", "Urgent labeled issues"},
	}

}

func printQueryExamples() {
	fmt.Println("TDQ Query Examples:")
	fmt.Println()
	for _, ex := range queryExamples() {
		fmt.Printf("  td query %q\n", ex.Query)
		fmt.Printf("    → %s\n\n", ex.Description)
	}
}

type queryFieldDoc struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	AllowedValues []string `json:"allowed_values,omitempty"`
}

type queryFieldDocs struct {
	IssueFields       []queryFieldDoc   `json:"issue_fields"`
	CrossEntityFields []queryFieldDoc   `json:"cross_entity_fields"`
	SpecialValues     map[string]string `json:"special_values"`
	RelativeDates     []string          `json:"relative_dates"`
}

// The parser's validation registry is authoritative for both output formats.
func queryFieldInventory() queryFieldDocs {
	docs := queryFieldDocs{IssueFields: []queryFieldDoc{}, CrossEntityFields: []queryFieldDoc{}, SpecialValues: map[string]string{"@me": "Current session ID", "EMPTY": "Empty/null field", "NULL": "Null field"}, RelativeDates: []string{"today", "yesterday", "this_week", "last_week", "this_month", "last_month", "-Nd/-Nw/-Nm/-Nh", "+Nd/+Nw/+Nm/+Nh"}}
	for name, typ := range query.KnownFields {
		if typ != "prefix" {
			docs.IssueFields = append(docs.IssueFields, queryFieldDoc{name, typ, query.EnumValues[name]})
		}
	}
	for prefix, fields := range query.CrossEntityFields {
		for field, typ := range fields {
			name := prefix + "." + field
			docs.CrossEntityFields = append(docs.CrossEntityFields, queryFieldDoc{name, typ, query.EnumValues[name]})
		}
	}
	sort.Slice(docs.IssueFields, func(i, j int) bool { return docs.IssueFields[i].Name < docs.IssueFields[j].Name })
	sort.Slice(docs.CrossEntityFields, func(i, j int) bool { return docs.CrossEntityFields[i].Name < docs.CrossEntityFields[j].Name })
	return docs
}

func printQueryFields() {
	docs := queryFieldInventory()
	fmt.Println("TDQ Searchable Fields:")
	fmt.Println()
	fmt.Println("ISSUE FIELDS:")
	for _, field := range docs.IssueFields {
		fmt.Printf("  %-14s %-8s %s\n", field.Name, field.Type, strings.Join(field.AllowedValues, ", "))
	}
	fmt.Println()
	fmt.Println("CROSS-ENTITY FIELDS:")
	for _, field := range docs.CrossEntityFields {
		fmt.Printf("  %-20s %-8s %s\n", field.Name, field.Type, strings.Join(field.AllowedValues, ", "))
	}
	fmt.Println()
	fmt.Println("SPECIAL VALUES:")
	for _, name := range []string{"@me", "EMPTY", "NULL"} {
		fmt.Printf("  %-7s %s\n", name, docs.SpecialValues[name])
	}
	fmt.Println()
	fmt.Println("RELATIVE DATES:")
	fmt.Println("  " + strings.Join(docs.RelativeDates, ", "))
}

func init() {
	rootCmd.AddCommand(queryCmd)

	queryCmd.Flags().StringP("output", "o", "table", "Output format: table, json, ids, count")
	queryCmd.Flags().IntP("limit", "n", 50, "Limit results (0 = no limit; truncation is reported)")
	queryCmd.Flags().Int("max-scan", query.DefaultMaxResults, "Maximum issues to scan before filtering")
	queryCmd.Flags().String("sort", "", "Sort by field (prefix with - for descending)")
	queryCmd.Flags().Bool("explain", false, "Show query parsing without executing")
	queryCmd.Flags().Bool("examples", false, "Show query examples")
	queryCmd.Flags().Bool("fields", false, "List all searchable fields")
}
