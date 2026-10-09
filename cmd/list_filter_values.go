package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type listPointsRange struct{ min, max *int }

func parseListPointsRange(raw string) (listPointsRange, error) {
	s := strings.TrimSpace(raw)
	number := func(s string) (*int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid points filter %q; use N, >=N, <=N or N-N with nonnegative integers", raw)
		}
		return &n, nil
	}
	var r listPointsRange
	var err error
	switch {
	case strings.HasPrefix(s, ">="):
		r.min, err = number(strings.TrimPrefix(s, ">="))
	case strings.HasPrefix(s, "<="):
		r.max, err = number(strings.TrimPrefix(s, "<="))
	case strings.Contains(s, "-"):
		parts := strings.Split(s, "-")
		if len(parts) != 2 {
			return r, fmt.Errorf("invalid points filter %q", raw)
		}
		r.min, err = number(parts[0])
		if err == nil {
			r.max, err = number(parts[1])
		}
	default:
		r.min, err = number(s)
		r.max = r.min
	}
	if err != nil {
		return r, err
	}
	if r.min != nil && r.max != nil && *r.min > *r.max {
		return r, fmt.Errorf("points range minimum exceeds maximum")
	}
	return r, nil
}

func (r listPointsRange) matches(n int) bool {
	return (r.min == nil || n >= *r.min) && (r.max == nil || n <= *r.max)
}

type listDateRange struct{ after, before time.Time }

// Retain list's UTC timestamp bounds, including its inclusive end boundary.
// Calendar due/defer and TDQ day predicates have their own local-day contract.
func parseListDateRange(raw string) (listDateRange, error) {
	s := strings.TrimSpace(raw)
	parse := func(s string) (time.Time, error) {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			return d, fmt.Errorf("invalid date filter %q; use YYYY-MM-DD, after:DATE, before:DATE or DATE..DATE", raw)
		}
		return d, nil
	}
	var r listDateRange
	var err error
	switch {
	case strings.HasPrefix(s, "after:"):
		r.after, err = parse(strings.TrimPrefix(s, "after:"))
	case strings.HasPrefix(s, "before:"):
		r.before, err = parse(strings.TrimPrefix(s, "before:"))
	case strings.Contains(s, ".."):
		parts := strings.Split(s, "..")
		if len(parts) != 2 || (parts[0] == "" && parts[1] == "") {
			return r, fmt.Errorf("invalid date range %q", raw)
		}
		if parts[0] != "" {
			r.after, err = parse(parts[0])
		}
		if err == nil && parts[1] != "" {
			r.before, err = parse(parts[1])
		}
	default:
		r.after, err = parse(s)
		if err == nil {
			r.before = r.after.Add(24 * time.Hour)
		}
	}
	if err != nil {
		return r, err
	}
	if !r.after.IsZero() && !r.before.IsZero() && r.after.After(r.before) {
		return r, fmt.Errorf("date range start exceeds end")
	}
	return r, nil
}

func (r listDateRange) matches(t time.Time) bool {
	return (r.after.IsZero() || !t.Before(r.after)) && (r.before.IsZero() || !t.After(r.before))
}

func (r listDateRange) matchesOptional(t *time.Time) bool {
	if r.after.IsZero() && r.before.IsZero() {
		return true
	}
	return t != nil && r.matches(*t)
}
