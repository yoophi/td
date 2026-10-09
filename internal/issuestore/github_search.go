package issuestore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/marcus/td/internal/db"
)

// SQLiteLIKE implements SQLite's default ASCII case folding and %/_ wildcards.
// Ranked search historically uses LIKE; TDQ's ~ operator remains a literal
// substring comparison. Keeping the two contracts separate avoids SQL strings.
func SQLiteLIKE(pattern string) func(string) bool {
	fold := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'Z' {
				return r + ('a' - 'A')
			}
			return r
		}, s)
	}
	var expression strings.Builder
	expression.WriteString("(?s)^")
	for _, r := range fold(pattern) {
		switch r {
		case '%':
			expression.WriteString(".*")
		case '_':
			expression.WriteString(".")
		default:
			expression.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	expression.WriteString("$")
	re := regexp.MustCompile(expression.String())
	return func(s string) bool { return re.MatchString(fold(s)) }
}

func (s *GitHubQuerySnapshot) SearchIssuesRanked(text string, opts db.ListIssuesOptions) ([]db.SearchResult, error) {
	rest := opts
	rest.Status = nil
	rest.Type = nil
	rest.Priority = ""
	rest.Labels = nil
	rest.Limit = 0
	if !reflect.DeepEqual(rest, db.ListIssuesOptions{}) {
		return nil, fmt.Errorf("unsupported ranked search options")
	}
	if opts.Limit < 0 {
		return nil, fmt.Errorf("search limit must be zero or positive")
	}
	match := SQLiteLIKE("%" + text + "%")
	labelMatchers := []func(string) bool{}
	for _, label := range opts.Labels {
		patterns := []func(string) bool{SQLiteLIKE(label + ",%"), SQLiteLIKE("%," + label + ",%"), SQLiteLIKE("%," + label), SQLiteLIKE(label)}
		labelMatchers = append(labelMatchers, func(csv string) bool {
			for _, m := range patterns {
				if m(csv) {
					return true
				}
			}
			return false
		})
	}
	results := []db.SearchResult{}
	for _, r := range s.records {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
		if len(opts.Status) > 0 && !slices.Contains(opts.Status, r.Status) {
			continue
		}
		if len(opts.Type) > 0 && !slices.Contains(opts.Type, r.Type) {
			continue
		}
		if opts.Priority != "" && string(r.Priority) != opts.Priority {
			continue
		}
		csv := strings.Join(r.Labels, ",")
		labelsMatch := true
		for _, m := range labelMatchers {
			if !m(csv) {
				labelsMatch = false
				break
			}
		}
		if !labelsMatch {
			continue
		}
		ownMatch := match(r.ID) || match(r.Title) || match(r.Description)
		activityField := ""
		if !ownMatch {
			var err error
			activityField, err = s.searchActivityField(r.ID, match)
			if err != nil {
				return nil, err
			}
			if activityField == "" {
				continue
			}
		}
		result := db.SearchResult{Issue: r.Issue, Score: 10, MatchField: activityField}
		lower := strings.ToLower(text)
		switch {
		case strings.EqualFold(r.ID, text):
			result.Score, result.MatchField = 100, "id"
		case strings.Contains(strings.ToLower(r.ID), lower):
			result.Score, result.MatchField = 90, "id"
		case strings.EqualFold(r.Title, text):
			result.Score, result.MatchField = 80, "title"
		case strings.HasPrefix(strings.ToLower(r.Title), lower):
			result.Score, result.MatchField = 70, "title"
		case strings.Contains(strings.ToLower(r.Title), lower):
			result.Score, result.MatchField = 60, "title"
		case strings.Contains(strings.ToLower(r.Description), lower):
			result.Score, result.MatchField = 40, "description"
		case strings.Contains(strings.ToLower(csv), lower):
			result.Score, result.MatchField = 20, "labels"
		}
		if result.Score == 10 && ownMatch {
			var err error
			result.MatchField, err = s.searchActivityField(r.ID, match)
			if err != nil {
				return nil, err
			}
		}
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Issue.Priority != b.Issue.Priority {
			return a.Issue.Priority < b.Issue.Priority
		}
		return a.Issue.ID < b.Issue.ID
	})
	if opts.Limit > 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
	}
	return results, nil
}

func (s *GitHubQuerySnapshot) searchActivityField(id string, match func(string) bool) (string, error) {
	activities, err := s.activities(id)
	if err != nil {
		return "", fmt.Errorf("search activity for %s: %w", id, err)
	}
	for _, a := range activities {
		if a.Kind == "log" && match(a.Message) {
			return "log", nil
		}
	}
	for _, a := range activities {
		if a.Kind != "handoff" {
			continue
		}
		for _, items := range [][]string{a.Done, a.Remaining, a.Decisions, a.Uncertain} {
			raw, _ := json.Marshal(items)
			if match(string(raw)) {
				return "handoff", nil
			}
		}
	}
	return "", nil
}
