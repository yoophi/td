package storetransfer

import (
	"bytes"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func gitHubPrepareTarget(t *testing.T, data *ghstore.WorkspaceExport) *InspectedTarget {
	t.Helper()
	observation, err := NewGitHub(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &InspectedTarget{Spec: TargetSpec{ProjectDir: t.TempDir(), Store: "gh-issue", Namespace: data.Repository, Remote: "origin"}, Observation: observation}
}

func TestPrepareGitHubWorkspaceFreezesInputsAndChecksFreshnessWithoutObservationClock(t *testing.T) {
	source, _ := githubEntitySourceFixture(t)
	target := gitHubPrepareTarget(t, &ghstore.WorkspaceExport{Repository: "owner/repo"})
	p, err := PrepareGitHubWorkspace(source, target, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	archive := p.OriginalArchive()
	decoded, err := Read(bytes.NewReader(archive), 0)
	if err != nil || decoded.Envelope.SHA256 != source.Envelope.SHA256 {
		t.Fatal("preparation changed source", err)
	}
	archive[0] = '!'
	if _, err := Read(bytes.NewReader(p.OriginalArchive()), 0); err != nil {
		t.Fatal("archive copy mutated preparation", err)
	}
	tasks, err := p.TaskInputs()
	if err != nil {
		t.Fatal(err)
	}
	tasks.Items[0].Issue.Title = "mutable caller"
	second, err := p.TaskInputs()
	if err != nil || second.Items[0].Issue.Title == "mutable caller" {
		t.Fatal("task input alias escaped", err)
	}
	fresh, err := NewGitHub(&ghstore.WorkspaceExport{Repository: "owner/repo", ObservedAt: time.Now().UTC()}, 0)
	if err != nil || p.CheckTarget(fresh) != nil {
		t.Fatal("observation clock counted as target mutation", err)
	}
	changed := githubTaskInputFixture(t)
	if err := p.CheckTarget(changed); err == nil {
		t.Fatal("changed native workspace accepted")
	}
	foreign, err := NewGitHub(&ghstore.WorkspaceExport{Repository: "other/repo"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckTarget(foreign); err == nil {
		t.Fatal("different repository accepted")
	}
}

func TestPrepareGitHubWorkspaceTaskInputsBindExplicitAdoptionWithoutMutatingSource(t *testing.T) {
	source, _ := githubEntitySourceFixture(t)
	target := gitHubPrepareTarget(t, githubTaskInputFixture(t).GitHub)
	preview, err := source.Preview()
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]string{}
	for _, identity := range preview.Identities {
		if identity.Kind == "task" && identity.ID == "td-parent" {
			bindings[identity.Key] = "gh-2"
		}
	}
	if len(bindings) != 1 {
		t.Fatal("fixture has no source parent")
	}
	prepared, err := PrepareGitHubWorkspace(source, target, bindings, 0)
	if err != nil {
		t.Fatal(err)
	}
	input, err := prepared.TaskInputs()
	if err != nil {
		t.Fatal(err)
	}
	adopted := 0
	for _, item := range input.Items {
		if item.AdoptTargetID != "" {
			adopted++
			if item.AdoptTargetID != "gh-2" {
				t.Fatal("binding changed", item.AdoptTargetID)
			}
		}
	}
	if adopted != 1 || input.SourceSHA256 != source.Envelope.SHA256 {
		t.Fatal("lost adoption or changed original source digest", input)
	}
	input.Items[0].AdoptTargetID = "gh-999"
	fresh, err := prepared.TaskInputs()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range fresh.Items {
		if item.AdoptTargetID == "gh-999" {
			t.Fatal("mutable adoption input escaped into preparation")
		}
	}
	plain, err := BuildTaskImportInput(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plain.Items {
		if item.AdoptTargetID != "" {
			t.Fatal("destination decision mutated original source")
		}
	}
}

func TestPrepareGitHubWorkspaceRejectsBoardConflictAndPermitsExplicitReuse(t *testing.T) {
	source, _ := githubEntitySourceFixture(t)
	now := time.Now().UTC()
	data := &ghstore.WorkspaceExport{Repository: "owner/repo", Boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-3", Name: "SOURCE BOARD", ViewMode: "backlog", CreatedAt: now, UpdatedAt: now}, Number: 3, Details: ghstore.BoardDetails{Version: 1, ViewMode: "backlog"}}}, Native: []ghstore.NativeExport{{Number: 3, Kind: "board", Title: "SOURCE BOARD"}}}
	target := gitHubPrepareTarget(t, data)
	if p, err := PrepareGitHubWorkspace(source, target, nil, 0); err == nil || p != nil {
		t.Fatal("existing board name would collide")
	}
	preview, err := source.Preview()
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]string{}
	for _, i := range preview.Identities {
		if i.Kind == "board" {
			bindings[i.Key] = "bd-gh-3"
		}
	}
	p, err := PrepareGitHubWorkspace(source, target, bindings, 0)
	if err != nil || len(p.ReusedBindings()) != 1 {
		t.Fatal("explicit board reuse not preserved", err)
	}
	for key := range p.ReusedBindings() {
		p.ReusedBindings()[key] = "bd-gh-999"
	}
	for _, id := range p.ReusedBindings() {
		if id != "bd-gh-3" {
			t.Fatal("mutable reuse map escaped")
		}
	}
}

func TestPrepareGitHubWorkspaceCapacityAndUnknownMappingsReturnNoPreparation(t *testing.T) {
	source, _ := githubEntitySourceFixture(t)
	target := gitHubPrepareTarget(t, &ghstore.WorkspaceExport{Repository: "owner/repo"})
	if p, err := PrepareGitHubWorkspace(source, target, nil, 1); err == nil || p != nil {
		t.Fatal("capacity returned partial preparation")
	}
	if p, err := PrepareGitHubWorkspace(source, target, map[string]string{"unknown": "gh-1"}, 0); err == nil || p != nil {
		t.Fatal("unknown mapping accepted")
	}
}

func TestPrepareGitHubBuiltinPositionsConflictBeforePreparationAndDisclosesReuse(t *testing.T) {
	source, _ := githubEntitySourceFixture(t)
	board := &source.SQLite.Boards[0]
	board.Board.ID = "bd-all-issues"
	board.Board.Name = "All Issues"
	board.Board.IsBuiltin = true
	board.Board.Query = ""
	for i := range board.Positions {
		board.Positions[i].BoardID = "bd-all-issues"
	}
	source, err := NewSQLite("original", source.SQLite, 0)
	if err != nil {
		t.Fatal(err)
	}
	data := githubTaskInputFixture(t).GitHub
	stamp := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	data.Boards = []ghstore.BoardRecord{{Board: models.Board{ID: "bd-all-issues", Name: "All Issues", IsBuiltin: true, ViewMode: "swimlanes"}, Number: 3, Details: ghstore.BoardDetails{Version: 1, Builtin: true, ViewMode: "swimlanes", Positions: []ghstore.BoardPosition{{IssueID: "gh-2", Position: 4, AddedAt: stamp}}}}}
	data.Native = append(data.Native, ghstore.NativeExport{Number: 3, Kind: "board", Title: "All Issues"})
	target := gitHubPrepareTarget(t, data)
	if result, err := PrepareGitHubWorkspace(source, target, nil, 0); err == nil || result != nil {
		t.Fatal("builtin position collision accepted")
	}
	data.Boards[0].Details.Positions[0].Position = 8
	target = gitHubPrepareTarget(t, data)
	if _, err := PrepareGitHubWorkspace(source, target, nil, 0); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildDataPlan(source, target.Observation, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range plan.Disclosures {
		if d.Code == "builtin_github_board_configuration" {
			found = d.Handling == "reuse_existing" && d.Count == 1
		}
		if d.Code == "boards_configuration_queries" && d.Count != 0 {
			t.Fatal("existing builtin settings counted as copied")
		}
	}
	if !found {
		t.Fatal("existing builtin settings reuse undisclosed")
	}
}
