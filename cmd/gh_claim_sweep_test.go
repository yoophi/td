package cmd

import (
	"reflect"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/session"
)

func TestGitHubClaimSelectionUsesNewestEvidenceAndProtectsLineage(t *testing.T) {
	now := time.Now().UTC()
	old, recent := now.Add(-24*time.Hour), now.Add(-time.Minute)
	records := []ghstore.Record{}
	for i, holder := range []string{"remote-idle", "local-active", "history-active", "ancestor", "unknown-time", "", "remote-idle", "remote-idle"} {
		r := ghstore.Record{Number: i + 1, Issue: models.Issue{ID: []string{"gh-1", "gh-2", "gh-3", "gh-4", "gh-5", "gh-6", "gh-7", "gh-8"}[i], Status: models.StatusInProgress, ImplementerSession: holder, UpdatedAt: old}}
		if i == 2 {
			r.Details = &ghstore.IssueDetails{Sessions: []models.IssueSessionHistory{{CreatedAt: recent}}}
		}
		if i == 4 {
			r.UpdatedAt = time.Time{}
		}
		if i == 6 {
			r.Status = models.StatusOpen
		}
		if i == 7 {
			r.Status = models.StatusBlocked
		}
		records = append(records, r)
	}
	self := session.Session{ID: "caller", PreviousSessionID: "ancestor", LastActivity: now}
	local := []session.Session{self, {ID: "ancestor", LastActivity: old}, {ID: "local-active", LastActivity: recent}}
	claims, unresolved := selectGitHubClaims(records, local, &self, "", 2*time.Hour, now)
	ids := []string{}
	for _, claim := range claims {
		ids = append(ids, claim.ID)
	}
	if !reflect.DeepEqual(ids, []string{"gh-1", "gh-7"}) || len(unresolved) != 3 {
		t.Fatalf("claims=%v unresolved=%v", ids, unresolved)
	}
	claims, unresolved = selectGitHubClaims(records, local, &self, "ancestor", 0, now)
	if len(claims) != 1 || claims[0].ID != "gh-4" || len(unresolved) != 0 {
		t.Fatalf("explicit selector: %+v %v", claims, unresolved)
	}
}

func TestGitHubClaimSelectionRecentOrFutureActivityIsNotStale(t *testing.T) {
	now := time.Now().UTC()
	r := ghstore.Record{Issue: models.Issue{ID: "gh-1", Status: models.StatusInProgress, ImplementerSession: "worker", UpdatedAt: now.Add(time.Hour)}}
	claims, unresolved := selectGitHubClaims([]ghstore.Record{r}, nil, &session.Session{ID: "caller"}, "", time.Minute, now)
	if len(claims) != 0 || len(unresolved) != 0 {
		t.Fatalf("future timestamp treated as stale: %+v %v", claims, unresolved)
	}
}
