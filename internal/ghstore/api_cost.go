package ghstore

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

type apiCostKey struct{}
type apiPurposeKey struct{}

var costScopeSequence atomic.Uint64

// APICounter distinguishes gh API invocations from observed paginated HTTP responses.
type APICounter struct {
	Invocations   int `json:"invocations"`
	HTTPResponses int `json:"http_responses"`
}

// APICostSnapshot contains only fixed categories and numeric metrics.
type APICostSnapshot struct {
	ID              uint64                `json:"id"`
	Total           APICounter            `json:"total"`
	Categories      map[string]APICounter `json:"categories"`
	CacheHits       int                   `json:"cache_hits"`
	CacheMisses     int                   `json:"cache_misses"`
	AuthInvocations int                   `json:"auth_invocations"`
}

// APICost records one request/consumer's costs, safely under concurrent reads.
type APICost struct {
	mu       sync.Mutex
	snapshot APICostSnapshot
}

// WithAPICost creates a cost scope without exposing any credential/repository data.
func WithAPICost(ctx context.Context) (context.Context, *APICost) {
	cost := &APICost{snapshot: APICostSnapshot{ID: costScopeSequence.Add(1), Categories: map[string]APICounter{}}}
	return context.WithValue(ctx, apiCostKey{}, cost), cost
}

// Snapshot returns independent counters. Invocations are attempted gh API calls;
// failures without response headers contribute zero observed HTTP responses.
func (c *APICost) Snapshot() APICostSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := c.snapshot
	result.Categories = make(map[string]APICounter, len(c.snapshot.Categories))
	for key, value := range c.snapshot.Categories {
		result.Categories[key] = value
	}
	return result
}

func recordCacheCost(ctx context.Context, hit bool) {
	if cost, ok := ctx.Value(apiCostKey{}).(*APICost); ok {
		cost.mu.Lock()
		defer cost.mu.Unlock()
		if hit {
			cost.snapshot.CacheHits++
		} else {
			cost.snapshot.CacheMisses++
		}
	}
}

func recordAuthCost(ctx context.Context) {
	if cost, ok := ctx.Value(apiCostKey{}).(*APICost); ok {
		cost.mu.Lock()
		cost.snapshot.AuthInvocations++
		cost.mu.Unlock()
	}
}

func withReadbackCost(ctx context.Context) context.Context {
	return context.WithValue(ctx, apiPurposeKey{}, "write_readback")
}
func withReviewCost(ctx context.Context) context.Context {
	return context.WithValue(ctx, apiPurposeKey{}, "review_validation")
}

func apiCostCategory(ctx context.Context, args []string) string {
	method, endpoint := "GET", ""
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--method", "-X":
			if i+1 < len(args) {
				i++
				method = args[i]
			}
		case "--hostname", "--header", "-H", "--input", "--field", "-F", "--raw-field", "-f":
			i++
		default:
			if strings.HasPrefix(arg, "repos/") {
				endpoint = strings.SplitN(arg, "?", 2)[0]
			}
		}
	}
	pieces := strings.Split(endpoint, "/")
	if len(pieces) < 3 {
		return "other"
	}
	if len(pieces) == 3 {
		return "preflight"
	}
	if pieces[3] == "labels" {
		return "label_setup"
	}
	if method != "GET" {
		return "writes"
	}
	if purpose, ok := ctx.Value(apiPurposeKey{}).(string); ok {
		return purpose
	}
	if pieces[3] != "issues" {
		return "other"
	}
	switch {
	case len(pieces) == 4:
		return "issue_pages"
	case len(pieces) == 5 && pieces[4] == "comments":
		return "comment_pages"
	case len(pieces) == 5:
		return "issue_detail"
	case len(pieces) == 6 && pieces[4] == "comments":
		return "comment_detail"
	case len(pieces) == 6 && pieces[5] == "comments":
		return "issue_comments"
	case len(pieces) == 6 && pieces[5] == "events":
		return "review_validation"
	default:
		return "other"
	}
}

func recordAPICost(ctx context.Context, args []string, responses int) (uint64, string) {
	category := apiCostCategory(ctx, args)
	cost, ok := ctx.Value(apiCostKey{}).(*APICost)
	if !ok {
		return 0, category
	}
	cost.mu.Lock()
	defer cost.mu.Unlock()
	value := cost.snapshot.Categories[category]
	value.Invocations++
	value.HTTPResponses += responses
	cost.snapshot.Categories[category] = value
	cost.snapshot.Total.Invocations++
	cost.snapshot.Total.HTTPResponses += responses
	return cost.snapshot.ID, category
}

// LogAPICost emits a numeric scope summary with fixed categories under TD_GH_DEBUG.
// No user text, paths, tokens or request queries are included.
func LogAPICost(cost *APICost) {
	if cost == nil || os.Getenv("TD_GH_DEBUG") != "1" {
		return
	}
	snapshot := cost.Snapshot()
	fmt.Fprintf(os.Stderr, "gh-api-cost scope=%d api_invocations=%d auth_invocations=%d gh_invocations=%d http_responses=%d cache_hits=%d cache_misses=%d", snapshot.ID, snapshot.Total.Invocations, snapshot.AuthInvocations, snapshot.Total.Invocations+snapshot.AuthInvocations, snapshot.Total.HTTPResponses, snapshot.CacheHits, snapshot.CacheMisses)
	for _, category := range []string{"preflight", "issue_pages", "comment_pages", "issue_detail", "issue_comments", "comment_detail", "label_setup", "review_validation", "write_readback", "writes", "other"} {
		value := snapshot.Categories[category]
		fmt.Fprintf(os.Stderr, " %s_calls=%d %s_http=%d", category, value.Invocations, category, value.HTTPResponses)
	}
	fmt.Fprintln(os.Stderr)
}

// LogAPICostContext logs an attached scope, if present.
func LogAPICostContext(ctx context.Context) {
	if cost, ok := ctx.Value(apiCostKey{}).(*APICost); ok {
		LogAPICost(cost)
	}
}
