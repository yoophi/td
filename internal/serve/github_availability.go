package serve

import (
	"context"
	"fmt"
	"net/http"

	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/reviewpolicy"
)

type githubAvailabilityClient interface {
	AvailableTransitions(context.Context, *ghstore.Record, string, reviewpolicy.Mode) ([]string, error)
}

func githubIssueDTO(ctx context.Context, client any, record *ghstore.Record, baseDir, session string, enabled bool) (IssueDTO, error) {
	dto := IssueToDTO(&record.Issue)
	if !enabled {
		return dto, nil
	}
	provider, ok := client.(githubAvailabilityClient)
	if !ok {
		return dto, fmt.Errorf("workflow availability is unavailable")
	}
	mode, err := features.ResolveReviewPolicyMode(baseDir)
	if err != nil {
		return dto, err
	}
	dto.AvailableTransitions, err = provider.AvailableTransitions(ctx, record, session, mode)
	return dto, err
}

func (s *GitHubWriteStore) writeIssueSuccess(w http.ResponseWriter, r *http.Request, client githubIssueWriter, record *ghstore.Record, status int, changed bool) {
	enabled := s.availabilityEnabled != nil && s.availabilityEnabled()
	dto, err := githubIssueDTO(r.Context(), client, record, s.baseDir, s.sessionID, enabled)
	if err != nil {
		if changed {
			err = fmt.Errorf("%s is saved, but transition availability could not be read (inspect current state before retrying): %w", record.ID, err)
		}
		githubWriteError(w, err)
		return
	}
	WriteSuccess(w, map[string]any{"issue": dto}, status)
}
