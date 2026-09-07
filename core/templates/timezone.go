package templates

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/Newton-School/gogo/core/i18n"
)

type timezoneKey struct{}
type timezoneState struct {
	convert  bool
	resolver *i18n.Resolver
}

// Explicit filters must not be converted back to the request timezone by a
// later date filter or output node. This marker never exposes callable methods.
type zonedTime struct{ instant time.Time }

// TimeValue lets registered custom filters inspect a temporal value using the
// current template block's timezone rules, including an earlier explicit tz
// filter. It returns a detached time.Time; methods on arbitrary values are
// never invoked. Outside rendering an inherited locale or UTC is used.
func TimeValue(ctx context.Context, value any) (time.Time, bool) {
	if ctx == nil || ctx.Err() != nil {
		return time.Time{}, false
	}
	instant, ok := templateTime(ctx, value)
	if !ok {
		return time.Time{}, false
	}
	return copyTemplateTime(instant), true
}

func copyTemplateTime(value time.Time) time.Time {
	zone := value.Location()
	_ = zone.String()
	copy := *zone
	return value.In(&copy)
}

func (e *Engine) timezoneContext(ctx context.Context) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.config.LocaleResolver != nil {
		var err error
		ctx, err = e.config.LocaleResolver.WithLocale(ctx, i18n.Preferences{})
		if err != nil {
			return nil, ErrRender
		}
	}
	return context.WithValue(ctx, timezoneKey{}, timezoneState{convert: true, resolver: e.config.LocaleResolver}), nil
}

func currentTimeZone(ctx context.Context) string {
	locale, _ := i18n.FromContext(ctx)
	return locale.TimeZone()
}

func templateTime(ctx context.Context, value any) (time.Time, bool) {
	if explicit, ok := value.(zonedTime); ok {
		return explicit.instant, true
	}
	instant, ok := value.(time.Time)
	if !ok {
		return time.Time{}, false
	}
	state, found := ctx.Value(timezoneKey{}).(timezoneState)
	if !found || state.convert {
		locale, _ := i18n.FromContext(ctx)
		instant = locale.LocalTime(instant)
	}
	return instant, true
}

func rawTemplateTime(value any) (time.Time, bool) {
	if explicit, ok := value.(zonedTime); ok {
		return explicit.instant, true
	}
	instant, ok := value.(time.Time)
	return instant, ok
}

func overrideTimeZone(ctx context.Context, value any) (context.Context, error) {
	state, _ := ctx.Value(timezoneKey{}).(timezoneState)
	if state.resolver == nil {
		return nil, ErrRender
	}
	name, ok := value.(string)
	if value == nil {
		name, ok = state.resolver.Default().TimeZone(), true
	}
	if !ok || name == "" {
		return nil, ErrRender
	}
	result, err := state.resolver.WithLocale(ctx, i18n.Preferences{TimeZone: name})
	if err != nil {
		return nil, ErrRender
	}
	return result, nil
}

func timezoneFilters() map[string]Filter {
	filters := map[string]Filter{}
	for _, name := range []string{"localtime", "utc", "timezone"} {
		filters[name] = func(ctx context.Context, value, argument any) (any, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			instant, ok := rawTemplateTime(value)
			if !ok {
				return "", nil
			}
			if name == "timezone" {
				if argument == nil {
					return nil, ErrRender
				}
				var err error
				ctx, err = overrideTimeZone(ctx, argument)
				if err != nil {
					return nil, err
				}
			} else if argument != nil {
				return nil, ErrRender
			}
			if name == "utc" {
				instant = instant.UTC()
			} else {
				locale, _ := i18n.FromContext(ctx)
				instant = locale.LocalTime(instant)
			}
			return zonedTime{instant}, nil
		}
	}
	return filters
}

func (r *renderer) timezoneBlock(n *node, data Context, overrides map[string][]node, out *strings.Builder, depth int) error {
	previous := r.ctx
	defer func() { r.ctx = previous }()
	if n.kind == "localtime" {
		if n.arg != "on" && n.arg != "off" && n.arg != "" {
			return ErrRender
		}
		state, _ := r.ctx.Value(timezoneKey{}).(timezoneState)
		state.convert = n.arg != "off"
		r.ctx = context.WithValue(r.ctx, timezoneKey{}, state)
	} else {
		words := splitQuoted(n.arg, ' ')
		if len(words) != 1 {
			return ErrRender
		}
		value, err := r.eval(words[0], data)
		if err != nil {
			return err
		}
		r.ctx, err = overrideTimeZone(r.ctx, value)
		if err != nil {
			return err
		}
	}
	return r.render(n.children, data, overrides, out, depth+1)
}

func timezoneVariable(name string) bool {
	if name == "" || len(name) > 128 || strings.HasPrefix(name, "_") {
		return false
	}
	for index, r := range name {
		if !unicode.IsLetter(r) && (index == 0 || !unicode.IsDigit(r)) && r != '_' {
			return false
		}
	}
	return true
}
