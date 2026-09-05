package async_test

import (
	"testing"
	"time"

	"github.com/Newton-School/gogo/async"
)

func TestCrontabAndIntervalRules(t *testing.T) {
	start := time.Date(2026, 1, 1, 12, 3, 0, 0, time.UTC)
	rule, err := async.Crontab("*/15 9-17 * * 1-5", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	next, err := rule.Next(start)
	if err != nil || !next.Equal(time.Date(2026, 1, 1, 12, 15, 0, 0, time.UTC)) {
		t.Fatal(next, err)
	}
	for _, expression := range []string{"* * * *", "60 * * * *", "*/0 * * * *", "* * 32 * *", "* * * 13 *"} {
		if _, err := async.Crontab(expression, "UTC"); err == nil {
			t.Fatal(expression)
		}
	}
	rule = async.Every(time.Hour)
	next, err = rule.Next(start)
	if err != nil || !next.Equal(start.Add(time.Hour)) {
		t.Fatal(next, err)
	}
}

func TestCrontabDSTFoldAndGap(t *testing.T) {
	rule, err := async.Crontab("30 1 * * *", "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	first, err := rule.Next(before)
	if err != nil || first.Hour() != 5 {
		t.Fatal(first, err)
	}
	second, err := rule.Next(first)
	if err != nil || second.Hour() != 6 || second.Day() != 1 {
		t.Fatal(second, err)
	}
	rule.FoldPolicy = "first"
	next, err := rule.Next(first)
	if err != nil || next.Day() != 2 {
		t.Fatal(next, err)
	}
	rule.FoldPolicy = "second"
	next, err = rule.Next(before)
	if err != nil || next.Hour() != 6 {
		t.Fatal(next, err)
	}
	gap, _ := async.Crontab("30 2 * * *", "America/New_York")
	next, err = gap.Next(time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC))
	if err != nil || next.Day() != 9 {
		t.Fatal(next, err)
	}
}
