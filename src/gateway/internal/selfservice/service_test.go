package selfservice

import (
	"testing"
	"time"
)

func TestHandles(t *testing.T) {
	for _, path := range []string{"/v1/models", "/v1/limit", "/v1/budget", "/v1/usage/requests", "/v1/usage/summary"} {
		if !Handles(path) {
			t.Fatalf("%s should be handled locally", path)
		}
	}
	for _, path := range []string{"/healthz", "/v1/messages", "/v1/chat/completions"} {
		if Handles(path) {
			t.Fatalf("%s must be forwarded", path)
		}
	}
}

func TestBudgetLevel(t *testing.T) {
	cases := []struct {
		limit, used, reserved int64
		want                  string
	}{
		{100, 0, 0, "ok"},
		{100, 79, 0, "ok"},
		{100, 80, 0, "warn"},
		{100, 95, 5, "blocked"},
		{100, 120, 0, "blocked"},
		{0, 0, 0, "ok"},
	}
	for _, c := range cases {
		if got := budgetLevel(c.limit, c.used, c.reserved); got != c.want {
			t.Fatalf("budgetLevel(%d,%d,%d) = %s, want %s", c.limit, c.used, c.reserved, got, c.want)
		}
	}
}

func TestSummaryWindow(t *testing.T) {
	now := time.Date(2026, 10, 30, 15, 4, 0, 0, time.Local)

	from, to, group := summaryWindow("today", now)
	if !from.Equal(time.Date(2026, 10, 30, 0, 0, 0, 0, time.Local)) || !to.Equal(now) || group != "day" {
		t.Fatalf("today window = %s..%s %s", from, to, group)
	}

	from, _, _ = summaryWindow("week", now)
	if !from.Equal(time.Date(2026, 10, 24, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("week start = %s", from)
	}

	from, _, _ = summaryWindow("month", now)
	if !from.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("month start = %s", from)
	}
}

func TestParseTimeAndAtoi(t *testing.T) {
	if parseTime("2026-10-01") == nil {
		t.Fatal("date should parse")
	}
	if parseTime("not-a-date") != nil {
		t.Fatal("invalid date should be nil")
	}
	if atoiDefault("7", 20) != 7 || atoiDefault("x", 20) != 20 {
		t.Fatal("atoiDefault")
	}
}
