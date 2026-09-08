// Package humanize provides plain-text, request-local presentation helpers.
package humanize

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Newton-School/gogo/core/i18n"
	"golang.org/x/text/language"
)

var ErrInvalidValue = errors.New("humanize: invalid or excessive display value")
var ErrInvalidLocale = errors.New("humanize: unsupported locale number pattern")

const Domain = "humanize"

type Config struct {
	Resolver   *i18n.Resolver
	Translator *i18n.Translator
	Clock      func() time.Time
}

type Formatter struct {
	resolver   *i18n.Resolver
	translator *i18n.Translator
	clock      func() time.Time
	patterns   sync.Map
}

func New(config Config) (*Formatter, error) {
	if config.Resolver == nil {
		var err error
		config.Resolver, err = i18n.New(i18n.Config{Languages: []string{"en"}})
		if err != nil {
			return nil, err
		}
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	f := &Formatter{resolver: config.Resolver, translator: config.Translator, clock: config.Clock}
	if _, err := f.pattern(config.Resolver.Default().Tag()); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *Formatter) locale(ctx context.Context) (context.Context, i18n.Locale, error) {
	if f == nil || ctx == nil {
		return nil, i18n.Locale{}, ErrInvalidLocale
	}
	ctx, err := f.resolver.WithLocale(ctx, i18n.Preferences{})
	if err != nil {
		return nil, i18n.Locale{}, err
	}
	l, _ := i18n.FromContext(ctx)
	return ctx, l, nil
}
func (f *Formatter) pattern(tag language.Tag) (numberPattern, error) {
	if existing, ok := f.patterns.Load(tag.String()); ok {
		return existing.(numberPattern), nil
	}
	p, err := localePattern(tag)
	if err != nil {
		return p, err
	}
	actual, _ := f.patterns.LoadOrStore(tag.String(), p)
	return actual.(numberPattern), nil
}

func (f *Formatter) text(ctx context.Context, kind, source string) (string, error) {
	if f.translator == nil {
		return displayResult(ctx, source)
	}
	return f.translator.Pgettext(ctx, Domain, kind, source)
}
func (f *Formatter) plural(ctx context.Context, kind, one, many string, n int64) (string, error) {
	if f.translator == nil {
		if n == 1 {
			return displayResult(ctx, one)
		}
		return displayResult(ctx, many)
	}
	return f.translator.NPgettext(ctx, Domain, kind, one, many, n)
}

func displayResult(ctx context.Context, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return value, nil
}

// Literal fallback groups only the initial signed digit run, preserving all
// other characters and digits exactly. It never parses exponents or HTML.
func commaLiteral(value string) string {
	runes := []rune(value)
	start := 0
	if len(runes) > 0 && runes[0] == '-' {
		start = 1
	}
	end := start
	for end < len(runes) && unicode.Is(unicode.Nd, runes[end]) {
		end++
	}
	var out strings.Builder
	for i, r := range runes {
		if i > start && i < end && (end-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(r)
	}
	return out.String()
}

func insertValue(pattern, value string) (string, error) {
	const limit = 64 << 10
	count := strings.Count(pattern, "{value}")
	if len(pattern) > limit || len(value) > 7 && count > (limit-len(pattern))/(len(value)-7) {
		return "", ErrInvalidValue
	}
	return strings.ReplaceAll(pattern, "{value}", value), nil
}

func (f *Formatter) IntComma(ctx context.Context, value any, localized bool) (string, error) {
	ctx, locale, err := f.locale(ctx)
	if err != nil {
		return "", err
	}
	d, ok, err := numericValueMode(value, localized)
	if err != nil {
		return "", err
	}
	if !ok || !localized {
		return displayResult(ctx, commaLiteral(d.original))
	}
	tag := locale.Tag()
	p, err := f.pattern(tag)
	if err != nil {
		return "", err
	}
	return displayResult(ctx, p.format(d, true))
}

func (f *Formatter) APNumber(ctx context.Context, value any) (string, error) {
	ctx, _, err := f.locale(ctx)
	if err != nil {
		return "", err
	}
	d, ok, err := numericValue(value)
	if err != nil {
		return "", err
	}
	if !ok {
		return displayResult(ctx, d.original)
	}
	n, integer := d.integer()
	if !integer || len(n) != 1 || n[0] < '1' || n[0] > '9' {
		return displayResult(ctx, n)
	}
	return f.text(ctx, "", []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}[n[0]-'1'])
}

func (f *Formatter) Ordinal(ctx context.Context, value any) (string, error) {
	ctx, _, err := f.locale(ctx)
	if err != nil {
		return "", err
	}
	d, ok, err := numericValue(value)
	if err != nil {
		return "", err
	}
	if !ok {
		return displayResult(ctx, d.original)
	}
	n, integer := d.integer()
	if !integer || strings.HasPrefix(n, "-") {
		return displayResult(ctx, n)
	}
	tail := n
	if len(tail) > 2 {
		tail = tail[len(tail)-2:]
	}
	mod, _ := strconv.Atoi(tail)
	suffix, kind := "th", "ordinal "+string(n[len(n)-1])
	if mod >= 11 && mod <= 13 {
		kind = "ordinal 11, 12, 13"
	} else {
		switch mod % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	pattern, err := f.text(ctx, kind, "{value}"+suffix)
	if err != nil {
		return "", err
	}
	out, err := insertValue(pattern, n)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return out, err
}

var largeUnits = []struct {
	power int
	name  string
}{{6, "million"}, {9, "billion"}, {12, "trillion"}, {15, "quadrillion"}, {18, "quintillion"}, {21, "sextillion"}, {24, "septillion"}, {27, "octillion"}, {30, "nonillion"}, {33, "decillion"}, {100, "googol"}}

func (f *Formatter) IntWord(ctx context.Context, value any) (string, error) {
	ctx, locale, err := f.locale(ctx)
	if err != nil {
		return "", err
	}
	d, ok, err := numericValue(value)
	if err != nil {
		return "", err
	}
	if !ok {
		return displayResult(ctx, d.original)
	}
	n, integer := d.integer()
	wholeDigits := strings.TrimPrefix(n, "-")
	if !integer || len(wholeDigits) < 7 {
		return displayResult(ctx, n)
	}
	for _, unit := range largeUnits {
		if len(wholeDigits) > unit.power+3 {
			continue
		}
		// Round the exact integer quotient to one decimal, halfway away from
		// zero. Float conversion would change large values and unit boundaries.
		magnitude, _ := new(big.Int).SetString(wholeDigits, 10)
		divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(unit.power-1)), nil)
		q, remainder := new(big.Int), new(big.Int)
		q.QuoRem(magnitude, divisor, remainder)
		if new(big.Int).Lsh(remainder, 1).Cmp(divisor) >= 0 {
			q.Add(q, big.NewInt(1))
		}
		digits := q.String()
		if len(digits) == 1 {
			digits = "0" + digits
		}
		scaled := decimalValue{negative: d.negative, whole: digits[:len(digits)-1], fraction: digits[len(digits)-1:]}
		p, err := f.pattern(locale.Tag())
		if err != nil {
			return "", err
		}
		// Preserve Django's integer plural proxy: exactly one is singular;
		// a positive fraction below one rounds down, above one rounds up.
		// Negative values use signed floor before taking the absolute value.
		whole := new(big.Int).Quo(magnitude, new(big.Int).Mul(divisor, big.NewInt(10)))
		proxy := whole.Int64()
		if whole.IsInt64() {
			if (d.negative || proxy >= 1) && new(big.Int).Mod(magnitude, new(big.Int).Mul(divisor, big.NewInt(10))).Sign() != 0 {
				proxy++
			}
		} else {
			proxy = 2
		}
		pattern, err := f.plural(ctx, "intword", "{value} "+unit.name, "{value} "+unit.name, proxy)
		if err != nil {
			return "", err
		}
		out, err := insertValue(pattern, p.format(scaled, true))
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return out, err
	}
	return displayResult(ctx, n)
}
