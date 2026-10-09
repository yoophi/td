package cmd

// This inventory detects CLI surface changes, not store support. Each new or
// changed entry still needs an implementation/flag contract and acceptance
// evidence before it may be classified as supported in the store matrix.
import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type cliInventoryFlag struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Shorthand  string `json:"shorthand,omitempty"`
	Hidden     bool   `json:"hidden,omitempty"`
	Deprecated string `json:"deprecated,omitempty"`
	Inherited  bool   `json:"inherited,omitempty"`
	Persistent bool   `json:"persistent,omitempty"`
}

type cliInventoryCommand struct {
	Path       string             `json:"path"`
	Use        string             `json:"use"`
	Aliases    []string           `json:"aliases"`
	Hidden     bool               `json:"hidden,omitempty"`
	Deprecated string             `json:"deprecated,omitempty"`
	Runnable   bool               `json:"runnable"`
	Flags      []cliInventoryFlag `json:"flags"`
}

func collectCLIInventory(root *cobra.Command) []cliInventoryCommand {
	rows := []cliInventoryCommand{}
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		cmd.InitDefaultHelpFlag()
		cmd.InitDefaultVersionFlag()
		aliases := append([]string{}, cmd.Aliases...)
		sort.Strings(aliases)
		row := cliInventoryCommand{Path: cmd.CommandPath(), Use: cmd.Use,
			Aliases: aliases, Hidden: cmd.Hidden, Deprecated: cmd.Deprecated,
			Runnable: cmd.Runnable(), Flags: []cliInventoryFlag{}}
		flags := map[string]cliInventoryFlag{}
		appendFlags := func(set *pflag.FlagSet, inherited bool) {
			set.VisitAll(func(flag *pflag.Flag) {
				flags[flag.Name] = cliInventoryFlag{Name: flag.Name, Type: flag.Value.Type(),
					Shorthand: flag.Shorthand, Hidden: flag.Hidden, Deprecated: flag.Deprecated,
					Inherited: inherited, Persistent: !inherited && cmd.PersistentFlags().Lookup(flag.Name) != nil}
			})
		}
		appendFlags(cmd.InheritedFlags(), true)
		appendFlags(cmd.LocalFlags(), false)
		for _, flag := range flags {
			row.Flags = append(row.Flags, flag)
		}
		sort.Slice(row.Flags, func(i, j int) bool { return row.Flags[i].Name < row.Flags[j].Name })
		rows = append(rows, row)
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })
	return rows
}

func TestCLIInventoryHelperProcess(t *testing.T) {
	if os.Getenv("TD_CLI_INVENTORY_HELPER") != "1" {
		return
	}
	SetVersion("inventory-fixture")
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()
	if err := json.NewEncoder(os.Stdout).Encode(collectCLIInventory(rootCmd)); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestCLIInventoryIncludesInheritedShadowedAndHiddenFlags(t *testing.T) {
	root := &cobra.Command{Use: "td"}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().String("scope", "", "")
	child := &cobra.Command{Use: "hidden ARG", Aliases: []string{"z", "a"}, Hidden: true, Run: func(*cobra.Command, []string) {}}
	child.Flags().Bool("scope", false, "local shadow")
	child.Flags().String("legacy", "", "")
	if err := child.Flags().MarkHidden("legacy"); err != nil {
		t.Fatal(err)
	}
	if err := child.Flags().MarkDeprecated("legacy", "use scope"); err != nil {
		t.Fatal(err)
	}
	root.AddCommand(child)
	rows := collectCLIInventory(root)
	if len(rows) != 2 || !rows[1].Hidden || !reflect.DeepEqual(rows[1].Aliases, []string{"a", "z"}) || !rows[1].Runnable {
		t.Fatal(rows)
	}
	byName := map[string]cliInventoryFlag{}
	for _, flag := range rows[1].Flags {
		byName[flag.Name] = flag
	}
	if !byName["json"].Inherited || byName["scope"].Inherited || byName["scope"].Type != "bool" || !byName["legacy"].Hidden || byName["legacy"].Deprecated != "use scope" {
		t.Fatal(byName)
	}
}

func TestCLIInventoryMatchesCapturedSurface(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_CLI_INVENTORY_HELPER", "1")
	t.Setenv("TD_ANALYTICS", "false")
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "false")
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	for _, enabled := range []string{"false", "true"} {
		t.Run("sync_cli="+enabled, func(t *testing.T) {
			t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			data, err := exec.CommandContext(ctx, executable, "-test.run=^TestCLIInventoryHelperProcess$").Output()
			if err != nil {
				t.Fatal(err)
			}
			var rows []cliInventoryCommand
			if err := json.Unmarshal(data, &rows); err != nil {
				t.Fatal(err)
			}
			canonical, err := json.MarshalIndent(rows, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			canonical = append(canonical, '\n')
			file := filepath.Join("testdata", "cli-inventory-sync-"+enabled+".json")
			if os.Getenv("TD_UPDATE_CLI_INVENTORY") == "1" {
				if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, canonical, 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != string(canonical) {
				t.Fatalf("CLI commands, aliases or flags changed; review store contracts and update %s explicitly", file)
			}
		})
	}
}
