package humanize

import (
	"context"
	"time"

	"github.com/Newton-School/gogo/core/templates"
)

// Filters returns independently owned registrations. Include "humanize" in the
// template Config.Libraries to permit {% load humanize %}; configuring filters
// never creates global registrations. Results are ordinary escaped text.
func (f *Formatter) Filters() map[string]templates.Filter {
	filters := map[string]templates.Filter{}
	for name, format := range map[string]func(context.Context, any) (string, error){"ordinal": f.Ordinal, "apnumber": f.APNumber, "intword": f.IntWord} {
		filters[name] = func(ctx context.Context, value, argument any) (any, error) {
			if argument != nil {
				return nil, ErrInvalidValue
			}
			return format(ctx, value)
		}
	}
	filters["intcomma"] = func(ctx context.Context, value, argument any) (any, error) {
		localized := true
		if argument != nil {
			var ok bool
			localized, ok = argument.(bool)
			if !ok {
				return nil, ErrInvalidValue
			}
		}
		return f.IntComma(ctx, value, localized)
	}
	for _, name := range []string{"naturalday", "naturaltime"} {
		filters[name] = func(ctx context.Context, value, argument any) (any, error) {
			ctx, _, err := f.locale(ctx)
			if err != nil {
				return nil, err
			}
			format := ""
			if argument != nil {
				var ok bool
				format, ok = argument.(string)
				if !ok || name == "naturaltime" {
					return nil, ErrInvalidValue
				}
			}
			if pointer, ok := value.(*time.Time); ok {
				if pointer == nil {
					value = nil
				} else {
					value = *pointer
				}
			}
			if value == nil {
				return displayResult(ctx, "")
			}
			at, ok := templates.TimeValue(ctx, value)
			if !ok {
				return nil, ErrInvalidValue
			}
			if name == "naturaltime" {
				return f.naturalTime(ctx, at)
			}
			return f.naturalDay(ctx, at, format)
		}
	}
	return filters
}
