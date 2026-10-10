package cmd

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The inventory test checks the running Cobra tree in each feature mode. This
// check ties that surface to explicit audit responsibility, including each
// effective flag; a pending classification must not become a support claim.
func TestGitHubSupportMatrixCoversInventoryAndEvidence(t *testing.T) {
	var matrix struct {
		SchemaVersion int `json:"schema_version"`
		Commands      []struct {
			Command  string            `json:"command"`
			Owner    string            `json:"owner"`
			State    string            `json:"state"`
			Reason   string            `json:"reason"`
			Evidence []string          `json:"evidence"`
			Flags    map[string]string `json:"flags"`
		} `json:"commands"`
	}
	data, err := os.ReadFile("../docs/storage/github-support-matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &matrix); err != nil {
		t.Fatal(err)
	}
	if matrix.SchemaVersion != 1 {
		t.Fatal("unknown matrix schema")
	}
	tests := map[string]bool{}
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			if fn, ok := declaration.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				tests[fn.Name.Name] = true
			}
		}
	}
	byCommand := map[string]int{}
	allowed := map[string]bool{"pending": true, "partial": true, "unsupported": true, "sqlite-only": true}
	for i, row := range matrix.Commands {
		if _, exists := byCommand[row.Command]; exists {
			t.Fatalf("duplicate command %s", row.Command)
		}
		byCommand[row.Command] = i
		if !strings.HasPrefix(row.Owner, "#") || row.Reason == "" || !allowed[row.State] {
			t.Fatalf("missing responsibility/classification: %s", row.Command)
		}
		if row.State == "partial" && len(row.Evidence) == 0 {
			t.Fatalf("partial without evidence: %s", row.Command)
		}
		for _, evidence := range row.Evidence {
			if !tests[evidence] {
				t.Fatalf("%s references missing test %s", row.Command, evidence)
			}
		}
		if row.State == "sqlite-only" && row.Command != "td upgrade" && row.Command != "td doctor fk" {
			t.Fatalf("new exclusion needs explicit scope review: %s", row.Command)
		}
		for flag, state := range row.Flags {
			if !allowed[state] || state == "partial" {
				t.Fatalf("invalid flag audit %s --%s: %s", row.Command, flag, state)
			}
		}
	}
	for _, mode := range []string{"false", "true"} {
		data, err := os.ReadFile(filepath.Join("testdata", "cli-inventory-sync-"+mode+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var inventory []cliInventoryCommand
		if err := json.Unmarshal(data, &inventory); err != nil {
			t.Fatal(err)
		}
		if len(inventory) != len(matrix.Commands) {
			t.Fatalf("sync=%s: matrix misses commands", mode)
		}
		for _, command := range inventory {
			i, exists := byCommand[command.Path]
			if !exists {
				t.Fatalf("missing command %s", command.Path)
			}
			row := matrix.Commands[i]
			if len(command.Flags) != len(row.Flags) {
				t.Fatalf("flag coverage changed for %s", command.Path)
			}
			for _, flag := range command.Flags {
				if _, exists := row.Flags[flag.Name]; !exists {
					t.Fatalf("missing flag %s --%s", command.Path, flag.Name)
				}
			}
		}
	}
}
