package i18n_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/i18n"
)

func zoneLocale(t testing.TB, zone string) i18n.Locale {
	t.Helper()
	r, err := i18n.New(i18n.Config{Languages: []string{"en"}, DefaultTimeZone: zone})
	if err != nil {
		t.Fatal(err)
	}
	return r.Default()
}

func TestResolveLocalRejectsFoldsGapsAndNormalizedCalendarValues(t *testing.T) {
	for _, test := range []struct {
		zone string
		wall i18n.LocalDateTime
		want error
	}{
		{"America/New_York", i18n.LocalDateTime{Year: 2026, Month: 3, Day: 8, Hour: 2, Minute: 30}, i18n.ErrNonexistentTime},
		{"America/New_York", i18n.LocalDateTime{Year: 2026, Month: 11, Day: 1, Hour: 1, Minute: 30}, i18n.ErrAmbiguousTime},
		{"Australia/Lord_Howe", i18n.LocalDateTime{Year: 2026, Month: 4, Day: 5, Hour: 1, Minute: 45}, i18n.ErrAmbiguousTime},
		{"Australia/Lord_Howe", i18n.LocalDateTime{Year: 2026, Month: 10, Day: 4, Hour: 2, Minute: 15}, i18n.ErrNonexistentTime},
		{"Pacific/Apia", i18n.LocalDateTime{Year: 2011, Month: 12, Day: 30, Hour: 12}, i18n.ErrNonexistentTime},
		{"Pacific/Kwajalein", i18n.LocalDateTime{Year: 1969, Month: 9, Day: 30, Hour: 13}, i18n.ErrAmbiguousTime},
		{"UTC", i18n.LocalDateTime{Year: 2026, Month: 2, Day: 29}, i18n.ErrInvalidTime},
		{"UTC", i18n.LocalDateTime{Year: 2026, Month: 1, Day: 1, Hour: 24}, i18n.ErrInvalidTime},
		{"UTC", i18n.LocalDateTime{Year: 2026, Month: 1, Day: 1, Second: 60}, i18n.ErrInvalidTime},
		{"UTC", i18n.LocalDateTime{Year: 10000, Month: 1, Day: 1}, i18n.ErrInvalidTime},
		{"UTC", i18n.LocalDateTime{Year: 2026, Month: 1, Day: 1, Nanosecond: 1000000000}, i18n.ErrInvalidTime},
		{"Asia/Kolkata", i18n.LocalDateTime{Year: 1, Month: 1, Day: 1}, i18n.ErrInvalidTime},
	} {
		got, err := zoneLocale(t, test.zone).ResolveLocal(context.Background(), test.wall)
		if !errors.Is(err, test.want) || !got.IsZero() {
			t.Fatal(test, got, err)
		}
	}
}

func TestResolveLocalPreservesExactInstantAndTransitionBoundaries(t *testing.T) {
	for _, test := range []struct{ zone, wall, utc string }{
		{"UTC", "2026-09-05T14:30:05.123456789", "2026-09-05T14:30:05.123456789Z"},
		{"Asia/Kolkata", "2026-09-05T14:30:05.123456789", "2026-09-05T09:00:05.123456789Z"},
		{"America/New_York", "2026-03-08T01:59:59.999999999", "2026-03-08T06:59:59.999999999Z"},
		{"America/New_York", "2026-03-08T03:00:00", "2026-03-08T07:00:00Z"},
		{"America/New_York", "2026-11-01T02:00:00", "2026-11-01T07:00:00Z"},
		{"Australia/Lord_Howe", "2026-10-04T02:30:00", "2026-10-03T15:30:00Z"},
		{"Pacific/Apia", "2011-12-31T00:00:00", "2011-12-30T10:00:00Z"},
	} {
		parsed, err := time.Parse("2006-01-02T15:04:05.999999999", test.wall)
		if err != nil {
			t.Fatal(err)
		}
		wall := i18n.LocalDateTime{Year: parsed.Year(), Month: parsed.Month(), Day: parsed.Day(), Hour: parsed.Hour(), Minute: parsed.Minute(), Second: parsed.Second(), Nanosecond: parsed.Nanosecond()}
		got, err := zoneLocale(t, test.zone).ResolveLocal(context.Background(), wall)
		if err != nil || got.Location() != time.UTC || got.Format(time.RFC3339Nano) != test.utc {
			t.Fatal(test, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (i18n.Locale{}).ResolveLocal(ctx, i18n.LocalDateTime{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := (i18n.Locale{}).ResolveLocal(nil, i18n.LocalDateTime{}); !errors.Is(err, i18n.ErrInvalidTime) {
		t.Fatal(err)
	}
}

func FuzzResolveLocalNeverNormalizesWallInput(f *testing.F) {
	locales := []i18n.Locale{zoneLocale(f, "UTC"), zoneLocale(f, "America/New_York"), zoneLocale(f, "Australia/Lord_Howe"), zoneLocale(f, "Pacific/Apia")}
	f.Add(2026, 11, 1, 1, 30, 0, 0, uint8(1))
	f.Add(2026, 3, 8, 2, 30, 0, 0, uint8(1))
	f.Add(2026, 9, 5, 12, 4, 5, 123456789, uint8(0))
	f.Fuzz(func(t *testing.T, year, month, day, hour, minute, second, nano int, zone uint8) {
		locale := locales[int(zone)%len(locales)]
		wall := i18n.LocalDateTime{Year: year, Month: time.Month(month), Day: day, Hour: hour, Minute: minute, Second: second, Nanosecond: nano}
		instant, err := locale.ResolveLocal(context.Background(), wall)
		if err != nil {
			if !instant.IsZero() {
				t.Fatal("error exposed a selected instant")
			}
			return
		}
		local := locale.LocalTime(instant)
		if local.Year() != year || int(local.Month()) != month || local.Day() != day || local.Hour() != hour || local.Minute() != minute || local.Second() != second || local.Nanosecond() != nano || instant.Location() != time.UTC {
			t.Fatal("wall input was normalized", wall, instant, local)
		}
	})
}
