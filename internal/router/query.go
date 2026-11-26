package router

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"
)

const (
	maxTextLen   = 200
	defaultLimit = 100
	maxLimit     = 1000
)

var (
	validSources    = []string{"nslkdd", "cloudtrail"}
	validSeverities = []string{"INFO", "WARN", "ERROR", "CRITICAL"}
)

// Query is a validated search request (LLD-001 §6.4.1).
type Query struct {
	From, To   time.Time
	Text       string
	EventID    string
	terms      textQuery // Text, parsed
	Sources    []string
	Severities []string
	Limit      int
}

func parseQuery(v url.Values) (Query, error) {
	var q Query
	var err error
	if q.From, err = parseTime(v, "from"); err != nil {
		return q, err
	}
	if q.To, err = parseTime(v, "to"); err != nil {
		return q, err
	}
	if !q.To.After(q.From) {
		return q, errors.New("to must be later than from")
	}

	q.Text = v.Get("q")
	if len(q.Text) > maxTextLen {
		return q, fmt.Errorf("q is longer than %d characters", maxTextLen)
	}
	if q.terms, err = parseText(q.Text); err != nil {
		return q, fmt.Errorf("q: %w", err)
	}
	q.EventID = v.Get("event_id")

	if q.Sources, err = oneOf(v, "source", validSources); err != nil {
		return q, err
	}
	if q.Severities, err = oneOf(v, "severity", validSeverities); err != nil {
		return q, err
	}

	q.Limit = defaultLimit
	if s := v.Get("limit"); s != "" {
		q.Limit, err = strconv.Atoi(s)
		if err != nil || q.Limit < 1 || q.Limit > maxLimit {
			return q, fmt.Errorf("limit must be between 1 and %d", maxLimit)
		}
	}
	return q, nil
}

func parseTime(v url.Values, key string) (time.Time, error) {
	s := v.Get(key)
	if s == "" {
		return time.Time{}, fmt.Errorf("%s is required", key)
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return t, fmt.Errorf("%s must be an RFC 3339 time, e.g. 2026-11-26T00:00:00Z", key)
	}
	return t.UTC(), nil
}

func oneOf(v url.Values, key string, allowed []string) ([]string, error) {
	vals := v[key]
	for _, s := range vals {
		if !slices.Contains(allowed, s) {
			return nil, fmt.Errorf("%s %q is not one of %v", key, s, allowed)
		}
	}
	return vals, nil
}
