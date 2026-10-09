package serve

import (
	"context"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"testing"
	"time"
)

func TestGitHubContextRetainsFullHistoryAndSingleObservation(t *testing.T) {
	f := monitorFixture(time.Now())
	for n := 0; n < 60; n++ {
		f.activities["gh-1"] = append(f.activities["gh-1"], models.Activity{ID: string(rune('a' + n)), Kind: "log", SessionID: "worker", Message: "history", CreatedAt: time.Now()})
	}
	s, err := ReadGitHubContext(context.Background(), f, "worker", reviewpolicy.ModeTrusted, nil)
	if err != nil || len(s.Data.Activity) != 50 || len(s.Activities["gh-1"]) != 61 || len(s.Records) != len(f.records) {
		t.Fatalf("%+v %v", s, err)
	}
	for _, calls := range f.calls {
		if calls != 1 {
			t.Fatal("history fetched twice", f.calls)
		}
	}
	f.failure = "activity"
	if s, err := ReadGitHubContext(context.Background(), f, "worker", reviewpolicy.ModeTrusted, nil); err == nil || s != nil {
		t.Fatal("partial context returned", s, err)
	}
}
