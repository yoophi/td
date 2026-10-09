package monitor

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/query"
)

type BoardQueryPreviewSource interface {
	PreviewQuery(string) BoardEditorQueryPreviewMsg
}

// PreviewQuery reads a complete listing and uses the shared TDQ engine. It
// returns at most five titles and the existing -1 signal for additional matches.
func (s *GitHubBoardSource) PreviewQuery(expression string) BoardEditorQueryPreviewMsg {
	s.CancelPreview()
	result := BoardEditorQueryPreviewMsg{Query: expression, Titles: []string{}}
	fail := func(err error) BoardEditorQueryPreviewMsg { result.Error = err; return result }
	if expression == "" {
		return result
	}
	parsed, err := query.Parse(expression)
	if err != nil {
		return fail(err)
	}
	if problems := parsed.Validate(); len(problems) > 0 {
		return fail(fmt.Errorf("invalid board TDQ query: %v", problems))
	}
	if strings.TrimSpace(s.actor) == "" {
		return fail(fmt.Errorf("board preview requires actual monitor session"))
	}
	ctx, cancel := s.previewContext()
	defer cancel()
	c, err := s.open(ctx)
	if err != nil {
		return fail(err)
	}
	records, err := c.List(ctx, true)
	if err != nil {
		return fail(err)
	}
	snapshot, err := issuestore.NewGitHubQuerySnapshot(ctx, records, c)
	if err != nil {
		return fail(err)
	}
	matched, err := query.ExecuteDetailed(snapshot, expression, s.actor, query.ExecuteOptions{Limit: 6, SortBy: "priority", MaxResults: len(records) + 1})
	if err != nil {
		return fail(err)
	}
	if matched.ScanLimited {
		return fail(fmt.Errorf("board query listing was incomplete; refresh before previewing"))
	}
	result.Count = len(matched.Issues)
	if matched.Matched > 5 {
		result.Count = -1
	}
	for i, issue := range matched.Issues {
		if i >= 5 {
			break
		}
		result.Titles = append(result.Titles, issue.Title)
	}
	return result
}

// CancelPreview cancels only preview reads, not saves or board refreshes.
func (s *GitHubBoardSource) CancelPreview() {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	if s.previewCancel != nil {
		s.previewCancel()
		s.previewCancel = nil
	}
}
func (s *GitHubBoardSource) previewContext() (context.Context, context.CancelFunc) {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	if s.previewCancel != nil {
		s.previewCancel()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.previewCancel = cancel
	return ctx, cancel
}
