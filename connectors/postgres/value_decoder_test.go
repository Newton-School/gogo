package postgres

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/models"
)

func TestFixedIntervalDecodingPreservesMicrosecondsAndRejectsAmbiguity(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want time.Duration
	}{
		{"00:00:00", 0}, {"00:00:01.000001", time.Second + time.Microsecond}, {"-00:00:01.000001", -time.Second - time.Microsecond},
		{"1 day 02:03:04.5", 26*time.Hour + 3*time.Minute + 4500*time.Millisecond}, {"-2 days +00:00:00.000001", -48*time.Hour + time.Microsecond},
		{"1 day -02:00:00", 22 * time.Hour}, {"2 days", 48 * time.Hour}, {"0 mons 00:00:01", time.Second},
		{"2562047:47:16.854775", time.Duration(math.MaxInt64 / 1000 * 1000)}, {"-2562047:47:16.854775", -time.Duration(math.MaxInt64 / 1000 * 1000)},
	} {
		got, err := fixedInterval(test.raw)
		if err != nil || got != test.want {
			t.Fatal(test.raw, got, test.want, err)
		}
	}
	for _, raw := range []string{"", " ", "private-invalid-value", "1 mon", "1 year", "1 day -1 mon", "2562047:47:16.854776", "-2562047:47:16.854776", "999999999999999999 days", "00:60:00", "00:00:60", "00:00:00.0000001", "00:00:00.", "00:00:", "--01:00:00", "-+01:00:00", "+-01:00:00", "++01:00:00", "infinity", "@ 1 sec ago", strings.Repeat("9", 257)} {
		if _, err := fixedInterval(raw); err == nil || strings.Contains(err.Error(), "private-invalid-value") {
			t.Fatal("invalid interval accepted or echoed", err)
		}
	}
	dialect := Dialect{}
	field := models.DurationField("elapsed")
	for _, value := range []any{"00:00:01", []byte("00:00:01"), time.Second} {
		if got, err := dialect.DecodeFieldValue(field, value); err != nil || got != time.Second {
			t.Fatal(got, err)
		}
	}
	if got, err := dialect.DecodeFieldValue(models.TextField("value"), "00:00:01"); err != nil || got != "00:00:01" {
		t.Fatal("unrelated driver representation changed", got, err)
	}
	if got, err := dialect.DecodeFieldValue(field, nil); err != nil || got != nil {
		t.Fatal("SQLNULL changed", got, err)
	}
}
