package ghstore

import (
	"context"
	"strings"
	"testing"

	"github.com/marcus/td/internal/reviewpolicy"
)

func TestCascadeRootMissingFromLaggedListUsesDirectObservation(t *testing.T) {
	for _, action := range []string{"review", "close"} {
		t.Run(action, func(t *testing.T) {
			f := newReviewFixture(t)
			base := f.client.run
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if strings.Contains(args[5], "?state=") {
					return []byte(`[[]]`), nil
				}
				return base(ctx, dir, payload, args...)
			}
			ctx := context.Background()
			observed, err := f.client.Get(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			o := TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted, Minor: action == "review"}
			if action == "close" {
				o.AdminReason = "Fixture explicit close"
			}
			if _, _, err := f.client.TransitionObservedWithCascades(ctx, observed, action, o); err != nil {
				t.Fatal("listed absence interpreted as deletion: ", err)
			}
		})
	}
}
