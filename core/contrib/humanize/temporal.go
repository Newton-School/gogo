package humanize

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/templates"
)

var ErrUnavailable = errors.New("humanize: display clock unavailable")

func validTime(at time.Time) bool {
	_, offset := at.Zone()
	return at.Year() >= 1 && at.Year() <= 9999 && offset > -86400 && offset < 86400
}

func (f *Formatter) now(ctx context.Context) (at time.Time, err error) {
	defer func() {
		if recover() != nil {
			at, err = time.Time{}, ErrUnavailable
		}
		if ctx.Err() != nil {
			at, err = time.Time{}, ctx.Err()
		}
	}()
	at = f.clock()
	if !validTime(at) {
		return time.Time{}, ErrInvalidValue
	}
	zone := at.Location()
	_ = zone.String()
	copy := *zone
	return at.In(&copy), nil
}

// NaturalDay compares calendar dates in the current request/task zone.
func (f *Formatter) NaturalDay(ctx context.Context, at time.Time, format string) (string, error) {
	ctx, locale, err := f.locale(ctx)
	if err != nil {
		return "", err
	}
	return f.naturalDay(ctx, locale.LocalTime(at), format)
}

func (f *Formatter) naturalDay(ctx context.Context, at time.Time, format string) (string, error) {
	if !validTime(at) {
		return "", ErrInvalidValue
	}
	now, err := f.now(ctx)
	if err != nil {
		return "", err
	}
	now = now.In(at.Location())
	if !validTime(now) {
		return "", ErrInvalidValue
	}
	date := func(at time.Time) time.Time { return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC) }
	days := (date(at).Unix() - date(now).Unix()) / 86400
	switch days {
	case -1:
		return f.text(ctx, "", "yesterday")
	case 0:
		return f.text(ctx, "", "today")
	case 1:
		return f.text(ctx, "", "tomorrow")
	}
	if format == "" {
		format = "DATE_FORMAT"
	}
	// The date has already been selected in the display zone. Legacy r/U
	// formats anchor those fields at project-default midnight, never at the
	// source clock time or the process's ambient timezone.
	dateContext, err := f.resolver.WithLocale(ctx, i18n.Preferences{TimeZone: f.resolver.Default().TimeZone()})
	if err != nil {
		return "", err
	}
	out, err := templates.FormatCalendarDate(dateContext, at, format)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return out, err
}

// NaturalTime reports elapsed sub-day units, then calendar years/months and up
// to two adjacent nonzero units for longer spans. No time.Duration overflow or
// floating-point arithmetic is used, including dates more than 292 years apart.
func (f *Formatter) NaturalTime(ctx context.Context, at time.Time) (string, error) {
	ctx, locale, err := f.locale(ctx)
	if err != nil {
		return "", err
	}
	return f.naturalTime(ctx, locale.LocalTime(at))
}

func wholeSeconds(first, last time.Time) int64 {
	seconds := last.Unix() - first.Unix()
	if last.Nanosecond() < first.Nanosecond() {
		seconds--
	}
	return seconds
}

func (f *Formatter) naturalTime(ctx context.Context, at time.Time) (string, error) {
	if !validTime(at) {
		return "", ErrInvalidValue
	}
	now, err := f.now(ctx)
	if err != nil {
		return "", err
	}
	now = now.In(at.Location())
	if !validTime(now) {
		return "", ErrInvalidValue
	}
	first, last := at, now
	direction, suffix := "past", " ago"
	if at.After(now) {
		first, last = now, at
		direction, suffix = "future", " from now"
	}
	seconds := wholeSeconds(first, last)
	if seconds == 0 {
		return f.text(ctx, "", "now")
	}
	if seconds < 86400 {
		count, unit, singular := seconds, "second", "a second"
		if seconds >= 3600 {
			count, unit, singular = seconds/3600, "hour", "an hour"
		} else if seconds >= 60 {
			count, unit, singular = seconds/60, "minute", "a minute"
		}
		pattern, err := f.plural(ctx, "naturaltime-"+direction, singular+suffix, "{value}\u00a0"+unit+"s"+suffix, count)
		if err != nil {
			return "", err
		}
		out, err := insertValue(pattern, strconv.FormatInt(count, 10))
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return out, err
	}
	parts := calendarParts(first, last)
	units := []string{"year", "month", "week", "day", "hour", "minute"}
	values := []string{}
	started := false
	for i, count := range parts {
		if count == 0 {
			if started {
				break
			}
			continue
		}
		started = true
		pattern, err := f.plural(ctx, "naturaltime-"+direction, "{value} "+units[i], "{value} "+units[i]+"s", count)
		if err != nil {
			return "", err
		}
		out, err := insertValue(pattern, strconv.FormatInt(count, 10))
		if err != nil {
			return "", err
		}
		values = append(values, strings.ReplaceAll(out, " ", "\u00a0"))
		if len(values) == 2 {
			break
		}
	}
	if len(values) == 0 {
		pattern, err := f.plural(ctx, "naturaltime-"+direction, "{value} minute", "{value} minutes", 0)
		if err != nil {
			return "", err
		}
		out, err := insertValue(pattern, "0")
		if err != nil {
			return "", err
		}
		values = append(values, strings.ReplaceAll(out, " ", "\u00a0"))
	}
	separator, err := f.text(ctx, "naturaltime-separator", ", ")
	if err != nil {
		return "", err
	}
	pattern, err := f.text(ctx, "naturaltime-"+direction, "{value}"+suffix)
	if err != nil {
		return "", err
	}
	out, err := insertValue(pattern, strings.Join(values, separator))
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return out, err
}

func calendarParts(first, last time.Time) [6]int64 {
	// Civil arithmetic intentionally counts displayed calendar units across
	// offset changes. Construct UTC copies of those fields, not converted
	// instants, so no host zone or implicit DST normalization is involved.
	civil := func(at time.Time) time.Time {
		return time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), at.Minute(), at.Second(), at.Nanosecond(), time.UTC)
	}
	first, last = civil(first), civil(last)
	months := (last.Year()-first.Year())*12 + int(last.Month()-first.Month())
	clock := func(at time.Time) int64 {
		return int64(at.Hour()*3600+at.Minute()*60+at.Second())*1e9 + int64(at.Nanosecond())
	}
	if first.Day() > last.Day() || first.Day() == last.Day() && clock(first) > clock(last) {
		months--
	}
	if months < 0 {
		months = 0
	}
	pivot := first
	if months > 0 {
		year := first.Year() + (int(first.Month())-1+months)/12
		month := time.Month((int(first.Month())-1+months)%12 + 1)
		// The display compatibility rule clamps the pivot using the historical
		// humanize month table (February 28); it does not change stored dates.
		monthDays := [12]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
		pivot = time.Date(year, month, min(first.Day(), monthDays[int(month)-1]), first.Hour(), first.Minute(), first.Second(), 0, time.UTC)
	}
	remaining := wholeSeconds(pivot, last)
	if remaining < 0 {
		remaining = 0
	}
	parts := [6]int64{int64(months / 12), int64(months % 12)}
	for i, scale := range []int64{604800, 86400, 3600, 60} {
		parts[i+2] = remaining / scale
		remaining %= scale
	}
	return parts
}
