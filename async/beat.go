package async

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ScheduleRule expresses portable interval or five-field crontab schedules.
// Solar/custom calendar rules use a registered Calendar implementation in Beat.
type ScheduleRule struct {
	Kind       string        `json:"kind"`
	Interval   time.Duration `json:"interval,omitempty"`
	Expression string        `json:"expression,omitempty"`
	Timezone   string        `json:"timezone"`
	FixedDelay bool          `json:"fixed_delay,omitempty"`
	FoldPolicy string        `json:"fold_policy"`
	GapPolicy  string        `json:"gap_policy"`
}

func Every(interval time.Duration) ScheduleRule {
	return ScheduleRule{Kind: "interval", Interval: interval, Timezone: "UTC", FoldPolicy: "both", GapPolicy: "skip"}
}
func Crontab(expression, timezone string) (ScheduleRule, error) {
	rule := ScheduleRule{Kind: "cron", Expression: expression, Timezone: timezone, FoldPolicy: "both", GapPolicy: "skip"}
	return rule, rule.Validate()
}
func (r ScheduleRule) Validate() error {
	if r.Timezone == "" {
		return ErrInvalid
	}
	if _, err := time.LoadLocation(r.Timezone); err != nil {
		return ErrInvalid
	}
	if r.FoldPolicy != "both" && r.FoldPolicy != "first" && r.FoldPolicy != "second" {
		return ErrInvalid
	}
	if r.GapPolicy != "skip" {
		return ErrInvalid
	}
	if r.Kind == "interval" {
		if r.Interval < time.Millisecond {
			return ErrInvalid
		}
		return nil
	}
	if r.Kind == "cron" {
		_, err := parseCron(r.Expression)
		return err
	}
	if !namePattern.MatchString(r.Kind) {
		return ErrInvalid
	}
	return nil
}

type Calendar interface {
	Next(ScheduleRule, time.Time) (time.Time, error)
}

func (r ScheduleRule) Next(after time.Time) (time.Time, error) {
	if err := r.Validate(); err != nil {
		return time.Time{}, err
	}
	if r.Kind == "interval" {
		return after.Add(r.Interval).UTC(), nil
	}
	if r.Kind != "cron" {
		return time.Time{}, ErrUnavailable
	}
	fields, err := parseCron(r.Expression)
	if err != nil {
		return time.Time{}, err
	}
	location, _ := time.LoadLocation(r.Timezone)
	for next, limit := after.UTC().Truncate(time.Minute).Add(time.Minute), after.AddDate(5, 0, 0); next.Before(limit); next = next.Add(time.Minute) {
		local := next.In(location)
		dom := fields[2][local.Day()]
		dow := fields[4][int(local.Weekday())]
		day := dom && dow
		parts := strings.Fields(r.Expression)
		if parts[2] != "*" && parts[4] != "*" {
			day = dom || dow
		}
		if !fields[0][local.Minute()] || !fields[1][local.Hour()] || !day || !fields[3][int(local.Month())] {
			continue
		}
		if r.FoldPolicy != "both" {
			// Compare equal local wall-clock minutes around the UTC candidate. The
			// bounded window also covers half-hour and two-hour DST transitions.
			duplicateBefore, duplicateAfter := false, false
			for offset := time.Minute; offset <= 3*time.Hour; offset += time.Minute {
				duplicateBefore = duplicateBefore || sameWallMinute(local, next.Add(-offset).In(location))
				duplicateAfter = duplicateAfter || sameWallMinute(local, next.Add(offset).In(location))
			}
			if r.FoldPolicy == "first" && duplicateBefore || r.FoldPolicy == "second" && duplicateAfter {
				continue
			}
		}
		return next, nil
	}
	return time.Time{}, fmt.Errorf("%w: no occurrence within five years", ErrInvalid)
}
func sameWallMinute(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay() && a.Hour() == b.Hour() && a.Minute() == b.Minute()
}
func parseCron(expression string) ([5]map[int]bool, error) {
	var fields [5]map[int]bool
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return fields, ErrInvalid
	}
	mins := []int{0, 0, 1, 1, 0}
	maxs := []int{59, 23, 31, 12, 7}
	for i, part := range parts {
		fields[i] = map[int]bool{}
		for _, piece := range strings.Split(part, ",") {
			base, stepText, hasStep := strings.Cut(piece, "/")
			step := 1
			var err error
			if hasStep {
				step, err = strconv.Atoi(stepText)
				if err != nil || step < 1 {
					return fields, ErrInvalid
				}
			}
			start, end := mins[i], maxs[i]
			if base != "*" {
				a, b, rangeFound := strings.Cut(base, "-")
				start, err = strconv.Atoi(a)
				if err != nil {
					return fields, ErrInvalid
				}
				end = start
				if rangeFound {
					end, err = strconv.Atoi(b)
					if err != nil {
						return fields, ErrInvalid
					}
				} else if hasStep {
					end = maxs[i]
				}
			}
			if start < mins[i] || end > maxs[i] || start > end {
				return fields, ErrInvalid
			}
			for n := start; n <= end; n += step {
				value := n
				if i == 4 && n == 7 {
					value = 0
				}
				fields[i][value] = true
			}
		}
	}
	return fields, nil
}

type PeriodicSchedule struct {
	ID           string       `json:"id"`
	Signature    Signature    `json:"signature"`
	Rule         ScheduleRule `json:"rule"`
	Enabled      bool         `json:"enabled"`
	NextDue      time.Time    `json:"next_due"`
	EndAt        time.Time    `json:"end_at,omitempty"`
	Misfire      string       `json:"misfire"`
	CatchUpLimit int          `json:"catch_up_limit"`
	Overlap      string       `json:"overlap"`
	LastTaskID   string       `json:"last_task_id,omitempty"`
	Revision     uint64       `json:"revision"`
	Fence        uint64       `json:"fence"`
	Owner        string       `json:"owner"`
	LeaseUntil   time.Time    `json:"lease_until"`
	ObservedAt   time.Time    `json:"observed_at"`
}

func (p PeriodicSchedule) Validate() error {
	if !idPattern.MatchString(p.ID) || p.NextDue.IsZero() || p.CatchUpLimit < 1 || p.CatchUpLimit > 1000 || (p.Misfire != "skip" && p.Misfire != "coalesce" && p.Misfire != "catchup") || (p.Overlap != "allow" && p.Overlap != "skip") {
		return ErrInvalid
	}
	return p.Rule.Validate()
}

type PeriodicStore interface {
	UpsertSchedule(context.Context, PeriodicSchedule, uint64) error
	LeaseSchedules(context.Context, string, int, time.Duration) ([]PeriodicSchedule, error)
	CommitOccurrence(context.Context, PeriodicSchedule, time.Time, string, []Intent) error
	Disable(context.Context, string, uint64) error
	List(context.Context, int) ([]PeriodicSchedule, error)
	IntentStore
}

type Beat struct {
	Client    *Client
	Store     PeriodicStore
	ID        string
	Lease     time.Duration
	BatchSize int
	Calendars map[string]Calendar
}

func (b *Beat) next(rule ScheduleRule, after time.Time) (time.Time, error) {
	if rule.Kind == "cron" || rule.Kind == "interval" {
		return rule.Next(after)
	}
	calendar := b.Calendars[rule.Kind]
	if calendar == nil {
		return time.Time{}, ErrUnavailable
	}
	return calendar.Next(rule, after)
}
func (b *Beat) Tick(ctx context.Context) error {
	if b.Client == nil || b.Store == nil || b.ID == "" {
		return ErrInvalid
	}
	lease := b.Lease
	if lease == 0 {
		lease = 30 * time.Second
	}
	limit := b.BatchSize
	if limit == 0 {
		limit = 100
	}
	schedules, err := b.Store.LeaseSchedules(ctx, b.ID, limit, lease)
	if err != nil {
		return err
	}
	for _, schedule := range schedules {
		if err := schedule.Validate(); err != nil {
			return err
		}
		now := schedule.ObservedAt
		due := schedule.NextDue
		next, err := b.next(schedule.Rule, due)
		if err != nil {
			return err
		}
		if schedule.Rule.FixedDelay {
			next, err = b.next(schedule.Rule, now)
			if err != nil {
				return err
			}
		}
		skipped := false
		if schedule.Overlap == "skip" && schedule.LastTaskID != "" {
			record, err := b.Client.config.Results.Lookup(ctx, schedule.LastTaskID)
			if err != nil {
				return err
			}
			skipped = !record.State.Terminal()
		}
		if schedule.Misfire == "skip" && !next.After(now) {
			skipped = true
			next, err = b.next(schedule.Rule, now)
			if err != nil {
				return err
			}
		}
		if schedule.Misfire == "coalesce" && !next.After(now) {
			next, err = b.next(schedule.Rule, now)
			if err != nil {
				return err
			}
		}
		var intents []Intent
		lastID := schedule.LastTaskID
		for occurrence := 0; !skipped; occurrence++ {
			if !schedule.EndAt.IsZero() && !due.Before(schedule.EndAt) {
				break
			}
			id := StableID(schedule.ID, due.UTC().Format(time.RFC3339Nano))
			signature := schedule.Signature.Set(WithID(id))
			e, err := b.Client.prepare(signature)
			if err != nil {
				return err
			}
			e.CreatedAt = now
			e.RootID = id
			if err := b.Client.authorize(ctx, "enqueue", e.Scope, id); err != nil {
				return err
			}
			intents = append(intents, dispatchIntent(schedule.ID, e))
			lastID = id
			if schedule.Misfire != "catchup" || next.After(now) || occurrence+1 >= schedule.CatchUpLimit {
				break
			}
			due = next
			next, err = b.next(schedule.Rule, due)
			if err != nil {
				return err
			}
		}
		if err := b.Store.CommitOccurrence(ctx, schedule, next, lastID, intents); err != nil {
			return err
		}
	}
	return nil
}
func (b *Beat) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := b.Tick(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
