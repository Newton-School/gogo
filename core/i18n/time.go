package i18n

import (
	"context"
	"errors"
	"time"
)

var ErrInvalidTime = errors.New("i18n: invalid local date and time")
var ErrAmbiguousTime = errors.New("i18n: local date and time occurs more than once")
var ErrNonexistentTime = errors.New("i18n: local date and time does not exist")

// LocalDateTime is wall-clock data, not an instant. ResolveLocal requires one
// and only one matching instant in the locale's timezone; it never chooses an
// arbitrary side of a daylight-saving fold or normalizes a nonexistent time.
type LocalDateTime struct {
	Year, Day                        int
	Month                            time.Month
	Hour, Minute, Second, Nanosecond int
}

// ResolveLocal returns a UTC instant for supported IANA wall-clock data. Years
// and the resulting UTC instant must lie within 1..9999. Zone offsets are bounded
// to +/-24h and at most 64 transition periods are examined around that date;
// unsupported pathological zone data fails, rather than selecting approximately.
func (l Locale) ResolveLocal(ctx context.Context, wall LocalDateTime) (time.Time, error) {
	if ctx == nil {
		return time.Time{}, ErrInvalidTime
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	if wall.Year < 1 || wall.Year > 9999 || wall.Month < time.January || wall.Month > time.December || wall.Day < 1 || wall.Day > 31 || wall.Hour < 0 || wall.Hour > 23 || wall.Minute < 0 || wall.Minute > 59 || wall.Second < 0 || wall.Second > 59 || wall.Nanosecond < 0 || wall.Nanosecond >= 1e9 {
		return time.Time{}, ErrInvalidTime
	}
	naive := time.Date(wall.Year, wall.Month, wall.Day, wall.Hour, wall.Minute, wall.Second, wall.Nanosecond, time.UTC)
	if naive.Month() != wall.Month || naive.Day() != wall.Day {
		return time.Time{}, ErrInvalidTime
	}
	location := l.Location()
	const maximumOffset = 24 * time.Hour
	start, end := naive.Add(-maximumOffset), naive.Add(maximumOffset)
	seen := map[int]bool{}
	var matched time.Time
	found := false
	periods := 0
	for cursor := start; !cursor.After(end); {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		periods++
		if periods > 64 {
			return time.Time{}, ErrInvalidTime
		}
		current := cursor.In(location)
		_, offset := current.Zone()
		if offset < -86400 || offset > 86400 {
			return time.Time{}, ErrInvalidTime
		}
		if !seen[offset] {
			seen[offset] = true
			candidate := naive.Add(-time.Duration(offset) * time.Second)
			local := candidate.In(location)
			if local.Year() == wall.Year && local.Month() == wall.Month && local.Day() == wall.Day && local.Hour() == wall.Hour && local.Minute() == wall.Minute && local.Second() == wall.Second && local.Nanosecond() == wall.Nanosecond {
				if found && !candidate.Equal(matched) {
					return time.Time{}, ErrAmbiguousTime
				}
				matched, found = candidate, true
			}
		}
		_, boundary := current.ZoneBounds()
		if boundary.IsZero() || boundary.After(end) {
			break
		}
		if !boundary.After(cursor) {
			return time.Time{}, ErrInvalidTime
		}
		cursor = boundary.UTC()
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	if !found {
		return time.Time{}, ErrNonexistentTime
	}
	if matched.Year() < 1 || matched.Year() > 9999 {
		return time.Time{}, ErrInvalidTime
	}
	return matched, nil
}
