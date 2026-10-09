package ghcontext

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestWorkSessionPersistenceRotationAndIsolation(t *testing.T) {
	dir := t.TempDir()
	scope := Scope{Directory: dir, Path: filepath.Join(dir, "one.json"), Branch: "main", Worktree: "one"}
	state, err := scope.Update(context.Background(), func(s *State) error {
		if _, err := s.StartWorkSession("work", "abc", "one", "repo"); err != nil {
			return err
		}
		if err := s.TagWorkSession("gh-7"); err != nil {
			return err
		}
		return s.TagWorkSession("gh-7")
	})
	if err != nil {
		t.Fatal(err)
	}
	id := state.ActiveWorkSession
	state, err = scope.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := state.CurrentWorkSession()
	if err != nil || ws.ID != id || len(ws.Issues) != 1 {
		t.Fatalf("bundle lost: %+v %v", ws, err)
	}
	other := Scope{Directory: dir, Path: filepath.Join(dir, "two.json")}
	isolated, err := other.Update(context.Background(), nil)
	if err != nil || len(isolated.WorkSessions) != 0 {
		t.Fatalf("context leaked: %+v %v", isolated, err)
	}
	state, err = scope.Update(context.Background(), func(s *State) error { scope.NewSession(s); return nil })
	if err != nil || state.ActiveWorkSession != "" || len(state.WorkSessions) != 1 {
		t.Fatalf("rotation: %+v %v", state, err)
	}
	if _, err := state.CurrentWorkSession(); err == nil {
		t.Fatal("old session remains active")
	}
	if _, err := state.WorkSession(id); err != nil {
		t.Fatal("rotation destroyed history")
	}
}

func TestWorkSessionConcurrentStartAndFailedUpdate(t *testing.T) {
	dir := t.TempDir()
	scope := Scope{Directory: dir, Path: filepath.Join(dir, "one.json")}
	var wg sync.WaitGroup
	successes := make(chan string, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := scope.Update(context.Background(), func(s *State) error { _, err := s.StartWorkSession("work", "", "", ""); return err })
			if err == nil {
				successes <- state.ActiveWorkSession
			}
		}()
	}
	wg.Wait()
	close(successes)
	if len(successes) != 1 {
		t.Fatalf("concurrent starts succeeded %d times", len(successes))
	}
	_, err := scope.Update(context.Background(), func(s *State) error { _ = s.TagWorkSession("gh-9"); return errors.New("cancel") })
	if err == nil {
		t.Fatal("failed mutation succeeded")
	}
	state, err := scope.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := state.CurrentWorkSession()
	if err != nil || len(ws.Issues) != 0 {
		t.Fatalf("failed update persisted: %+v %v", ws, err)
	}
	_, err = scope.Update(context.Background(), func(s *State) error {
		if err := s.TagWorkSession("gh-9"); err != nil {
			return err
		}
		if err := s.UntagWorkSession("gh-9"); err != nil {
			return err
		}
		_, err := s.EndWorkSession("def")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = scope.Update(context.Background(), nil)
	if err != nil || state.ActiveWorkSession != "" || state.WorkSessions[0].EndSHA != "def" || state.WorkSessions[0].EndedAt == nil {
		t.Fatalf("end lost: %+v %v", state, err)
	}
}
