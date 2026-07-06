package humanize

import (
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Newton-School/gogo/app"
)

type Options struct {
	Now func() time.Time
}

type Humanizer struct {
	now func() time.Time
}

func AppConfig() app.Config {
	return app.BaseConfig{AppName: "gogo.contrib.humanize", AppLabel: "humanize", AppPath: "contrib/humanize", AppVerboseName: "Humanize"}
}

func Filters(options Options) Humanizer {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return Humanizer{now: now}
}

func TemplateFilters(options Options) template.FuncMap {
	filters := Filters(options)
	return template.FuncMap{
		"apnumber":    filters.Apnumber,
		"intcomma":    filters.Intcomma,
		"intword":     filters.Intword,
		"naturalday":  filters.Naturalday,
		"naturaltime": filters.Naturaltime,
		"ordinal":     filters.Ordinal,
	}
}

func (h Humanizer) Apnumber(value any) string {
	number, ok := toInt(value)
	if !ok {
		return fmt.Sprint(value)
	}
	words := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}
	if number >= 0 && number < len(words) {
		return words[number]
	}
	return strconv.Itoa(number)
}

func (h Humanizer) Intcomma(value any) string {
	number, ok := toInt(value)
	if !ok {
		return fmt.Sprint(value)
	}
	sign := ""
	if number < 0 {
		sign = "-"
		number = -number
	}
	raw := strconv.Itoa(number)
	var parts []string
	for len(raw) > 3 {
		parts = append([]string{raw[len(raw)-3:]}, parts...)
		raw = raw[:len(raw)-3]
	}
	parts = append([]string{raw}, parts...)
	return sign + strings.Join(parts, ",")
}

func (h Humanizer) Intword(value any) string {
	number, ok := toFloat(value)
	if !ok {
		return fmt.Sprint(value)
	}
	units := []struct {
		value float64
		name  string
	}{
		{1_000_000_000_000, "trillion"},
		{1_000_000_000, "billion"},
		{1_000_000, "million"},
		{1_000, "thousand"},
	}
	for _, unit := range units {
		if math.Abs(number) >= unit.value {
			return trimFloat(number/unit.value) + " " + unit.name
		}
	}
	return trimFloat(number)
}

func (h Humanizer) Naturalday(value any) string {
	when, ok := toTime(value)
	if !ok {
		return fmt.Sprint(value)
	}
	now := h.now()
	y1, m1, d1 := when.Date()
	y2, m2, d2 := now.Date()
	whenDay := time.Date(y1, m1, d1, 0, 0, 0, 0, now.Location())
	nowDay := time.Date(y2, m2, d2, 0, 0, 0, 0, now.Location())
	switch diff := int(whenDay.Sub(nowDay).Hours() / 24); diff {
	case -1:
		return "yesterday"
	case 0:
		return "today"
	case 1:
		return "tomorrow"
	default:
		return when.Format("Jan 2, 2006")
	}
}

func (h Humanizer) Naturaltime(value any) string {
	when, ok := toTime(value)
	if !ok {
		return fmt.Sprint(value)
	}
	delta := h.now().Sub(when)
	suffix := "ago"
	if delta < 0 {
		delta = -delta
		suffix = "from now"
	}
	if delta < time.Minute {
		return "now"
	}
	amount, unit := durationUnit(delta)
	return fmt.Sprintf("%d %s %s", amount, plural(unit, amount), suffix)
}

func (h Humanizer) Ordinal(value any) string {
	number, ok := toInt(value)
	if !ok {
		return fmt.Sprint(value)
	}
	suffix := "th"
	if number%100 < 11 || number%100 > 13 {
		switch number % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(number) + suffix
}

func durationUnit(delta time.Duration) (int, string) {
	switch {
	case delta >= 24*time.Hour:
		return int(delta / (24 * time.Hour)), "day"
	case delta >= time.Hour:
		return int(delta / time.Hour), "hour"
	default:
		return int(delta / time.Minute), "minute"
	}
}

func plural(unit string, amount int) string {
	if amount == 1 {
		return unit
	}
	return unit + "s"
}

func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(typed)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func toTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, true
	case *time.Time:
		if typed == nil {
			return time.Time{}, false
		}
		return *typed, true
	default:
		return time.Time{}, false
	}
}

func trimFloat(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", value), "0"), ".")
}
