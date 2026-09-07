package templates

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var errDateInTime = errors.New("templates: date token in time-only format")

const maxTemporalOutput = 256 << 10

func dateFilter(timeOnly bool) Filter {
	return func(ctx context.Context, value, argument any) (any, error) {
		at, ok := templateTime(ctx, value)
		if !ok {
			return "", nil
		}
		format := "DATE_FORMAT"
		if timeOnly {
			format = "TIME_FORMAT"
		}
		if truthy(argument) {
			format = display(argument)
		}
		out, err := formatTemporal(at, format, timeOnly)
		if errors.Is(err, errDateInTime) {
			return "", nil
		}
		return out, err
	}
}

// Formats are presentation only. Calendar arithmetic uses the supplied zone;
// templateTime is the sole owner of automatic/explicit conversion before this
// formatter runs. English names/defaults are deterministic, never process-local.
func formatTemporal(at time.Time, format string, timeOnly bool) (string, error) {
	if len(format) > 4096 || !utf8.ValidString(format) || strings.ContainsRune(format, 0) || at.Year() < 1 || at.Year() > 9999 {
		return "", ErrRender
	}
	_, offset := at.Zone()
	if offset <= -86400 || offset >= 86400 {
		return "", ErrRender
	}
	format = temporalFormatName(format)
	var result strings.Builder
	// Recognize only characters not immediately preceded by a backslash,
	// then unescape literal runs once. This intentionally preserves Django's
	// multiple-backslash semantics instead of treating pairs as Go escapes.
	literalStart := 0
	previous := rune(0)
	for index, token := range format {
		if previous != '\\' && strings.ContainsRune("aAbcdDeEfFgGhHiIjlLmMnNoOPrsStTuUwWyYzZ", token) {
			result.WriteString(unescapeDateLiteral(format[literalStart:index]))
			if timeOnly && strings.ContainsRune("bcdDEFIjlLmMnNortSUwWyYz", token) {
				return "", errDateInTime
			}
			expanded := temporalToken(at, token)
			if len(expanded) > maxTemporalOutput-result.Len() {
				return "", ErrRender
			}
			result.WriteString(expanded)
			literalStart = index + utf8.RuneLen(token)
		}
		previous = token
	}
	result.WriteString(unescapeDateLiteral(format[literalStart:]))
	if result.Len() > maxTemporalOutput {
		return "", ErrRender
	}
	return result.String(), nil
}

func unescapeDateLiteral(value string) string {
	var out strings.Builder
	escaped := false
	for _, char := range value {
		if char == '\\' && !escaped {
			escaped = true
			continue
		}
		// The date-format unescape rule does not consume a newline.
		if escaped && char == '\n' {
			out.WriteByte('\\')
		}
		out.WriteRune(char)
		escaped = false
	}
	if escaped {
		out.WriteByte('\\')
	}
	return out.String()
}

func temporalFormatName(format string) string {
	switch format {
	case "DATE_FORMAT":
		return "N j, Y"
	case "DATETIME_FORMAT":
		return "N j, Y, P"
	case "SHORT_DATE_FORMAT":
		return "m/d/Y"
	case "SHORT_DATETIME_FORMAT":
		return "m/d/Y P"
	case "TIME_FORMAT":
		return "P"
	case "YEAR_MONTH_FORMAT":
		return "F Y"
	case "MONTH_DAY_FORMAT":
		return "F j"
	default:
		return format
	}
}

func temporalToken(at time.Time, token rune) string {
	integer := strconv.Itoa
	hour := at.Hour() % 12
	if hour == 0 {
		hour = 12
	}
	period := "a.m."
	if at.Hour() >= 12 {
		period = "p.m."
	}
	switch token {
	case 'a':
		return period
	case 'A':
		return at.Format("PM")
	case 'b':
		return strings.ToLower(at.Format("Jan"))
	case 'c':
		return isoTemporal(at)
	case 'd':
		return at.Format("02")
	case 'D':
		return at.Format("Mon")
	case 'e', 'T':
		name, _ := at.Zone()
		return name
	case 'E', 'F':
		return at.Month().String()
	case 'f':
		if at.Minute() == 0 {
			return integer(hour)
		}
		return integer(hour) + at.Format(":04")
	case 'g':
		return integer(hour)
	case 'G':
		return integer(at.Hour())
	case 'h':
		return at.Format("03")
	case 'H':
		return at.Format("15")
	case 'i':
		return at.Format("04")
	case 'I':
		if at.IsDST() {
			return "1"
		}
		return "0"
	case 'j':
		return integer(at.Day())
	case 'l':
		return at.Weekday().String()
	case 'L':
		if leapYear(at.Year()) {
			return "True"
		}
		return "False"
	case 'm':
		return at.Format("01")
	case 'M':
		return at.Format("Jan")
	case 'n':
		return integer(int(at.Month()))
	case 'N':
		return [...]string{"", "Jan.", "Feb.", "March", "April", "May", "June", "July", "Aug.", "Sept.", "Oct.", "Nov.", "Dec."}[at.Month()]
	case 'o':
		year, _ := at.ISOWeek()
		return integer(year)
	case 'O':
		return temporalOffset(at, false, false)
	case 'P':
		if at.Minute() == 0 && at.Hour() == 0 {
			return "midnight"
		}
		if at.Minute() == 0 && at.Hour() == 12 {
			return "noon"
		}
		return temporalToken(at, 'f') + " " + period
	case 'r':
		return at.Format("Mon, 02 Jan 2006 15:04:05") + " " + temporalOffset(at, false, false)
	case 's':
		return at.Format("05")
	case 'S':
		day := at.Day()
		if day >= 11 && day <= 13 {
			return "th"
		}
		switch day % 10 {
		case 1:
			return "st"
		case 2:
			return "nd"
		case 3:
			return "rd"
		}
		return "th"
	case 't':
		days := [...]int{0, 31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[at.Month()]
		if at.Month() == time.February && leapYear(at.Year()) {
			days++
		}
		return integer(days)
	case 'u':
		return strconv.FormatInt(int64(at.Nanosecond()/1000)+1000000, 10)[1:]
	case 'U':
		seconds := at.Unix()
		// Django int(timestamp) truncates negative fractional seconds toward
		// zero. Keep that behavior without any floating-point conversion.
		if seconds < 0 && at.Nanosecond() != 0 {
			seconds++
		}
		return strconv.FormatInt(seconds, 10)
	case 'w':
		return integer(int(at.Weekday()))
	case 'W':
		_, week := at.ISOWeek()
		return integer(week)
	case 'y':
		return at.Format("06")
	case 'Y':
		return at.Format("2006")
	case 'z':
		return integer(at.YearDay())
	case 'Z':
		_, offset := at.Zone()
		return integer(offset)
	default:
		return string(token)
	}
}

func leapYear(year int) bool { return year%4 == 0 && (year%100 != 0 || year%400 == 0) }

func isoTemporal(at time.Time) string {
	value := at.Format("2006-01-02T15:04:05")
	if ns := at.Nanosecond(); ns != 0 {
		if ns%1000 == 0 {
			value += fmt.Sprintf(".%06d", ns/1000)
		} else {
			value += fmt.Sprintf(".%09d", ns)
		}
	}
	_, offset := at.Zone()
	return value + temporalOffset(at, true, offset%60 != 0)
}

func temporalOffset(at time.Time, colon, seconds bool) string {
	_, offset := at.Zone()
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	separator := ""
	if colon {
		separator = ":"
	}
	value := fmt.Sprintf("%s%02d%s%02d", sign, offset/3600, separator, offset/60%60)
	if seconds {
		value += fmt.Sprintf("%s%02d", separator, offset%60)
	}
	return value
}
