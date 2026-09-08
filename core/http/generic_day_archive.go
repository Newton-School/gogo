package http

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/templates"
)

// DayArchiveViewOptions declares a scoped, paginated calendar-day archive.
// DateField is a local stored Date or DateTime; Date returns YYYY-MM-DD or
// exactly ErrInvalidLookup for malformed input. Neither implicitly exposes a
// field in object_list or loads it into an object-policy record.
type DayArchiveViewOptions struct {
	ListViewOptions
	DateField string
	Date      func(*http.Request) (string, error)
	// AllowEmpty permits an empty first page and calendar-adjacent navigation.
	// Otherwise an empty archive is 404 and neighbors are occupied periods.
	AllowEmpty bool
	// AllowFuture disables the captured-now publication cutoff. Unlike dated
	// detail, an archive excludes later instants today when this is false.
	AllowFuture bool
	// Clock defaults to time.Now and runs once when future rows are excluded.
	Clock func() time.Time
}

type genericDayArchive struct {
	model, navigation *genericModel
	field             models.Field
	allowEmpty        bool
	allowFuture       bool
}

// NewDayArchiveView creates a read-only archive with explicit template and row
// scope. Nil Ordering defaults to descending DateField, with primary-key ties.
// Scope must include all navigation visibility. For occupied neighbors only,
// AllowField also gates DateField metadata, even when it is not in Fields.
// Denied neighbors are omitted, never replaced by scanning farther rows.
func NewDayArchiveView(options DayArchiveViewOptions) (http.Handler, error) {
	return newDayArchiveView(options, false)
}

// Both public day selectors share one query, navigation and emission boundary.
// Today selects its date and publication cutoff from one request-local instant.
func newDayArchiveView(options DayArchiveViewOptions, useToday bool) (http.Handler, error) {
	date, clock := options.Date, options.Clock
	if !useToday && date == nil {
		return nil, ErrGenericConfiguration
	}
	if clock == nil {
		clock = time.Now
	}
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
	requestedOrder := options.Ordering
	if requestedOrder == nil {
		requestedOrder = []string{"-" + field.Name}
	}
	order, err := genericModelOrdering(model.schema, requestedOrder)
	if err != nil {
		return nil, err
	}
	config := options.Pagination
	if config.Mode == "" {
		config.Mode = pagination.PageNumber
	}
	if config.Mode != pagination.PageNumber && config.Mode != pagination.LimitOffset {
		return nil, ErrGenericConfiguration
	}
	paginator, err := pagination.New(config)
	if err != nil {
		return nil, ErrGenericConfiguration
	}
	renderer, err := newGenericTemplate(options.TemplateViewOptions)
	if err != nil {
		return nil, err
	}
	// Only this private reader includes the navigation date. Grants below use
	// the original model, so its policy records retain the declared projection.
	navigation := *model
	navigation.selected = append([]string(nil), model.selected...)
	selected := false
	for _, name := range navigation.selected {
		selected = selected || name == field.Name
	}
	if !selected {
		navigation.selected = append(navigation.selected, field.Name)
	}
	archive := genericDayArchive{model: model, navigation: &navigation, field: field, allowEmpty: options.AllowEmpty, allowFuture: options.AllowFuture}
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
		call.base = call.base.WithContext(ctx)
		query, err := model.query(ctx)
		if err != nil {
			return readViewResult{}, err
		}
		raw := ""
		var day time.Time
		if !useToday {
			raw, err = date(call.request())
			if !genericContextOK(ctx) {
				return readViewResult{}, ErrUnavailable
			}
			if err != nil {
				if err == ErrInvalidLookup {
					return readViewResult{}, ErrNotFound
				}
				return readViewResult{}, ErrUnavailable
			}
			var valid bool
			day, valid = genericCalendarDate(raw)
			if !valid {
				return readViewResult{}, ErrNotFound
			}
		}
		locale, _ := i18n.FromContext(ctx)
		if !genericContextOK(ctx) {
			return readViewResult{}, ErrUnavailable
		}
		today := ""
		if useToday || !archive.allowFuture {
			value, valid := genericModelTemporal(models.DateTime, clock(), genericLookupTextBytes)
			if !valid || !genericContextOK(ctx) {
				return readViewResult{}, ErrUnavailable
			}
			now := value.(time.Time)
			local := locale.LocalTime(now)
			if local.Year() < 1 || local.Year() > 9999 {
				return readViewResult{}, ErrUnavailable
			}
			today = local.Format("2006-01-02")
			if useToday {
				raw = today
				day, valid = genericCalendarDate(raw)
				if !valid {
					return readViewResult{}, ErrUnavailable
				}
			}
			if !archive.allowFuture {
				var cutoff any = now
				if field.Kind == models.Date {
					cutoff = today
				}
				query = query.Filter(orm.Q(field.Name+"__lte", cutoff))
			}
		}
		predicate, err := genericDatePredicate(ctx, locale, field, day, raw)
		if err != nil {
			return readViewResult{}, err
		}
		values, err := url.ParseQuery(call.rawQuery())
		if err != nil || !genericPaginationValues(values, config.Mode) {
			return readViewResult{response: genericStatusResponse(http.StatusBadRequest)}, nil
		}
		page, err := paginator.Parse(values)
		if err != nil {
			return readViewResult{response: genericStatusResponse(http.StatusBadRequest)}, nil
		}
		budget := templateContextBudget{remainingValues: templateContextMaxValues, remainingBytes: templateContextMaxBytes}
		jsonBudget := &genericModelJSONBudget{values: templateContextMaxValues, text: templateContextMaxBytes, raw: templateContextMaxBytes}
		rows, err := model.readRowsWithBudget(ctx, query.Filter(predicate).OrderBy(order...).Limit(page.Size+1).Offset(page.Offset), page.Size+1, &budget, jsonBudget)
		if err != nil {
			return readViewResult{}, err
		}
		if len(rows) == 0 && (!archive.allowEmpty || page.Offset > 0) {
			return readViewResult{}, ErrNotFound
		}
		hasMore := len(rows) > page.Size
		if hasMore {
			rows = rows[:page.Size]
		}
		objects, finalize, err := model.projectRows(ctx, rows, false)
		if err != nil {
			return readViewResult{}, err
		}
		next, previous := paginator.Links(values, page, hasMore)
		reserved := templates.Context{"object_list": objects, "date_list": nil, "day": raw, "page": map[string]any{
			"size": page.Size, "offset": page.Offset, "has_next": next != "", "has_previous": previous != "", "next": next, "previous": previous,
		}}
		contributors := []genericModelRow{}
		for _, direction := range []struct {
			name         string
			month, after bool
		}{{"previous_day", false, false}, {"next_day", false, true}, {"previous_month", true, false}, {"next_month", true, true}} {
			value, row, err := archive.neighbor(ctx, query, locale, day, today, direction.month, direction.after, &budget, jsonBudget)
			if err != nil {
				return readViewResult{}, err
			}
			reserved[direction.name] = value
			if row != nil {
				contributors = append(contributors, *row)
			}
		}
		response, err := renderer.renderWith(call, reserved)
		return readViewResult{response: response, finalize: func(call *readViewCall) error {
			if err := finalize(call); err != nil {
				return err
			}
			for _, row := range contributors {
				if err := model.objectGrant(call.base.Context(), row, false); err != nil {
					return err
				}
				allowed, err := model.fieldGrant(call.base.Context(), row, field.Name)
				if err != nil {
					return err
				}
				if !allowed {
					return auth.ErrPermissionDenied
				}
			}
			return nil
		}}, err
	})
}

func (a genericDayArchive) neighbor(ctx context.Context, query orm.Query[*models.MapRecord], locale i18n.Locale, day time.Time, today string, month, after bool, budget *templateContextBudget, jsonBudget *genericModelJSONBudget) (any, *genericModelRow, error) {
	start := day
	stepMonths, stepDays := 0, 1
	if month {
		start = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
		stepMonths, stepDays = 1, 0
	}
	candidate := start.AddDate(0, -stepMonths, -stepDays)
	if after {
		candidate = start.AddDate(0, stepMonths, stepDays)
	}
	if candidate.Year() < 1 || candidate.Year() > 9999 {
		return nil, nil, nil
	}
	if a.allowEmpty {
		text := candidate.Format("2006-01-02")
		if !a.allowFuture && text > today {
			return nil, nil, nil
		}
		if _, err := genericDatePredicate(ctx, locale, a.field, candidate, text); err != nil {
			if err == ErrNotFound {
				return nil, nil, nil
			}
			return nil, nil, err
		}
		return text, nil, nil
	}
	boundary := start
	operator, order := "__lt", "-"+a.field.Name
	if after {
		boundary, operator, order = candidate, "__gte", a.field.Name
	}
	var bound any = boundary.Format("2006-01-02")
	if a.field.Kind == models.DateTime {
		instant, err := locale.ResolveLocal(ctx, i18n.LocalDateTime{Year: boundary.Year(), Month: boundary.Month(), Day: boundary.Day()})
		if !genericContextOK(ctx) {
			return nil, nil, ErrUnavailable
		}
		if err == i18n.ErrInvalidTime || err == i18n.ErrAmbiguousTime || err == i18n.ErrNonexistentTime {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, ErrUnavailable
		}
		bound = instant
	}
	ordering, err := genericModelOrdering(a.model.schema, []string{order})
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	rows, err := a.navigation.readRowsWithBudget(ctx, query.Only(a.navigation.selected...).Filter(orm.Q(a.field.Name+operator, bound)).OrderBy(ordering...).Limit(1), 1, budget, jsonBudget)
	if err != nil || len(rows) == 0 {
		return nil, nil, err
	}
	row := rows[0]
	value, valid := row.values[a.field.Name].(time.Time)
	if !valid {
		return nil, nil, ErrUnavailable
	}
	if a.field.Kind == models.DateTime {
		value = locale.LocalTime(value)
	}
	if value.Year() < 1 || value.Year() > 9999 {
		return nil, nil, ErrUnavailable
	}
	text := value.Format("2006-01-02")
	boundaryText := boundary.Format("2006-01-02")
	if after && text < boundaryText || !after && text >= boundaryText || !a.allowFuture && text > today {
		return nil, nil, ErrUnavailable
	}
	// A discovered date must be usable by this view's strict day resolver.
	// Unsupported local midnight intervals are omitted, never scanned past.
	target, valid := genericCalendarDate(text)
	if !valid {
		return nil, nil, ErrUnavailable
	}
	if _, err := genericDatePredicate(ctx, locale, a.field, target, text); err != nil {
		if err == ErrNotFound {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if err := a.model.objectGrant(ctx, row, false); err != nil {
		if err == auth.ErrPermissionDenied || err == ErrNotFound {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	allowed, err := a.model.fieldGrant(ctx, row, a.field.Name)
	if err != nil || !allowed {
		return nil, nil, err
	}
	if month {
		text = time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		target, _ := genericCalendarDate(text)
		if _, err := genericDatePredicate(ctx, locale, a.field, target, text); err != nil {
			if err == ErrNotFound {
				return nil, nil, nil
			}
			return nil, nil, err
		}
	}
	return text, &row, nil
}
