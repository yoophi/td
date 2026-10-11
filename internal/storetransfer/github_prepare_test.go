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
