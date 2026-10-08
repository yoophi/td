package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

// Existing API fixtures model repositories whose status labels already exist.
// Dedicated tests below exercise discovery/creation and permission failures.
func fixtureStateLabels() map[string]bool {
	labels := map[string]bool{}
	for label := range stateLabelColors {
		labels[label] = true
	}
	return labels
}

// GitHub accepts names in requests but returns label objects in responses.
func fixtureIssueJSON(value map[string]any) ([]byte, error) {
	copy := map[string]any{}
	for key, val := range value {
		copy[key] = val
	}
	switch labels := value["labels"].(type) {
	case []any:
		objects := []map[string]string{}
		for _, label := range labels {
			objects = append(objects, map[string]string{"name": label.(string)})
		}
		copy["labels"] = objects
	case []string:
		objects := []map[string]string{}
		for _, label := range labels {
			objects = append(objects, map[string]string{"name": label})
		}
		copy["labels"] = objects
	}
	return json.Marshal(copy)
}

func TestStateLabelsPreserveUserLabelsAndFollowMetadata(t *testing.T) {
	f := newReviewFixture(t)
	f.issue["labels"] = []map[string]string{{"name": "bug"}, {"name": "td:custom"}, {"name": "TD:IN_REVIEW"}, {"name": "td:closed"}}
	r := f.transition(t, "start", TransitionOptions{SessionID: "fixture-worker"})
	if !reflect.DeepEqual(r.Labels, []string{"bug", "td:custom", "td:in_progress"}) {
		t.Fatalf("labels=%v", r.Labels)
	}
	if r.StateLabelWarning() != "" {
		t.Fatal(r.StateLabelWarning())
	}
	// A web label edit changes only presentation. It cannot change td state.
	f.issue["labels"] = []map[string]string{{"name": "bug"}, {"name": "td:closed"}}
	r, err := f.client.Get(context.Background(), "1")
	if err != nil || r.Status != models.StatusInProgress || r.StateLabelWarning() == "" {
		t.Fatalf("label became authoritative: %+v %v", r, err)
	}
	title := "Rename and repair"
	r, err = f.client.Update(context.Background(), "1", Changes{Title: &title})
	if err != nil || !reflect.DeepEqual(r.Labels, []string{"bug", "td:in_progress"}) {
		t.Fatalf("repair: %+v %v", r, err)
	}
	clear := []string{}
	r, err = f.client.Update(context.Background(), "1", Changes{Labels: &clear})
	if err != nil || !reflect.DeepEqual(r.Labels, []string{"td:in_progress"}) {
		t.Fatalf("clear erased state mirror: %+v %v", r, err)
	}
}

func TestStateLabelDiscoveryCreationAndFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "forbidden"}[failure], func(t *testing.T) {
			labels := []map[string]string{}
			posts := 0
			client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") {
					return json.Marshal([][]map[string]string{labels})
				}
				if !slices.Contains(args, "repos/owner/repo/labels") {
					t.Fatal("issue write before label setup")
				}
				posts++
				if failure {
					return nil, errors.New("HTTP 403")
				}
				var fields map[string]string
				if err := json.Unmarshal(payload, &fields); err != nil {
					t.Fatal(err)
				}
				labels = append(labels, fields)
				return json.Marshal(fields)
			}}
			err := client.ensureStateLabel(context.Background(), "td:in_progress")
			if (err != nil) != failure || posts != 1 {
				t.Fatalf("posts=%d err=%v", posts, err)
			}
			if failure && !strings.Contains(err.Error(), "issue write not attempted") {
				t.Fatal(err)
			}
			if !failure {
				if err := client.ensureStateLabel(context.Background(), "td:in_progress"); err != nil || posts != 1 {
					t.Fatal("did not reuse existing label")
				}
			}
		})
	}
}

func TestStateLabelIgnoredWriteIsPartialFailure(t *testing.T) {
	f := newReviewFixture(t)
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") {
			var fields map[string]any
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, "labels")
			payload, _ = json.Marshal(fields)
		}
		return base(ctx, dir, payload, args...)
	}
	_, _, err := f.client.Transition(context.Background(), "1", "start", TransitionOptions{SessionID: "fixture-worker"})
	if err == nil || !strings.Contains(err.Error(), "was updated") || f.writes != 1 {
		t.Fatalf("ignored labels: writes=%d %v", f.writes, err)
	}
	r, readErr := f.client.Get(context.Background(), "1")
	if readErr != nil || r.Status != models.StatusInProgress {
		t.Fatalf("lost actual partial state: %+v %v", r, readErr)
	}
}

func TestSyncStateLabelsRepairsManagedIssuesAndIsIdempotent(t *testing.T) {
	f := newReviewFixture(t)
	f.transition(t, "start", TransitionOptions{SessionID: "fixture-worker"})
	f.issue["labels"] = []map[string]string{{"name": "bug"}, {"name": "td:closed"}}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "repos/owner/repo/issues?state=all&per_page=100") {
			encoded, err := fixtureIssueJSON(f.issue)
			if err != nil {
				return nil, err
			}
			return []byte(`[[` + string(encoded) + `,{"number":2,"title":"Unmanaged","state":"open"}]]`), nil
		}
		return base(ctx, dir, payload, args...)
	}
	count, err := f.client.SyncStateLabels(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("repaired=%d %v", count, err)
	}
	writes := f.writes
	count, err = f.client.SyncStateLabels(context.Background())
	if err != nil || count != 0 || f.writes != writes {
		t.Fatalf("repeat repaired=%d writes=%d %v", count, f.writes, err)
	}
}
