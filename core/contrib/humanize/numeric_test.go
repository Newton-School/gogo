package humanize

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/Newton-School/gogo/core/i18n"
	"github.com/Newton-School/gogo/core/templates"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/message/catalog"
	"golang.org/x/text/number"
)

func formatter(t *testing.T) *Formatter {
	t.Helper()
	f, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestNumericHumanizeExactGroupingAndConversion(t *testing.T) {
	f := formatter(t)
	ctx := context.Background()
	for _, test := range []struct {
		value any
		want  string
	}{
		{nil, ""}, {4500, "4,500"}, {4500.2, "4,500.2"}, {"4500.2000", "4,500.2000"},
		{"9007199254740993123456789.000000000001", "9,007,199,254,740,993,123,456,789.000000000001"},
		{uint64(math.MaxUint64), "18,446,744,073,709,551,615"}, {int64(math.MinInt64), "-9,223,372,036,854,775,808"},
		{"-000.000", "-0.000"}, {"+.0120", "0.0120"}, {"1234e-5", "0.01234"}, {json.Number("1e20"), "100,000,000,000,000,000,000"},
		{"not a number", "not a number"}, {"<script>bad</script>", "<script>bad</script>"},
	} {
		got, err := f.IntComma(ctx, test.value, true)
		if err != nil || got != test.want {
			t.Errorf("%v: %q %v want %q", test.value, got, err, test.want)
		}
	}
	for _, value := range []any{math.NaN(), math.Inf(1), true, struct{}{}, json.Number("not-a-number"), strings.Repeat("1", maxDigits+1), "a\x00b", string([]byte{255}), json.Number("1e99999")} {
		if _, err := f.IntComma(ctx, value, true); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("invalid %T accepted: %v", value, err)
		}
	}
	large, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	before := large.String()
	if got, err := f.IntComma(ctx, large, true); err != nil || got != "123,456,789,012,345,678,901,234,567,890" || large.String() != before {
		t.Fatal(got, err)
	}
}

func TestNumericHumanizeOrdinalAPAndIntWord(t *testing.T) {
	f := formatter(t)
	ctx := context.Background()
	for _, test := range []struct {
		value       any
		ordinal, ap string
	}{{0, "0th", "0"}, {1, "1st", "one"}, {2, "2nd", "two"}, {3, "3rd", "three"}, {9, "9th", "nine"}, {11, "11th", "11"}, {12, "12th", "12"}, {13, "13th", "13"}, {21, "21st", "21"}, {-1, "-1", "-1"}, {"001", "1st", "one"}, {"1.0", "1.0", "1.0"}, {1.9, "1st", "one"}, {json.Number("2.9"), "2nd", "two"}, {"900719925474099312345678911", "900719925474099312345678911th", "900719925474099312345678911"}} {
		if got, err := f.Ordinal(ctx, test.value); err != nil || got != test.ordinal {
			t.Fatal(test.value, got, err)
		}
		if got, err := f.APNumber(ctx, test.value); err != nil || got != test.ap {
			t.Fatal(test.value, got, err)
		}
	}
	for _, test := range []struct {
		value any
		want  string
	}{{999999, "999999"}, {1000000, "1.0 million"}, {1200000, "1.2 million"}, {-1200000000, "-1.2 billion"}, {1250000, "1.3 million"}, {-1250000, "-1.3 million"}, {999999999, "1,000.0 million"}, {1000000000, "1.0 billion"}, {"1e6", "1e6"}, {json.Number("1e100"), "1.0 googol"}, {"99999999999999999999999999999999999999", "0.0 googol"}} {
		if got, err := f.IntWord(ctx, test.value); err != nil || got != test.want {
			t.Fatal(test.value, got, err, test.want)
		}
	}
	number := json.Number("2.5")
	if got, err := f.APNumber(ctx, &number); err != nil || got != "two" {
		t.Fatal("pointer JSON number", got, err)
	}
}

func TestNumericHumanizeReviewedBinaryIntegerAndLiteralBoundaries(t *testing.T) {
	f := formatter(t)
	ctx := context.Background()
	for _, n := range []float64{1e23, -1e23, 1e24, math.MaxFloat64, math.SmallestNonzeroFloat64, -0.9} {
		exact, _ := new(big.Float).SetFloat64(n).Int(nil)
		if got, err := f.APNumber(ctx, n); err != nil || got != exact.String() {
			t.Fatal("binary float integer changed", n, got, err, exact)
		}
	}
	if got, err := f.Ordinal(ctx, float64(1e23)); err != nil || got != "99999999999999991611392nd" {
		t.Fatal(got, err)
	}
	if got, err := f.IntWord(ctx, float64(1e24)); err != nil || got != "1,000.0 sextillion" {
		t.Fatal("float conversion moved unit boundary", got, err)
	}
	for _, tc := range []struct{ value, want string }{{"1e6", "1e6"}, {"1000x", "1,000x"}, {"-0001234.5000x", "-0,001,234.5000x"}, {"+1234", "+1234"}, {" 1234", " 1234"}, {"١٢٣٤x", "١,٢٣٤x"}, {"1234<script>", "1,234<script>"}, {"000000", "000,000"}} {
		if got, err := f.IntComma(ctx, tc.value, false); err != nil || got != tc.want {
			t.Fatal(tc.value, got, err, tc.want)
		}
	}
	if got, err := f.IntComma(ctx, "1000x", true); err != nil || got != "1,000x" {
		t.Fatal("conversion failure skipped literal fallback", got, err)
	}
	for _, value := range []string{"1e2048", "1e99999", "-1e-2048"} {
		if got, err := f.IntComma(ctx, value, false); err != nil || got != value {
			t.Fatal("literal mode expanded an exponent", value, got, err)
		}
	}
	r, _ := i18n.New(i18n.Config{Languages: []string{"en"}})
	tr, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Catalogs: []i18n.Catalog{{Language: "en", Domain: Domain, Messages: []i18n.Translation{{ID: "{value} googol", Context: "intword", Forms: map[i18n.PluralForm]string{i18n.One: "{value} one", i18n.Other: "{value} other"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	f, err = New(Config{Resolver: r, Translator: tr})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ value, want string }{{"1e99", "0.1 other"}, {"-1e99", "-0.1 one"}, {"1e100", "1.0 one"}, {"-1e100", "-1.0 one"}, {"-12e99", "-1.2 other"}} {
		if got, err := f.IntWord(ctx, json.Number(tc.value)); err != nil || got != tc.want {
			t.Fatal("signed plural proxy", tc.value, got, err, tc.want)
		}
	}
}

func TestNumericHumanizeLocalePatternsMatchPublicExactFormatter(t *testing.T) {
	for _, name := range []string{"en", "en-IN", "de", "fr", "hi", "bn", "ar", "fa", "sv", "ru", "es", "pl", "ja", "zh", "th", "hi-u-nu-deva"} {
		t.Run(name, func(t *testing.T) {
			tag := language.MustParse(name)
			p, err := localePattern(tag)
			if err != nil {
				t.Fatal(err)
			}
			printer := message.NewPrinter(tag, message.Catalog(catalog.NewBuilder()))
			for _, n := range []int64{123456789012345, -123456789012345, 9223372036854775807} {
				d, _, _ := numericValue(n)
				got := p.format(d, true)
				want := printer.Sprintf("%v", number.Decimal(n, number.Precision(-1)))
				if got != want {
					t.Fatalf("%d: %q want %q", n, got, want)
				}
			}
		})
	}
}

func TestNumericHumanizeLocaleTranslationEscapingAndConcurrency(t *testing.T) {
	r, err := i18n.New(i18n.Config{Languages: []string{"en", "de", "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	translator, err := i18n.NewTranslator(r, i18n.TranslatorConfig{Catalogs: []i18n.Catalog{{Language: "de", Domain: Domain, Messages: []i18n.Translation{
		{ID: "one", Text: "eins"}, {ID: "{value}st", Context: "ordinal 1", Text: "<sup>{value}.</sup>"},
		{ID: "{value} million", Context: "intword", Forms: map[i18n.PluralForm]string{i18n.One: "{value} Million", i18n.Other: "{value} Millionen"}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := New(Config{Resolver: r, Translator: translator})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := r.WithLocale(context.Background(), i18n.Preferences{Language: "de"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.IntWord(ctx, 1000000); err != nil || got != "1,0 Million" {
		t.Fatal(got, err)
	}
	if got, err := f.IntWord(ctx, 1200000); err != nil || got != "1,2 Millionen" {
		t.Fatal(got, err)
	}
	engine := templates.New(templates.Config{Loaders: []templates.Loader{templates.MapLoader{"x": `{% load humanize %}{{ 1|ordinal }}|{{ 1|apnumber }}|{{ value|intcomma }}|{{ 4500|intcomma:False }}`}}, Filters: f.Filters(), Libraries: []string{"humanize"}, LocaleResolver: r})
	if got, err := engine.Render(ctx, "x", templates.Context{"value": templates.SafeHTML("<b>text</b>")}); err != nil || got != "&lt;sup&gt;1.&lt;/sup&gt;|eins|&lt;b&gt;text&lt;/b&gt;|4,500" {
		t.Fatal(got, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := []string{"en", "de", "hi"}[i%3]
			ctx, err := r.WithLocale(context.Background(), i18n.Preferences{Language: name})
			if err != nil {
				t.Error(err)
				return
			}
			want := map[string]string{"en": "1,234,567.8900", "de": "1.234.567,8900", "hi": "12,34,567.8900"}[name]
			if got, err := f.IntComma(ctx, "1234567.8900", true); err != nil || got != want {
				t.Error(name, got, err)
			}
		}(i)
	}
	wg.Wait()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := f.Ordinal(canceled, 1); got != "" || !errors.Is(err, context.Canceled) {
		t.Fatal(got, err)
	}
	if got, err := insertValue(strings.Repeat("{value}", 10000), strings.Repeat("1", 2048)); got != "" || err == nil {
		t.Fatal("translation expansion unbounded")
	}
}

func FuzzNumericHumanizeBoundedExactInput(f *testing.F) {
	for _, s := range []string{"1.00000", "-1234567.89", "1e100", "1e99999", ".5", "--2", "<script>", "NaN"} {
		f.Add(s)
	}
	h, err := New(Config{})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			return
		}
		for _, fn := range []func(context.Context, any) (string, error){h.Ordinal, h.APNumber, h.IntWord, func(ctx context.Context, v any) (string, error) { return h.IntComma(ctx, v, true) }} {
			out, err := fn(context.Background(), s)
			if err != nil && out != "" {
				t.Fatal("partial error result")
			}
			if len(out) > 64<<10 {
				t.Fatal("unbounded output")
			}
		}
	})
}
