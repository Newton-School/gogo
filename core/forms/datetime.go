package forms

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/i18n"
)

func formLocale(ctx context.Context) i18n.Locale {
	locale, _ := i18n.FromContext(ctx)
	return locale // The zero locale uses UTC; there is no process-local fallback.
}

func (f Field) cleanDateTime(ctx context.Context, raw any) (any, error) {
	if instant, ok := raw.(time.Time); ok {
		if instant.UTC().Year() < 1 || instant.UTC().Year() > 9999 {
			return nil, f.failure("invalid", "Enter a valid date and time.")
		}
		return instant.UTC(), nil
	}
	input := stringValue(raw)
	if len(input) > 4096 {
		return nil, f.failure("invalid", "Enter a valid date and time.")
	}
	layouts := f.InputFormats
	if len(layouts) == 0 {
		layouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04"}
	}
	if len(layouts) > 64 {
		return nil, f.failure("invalid", "Enter a valid date and time.")
	}
	for _, layout := range layouts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(layout) > 256 {
			return nil, f.failure("invalid", "Enter a valid date and time.")
		}
		// Numeric offsets disambiguate an instant. Named zone abbreviations
		// depend on host/season and must not be fabricated by time.Parse.
		if strings.Contains(layout, "MST") {
			continue
		}
		parsed, err := time.Parse(layout, input)
		if err != nil {
			continue
		}
		if strings.Contains(layout, "-07") || strings.Contains(layout, "Z07") {
			return f.cleanDateTime(ctx, parsed)
		}
		return f.resolveDateTime(ctx, parsed)
	}
	return nil, f.failure("invalid", "Enter a valid date and time.")
}

func (f Field) resolveDateTime(ctx context.Context, parsed time.Time) (any, error) {
	instant, err := formLocale(ctx).ResolveLocal(ctx, i18n.LocalDateTime{
		Year: parsed.Year(), Month: parsed.Month(), Day: parsed.Day(),
		Hour: parsed.Hour(), Minute: parsed.Minute(), Second: parsed.Second(), Nanosecond: parsed.Nanosecond(),
	})
	if errors.Is(err, i18n.ErrAmbiguousTime) || errors.Is(err, i18n.ErrNonexistentTime) {
		return nil, f.failure("ambiguous_timezone", "This date and time is ambiguous or does not exist in the current time zone.")
	}
	if errors.Is(err, i18n.ErrInvalidTime) {
		return nil, f.failure("invalid", "Enter a valid date and time.")
	}
	if err != nil {
		return nil, err
	}
	return instant, nil
}
