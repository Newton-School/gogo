package http

import (
	"context"
	"net/http"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// DateDetailViewOptions narrows ordinary detail lookup to one calendar day.
// DateField is an explicit local stored Date or DateTime field; filtering never
// adds it to Fields or PolicyFields. Date returns exactly YYYY-MM-DD, or exactly
// ErrInvalidLookup for malformed input. Other callback failures are operational.
type DateDetailViewOptions struct {
	DetailViewOptions
	DateField string
	Date      func(*http.Request) (string, error)
	// AllowFuture permits future calendar days. When false, later instants on
	// today's date remain eligible: this is not an exact publication-time gate.
	AllowFuture bool
	// Clock defaults to time.Now. It runs once per eligible read when future
	// days are disabled, never for OPTIONS. It must be read-only and bounded.
	Clock func() time.Time
}

// NewDateDetailView creates a scoped GET/HEAD dated detail. The configured
// template locale resolver also selects the query's calendar timezone. Without
// a resolver, the inherited locale or UTC applies; the host timezone is unused.
// DateTime days use separately resolved midnights, not a fixed 24-hour duration.
// Ambiguous, nonexistent or unrepresentable day boundaries return 404.
func NewDateDetailView(options DateDetailViewOptions) (http.Handler, error) {
	date, key, clock, allowFuture := options.Date, options.Key, options.Clock, options.AllowFuture
	if date == nil || key == nil {
		return nil, ErrGenericConfiguration
	}
	if clock == nil {
		clock = time.Now
	}
	// One private resolver value drives both selection and rendering, even if
	// application code later assigns another resolver through the original pointer.
	resolver := options.Templates.LocaleResolver
	if resolver != nil {
		copy := *resolver
		resolver = &copy
		options.Templates.LocaleResolver = resolver
	}
	model, err := newGenericModel(options.ModelReadOptions)
	if err != nil {
		return nil, err
	}
	field, found := model.schema.Field(options.DateField)
	if !found || !models.ValidIdentifier(options.DateField) || !field.IsStored() || !genericModelFieldSupported(field, true) || field.Kind != models.Date && field.Kind != models.DateTime {
		return nil, ErrGenericConfiguration
	}
	renderer, err := newGenericTemplate(options.TemplateViewOptions)
	if err != nil {
		return nil, err
	}
	return newReadView(model.readOptions(options.ReadViewOptions), func(call *readViewCall) (readViewResult, error) {
		ctx := call.base.Context()
		if resolver != nil {
			var err error
			ctx, err = resolver.WithLocale(ctx, i18n.Preferences{})
			if err != nil {
				return readViewResult{}, ErrUnavailable
			}
		}
		if !genericContextOK(ctx) {
			return readViewResult{}, ErrUnavailable
		}
		// Keep selected locale consistent for scope, request hooks, template
		// processors and final grants without changing the original request.
		call.base = call.base.WithContext(ctx)
		query, err := model.query(ctx)
		if err != nil {
			return readViewResult{}, err
		}
		raw, err := date(call.request())
		if !genericContextOK(ctx) {
			return readViewResult{}, ErrUnavailable
		}
		if err != nil {
			if err == ErrInvalidLookup {
				return readViewResult{}, ErrNotFound
			}
			return readViewResult{}, ErrUnavailable
		}
		day, ok := genericCalendarDate(raw)
		if !ok {
			return readViewResult{}, ErrNotFound
		}
		locale, _ := i18n.FromContext(ctx)
		if !genericContextOK(ctx) {
			return readViewResult{}, ErrUnavailable
		}
		if !allowFuture {
			// Normalize/copy the clock's location before another callback can
			// mutate its returned pointer; only the instant selects today's date.
			now, valid := genericModelTemporal(models.DateTime, clock(), genericLookupTextBytes)
			if !genericContextOK(ctx) || !valid {
				return readViewResult{}, ErrUnavailable
			}
			local := locale.LocalTime(now.(time.Time))
			if local.Year() < 1 || local.Year() > 9999 {
				return readViewResult{}, ErrUnavailable
			}
			if raw > local.Format("2006-01-02") {
				return readViewResult{}, ErrNotFound
			}
		}
		predicate, err := genericDatePredicate(ctx, locale, field, day, raw)
		if err != nil {
			return readViewResult{}, err
		}
		return model.detailResponse(call, renderer, key, query.Filter(predicate))
	})
}

func genericCalendarDate(raw string) (time.Time, bool) {
	if len(raw) != 10 || raw[4] != '-' || raw[7] != '-' {
		return time.Time{}, false
	}
	for i := range raw {
		if i != 4 && i != 7 && (raw[i] < '0' || raw[i] > '9') {
			return time.Time{}, false
		}
	}
	value, ok := genericModelTemporal(models.Date, raw, 10)
	if !ok {
		return time.Time{}, false
	}
	return value.(time.Time), true
}

func genericDatePredicate(ctx context.Context, locale i18n.Locale, field models.Field, day time.Time, raw string) (db.Predicate, error) {
	if field.Kind == models.Date {
		return orm.Q(field.Name, raw), nil
	}
	resolve := func(date time.Time) (time.Time, error) {
		instant, err := locale.ResolveLocal(ctx, i18n.LocalDateTime{Year: date.Year(), Month: date.Month(), Day: date.Day()})
		if !genericContextOK(ctx) {
			return time.Time{}, ErrUnavailable
		}
		if err == i18n.ErrInvalidTime || err == i18n.ErrAmbiguousTime || err == i18n.ErrNonexistentTime {
			return time.Time{}, ErrNotFound
		}
		if err != nil {
			return time.Time{}, ErrUnavailable
		}
		return instant, nil
	}
	start, err := resolve(day)
	if err != nil {
		return db.Predicate{}, err
	}
	end, err := resolve(day.AddDate(0, 0, 1))
	if err != nil {
		return db.Predicate{}, err
	}
	return orm.And(orm.Q(field.Name+"__gte", start), orm.Q(field.Name+"__lt", end)), nil
}
