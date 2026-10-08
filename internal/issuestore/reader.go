// Package issuestore defines the small storage contracts used by shared queries.
// Workflow writes deliberately stay out of Reader: SQLite transactions and
// GitHub best-effort updates have different guarantees.
package issuestore

import (
	"context"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type Record struct {
	models.Issue
	StateLabelDiagnostic string `json:"state_label_warning,omitempty"`
	Number               int    `json:"number,omitempty"`
	URL                  string `json:"url,omitempty"`
}

// Reader returns complete, non-deleted issue sets. Callers filter, sort and
// limit afterwards. all=false excludes closed issues, not blocked/in-review.
// Local session state and SQL handles must not cross this boundary.
type Reader interface {
	Get(context.Context, string) (*Record, error)
	List(context.Context, bool) ([]Record, error)
	Close() error
}

func OpenReader(ctx context.Context, dir string) (Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	kind, err := config.Store(cfg)
	if err != nil {
		return nil, err
	}
	if kind == config.StoreGitHub {
		client, err := ghstore.Open(ctx, dir, cfg.GitHub)
		if err != nil {
			return nil, err
		}
		return &githubReader{client}, nil
	}
	database, err := db.Open(dir)
	if err != nil {
		return nil, err
	}
	return &sqliteReader{database}, nil
}

type sqliteReader struct{ database *db.DB }

func (r *sqliteReader) Close() error { return r.database.Close() }
func (r *sqliteReader) Get(ctx context.Context, id string) (*Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	issue, err := r.database.GetIssue(id)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Record{Issue: *issue}, nil
}
func (r *sqliteReader) List(ctx context.Context, all bool) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options := db.ListIssuesOptions{}
	if !all {
		options.Status = []models.Status{models.StatusOpen, models.StatusInProgress, models.StatusBlocked, models.StatusInReview}
	}
	issues, err := r.database.ListIssues(options)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(issues))
	for _, issue := range issues {
		records = append(records, Record{Issue: issue})
	}
	return records, nil
}

type githubReader struct{ client *ghstore.Client }

func (r *githubReader) Close() error { return nil }
func (r *githubReader) Get(ctx context.Context, id string) (*Record, error) {
	issue, err := r.client.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Record{Issue: issue.Issue, Number: issue.Number, URL: issue.URL, StateLabelDiagnostic: issue.StateLabelDiagnostic}, nil
}
func (r *githubReader) List(ctx context.Context, all bool) ([]Record, error) {
	issues, err := r.client.List(ctx, all)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(issues))
	for _, issue := range issues {
		records = append(records, Record{Issue: issue.Issue, Number: issue.Number, URL: issue.URL, StateLabelDiagnostic: issue.StateLabelDiagnostic})
	}
	return records, nil
}
