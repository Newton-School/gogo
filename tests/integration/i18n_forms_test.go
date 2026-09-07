package integration_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type localizedEvent struct {
	models.Base
	ID       int64
	StartsAt time.Time
}

func (*localizedEvent) Schema() models.Schema {
	return models.Schema{AppLabel: "calendar", Name: "Event", Fields: []models.Field{
		models.BigAutoField("id", models.WithStructField("ID")),
		models.DateTimeField("starts_at", models.WithStructField("StartsAt")),
	}}
}

func TestPostgresLocalizedModelFormRoundtripPreservesUTCInstant(t *testing.T) {
	backend := testservice.Postgres(t)
	ctx := context.Background()
	schema := (&localizedEvent{}).Schema()
	if err := backend.SchemaEditor().CreateModel(ctx, backend, schema); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	if err := registry.Register(schema); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	for _, test := range []struct{ zone, input, utc string }{
		{"Asia/Kolkata", "2026-09-05T14:30:05.123456", "2026-09-05T09:00:05.123456Z"},
		{"America/New_York", "2026-11-01T01:30:00-04:00", "2026-11-01T05:30:00Z"},
		{"America/New_York", "2026-11-01T01:30:00-05:00", "2026-11-01T06:30:00Z"},
	} {
		t.Run(test.zone+test.utc, func(t *testing.T) {
			locales, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: test.zone})
			if err != nil {
				t.Fatal(err)
			}
			requestCtx, err := locales.WithLocale(ctx, i18n.Preferences{})
			if err != nil {
				t.Fatal(err)
			}
			row := &localizedEvent{}
			record, err := models.Bind(row)
			if err != nil {
				t.Fatal(err)
			}
			form, err := forms.NewModelForm(requestCtx, record, forms.ModelFormOptions{Fields: []string{"starts_at"}}, forms.WithData(url.Values{"starts_at": {test.input}}))
			if err != nil || !form.IsValid() {
				t.Fatal(err, form.Errors())
			}
			if _, err := form.Save(false); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(requestCtx, row, orm.SaveOptions{ForceInsert: true}); err != nil {
				t.Fatal(err)
			}
			loaded, err := orm.For(store, func() *localizedEvent { return &localizedEvent{} }).Filter(orm.Q("id", row.ID)).Get(ctx)
			if err != nil || loaded.StartsAt.UTC().Format(time.RFC3339Nano) != test.utc {
				t.Fatal("stored instant differs from form intent", err, loaded)
			}
			loadedRecord, err := models.Bind(loaded)
			if err != nil {
				t.Fatal(err)
			}
			editor, err := forms.NewModelForm(requestCtx, loadedRecord, forms.ModelFormOptions{Fields: []string{"starts_at"}})
			if err != nil {
				t.Fatal(err)
			}
			html, err := editor.Render("div")
			want := locales.Default().LocalTime(loaded.StartsAt).Format("2006-01-02T15:04:05.999999999")
			if err != nil || !strings.Contains(string(html), `value="`+want+`"`) {
				t.Fatal("reloaded form lost current timezone", html, err)
			}
		})
	}
	locales, _ := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: "America/New_York"})
	requestCtx, _ := locales.WithLocale(ctx, i18n.Preferences{})
	for _, input := range []string{"2026-03-08T02:30", "2026-11-01T01:30"} {
		row := &localizedEvent{}
		record, _ := models.Bind(row)
		form, err := forms.NewModelForm(requestCtx, record, forms.ModelFormOptions{Fields: []string{"starts_at"}}, forms.WithData(url.Values{"starts_at": {input}}))
		if err != nil || form.IsValid() || !form.HasError("starts_at", "ambiguous_timezone") {
			t.Fatal("invalid wall time accepted", input, err, form.Errors())
		}
		if _, err := form.Save(false); err == nil || row.ID != 0 || !row.StartsAt.IsZero() {
			t.Fatal("invalid form produced a persistable change", row, err)
		}
	}
	if count, err := orm.For(store, func() *localizedEvent { return &localizedEvent{} }).Count(ctx); err != nil || count != 3 {
		t.Fatal("invalid forms changed storage", count, err)
	}
}
