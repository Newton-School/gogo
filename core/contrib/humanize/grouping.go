package humanize

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/message/catalog"
	"golang.org/x/text/number"
)

type numberPattern struct {
	digits                                         [10]string
	group, decimal, negativePrefix, negativeSuffix string
	primary, secondary                             int
}

// Discover only CLDR presentation symbols/group widths using exact small
// integer probes through x/text's public formatter. Arbitrary-precision input
// is never fed to its float conversion. An isolated empty message catalog avoids
// process-global message substitutions. Unsupported pattern shapes fail closed.
func localePattern(tag language.Tag) (numberPattern, error) {
	p := message.NewPrinter(tag, message.Catalog(catalog.NewBuilder()))
	pattern := numberPattern{}
	decode := map[rune]byte{}
	for i := range 10 {
		text := p.Sprintf("%v", number.Decimal(i, number.NoSeparator(), number.Scale(0)))
		if utf8.RuneCountInString(text) != 1 {
			return pattern, ErrInvalidLocale
		}
		r, _ := utf8.DecodeRuneInString(text)
		if _, exists := decode[r]; exists {
			return pattern, ErrInvalidLocale
		}
		pattern.digits[i] = text
		decode[r] = byte('0' + i)
	}
	grouped := p.Sprintf("%v", number.Decimal(int64(123456789012345), number.Precision(-1)))
	var normalized strings.Builder
	var groups []int
	var separators []string
	width := 0
	separator := ""
	for _, r := range grouped {
		if digit, ok := decode[r]; ok {
			if separator != "" {
				separators = append(separators, separator)
				separator = ""
			}
			normalized.WriteByte(digit)
			width++
		} else {
			if separator == "" {
				if width == 0 {
					return pattern, ErrInvalidLocale
				}
				groups = append(groups, width)
				width = 0
			}
			separator += string(r)
		}
	}
	if normalized.String() != "123456789012345" || separator != "" || width == 0 {
		return pattern, ErrInvalidLocale
	}
	groups = append(groups, width)
	if len(separators) > 0 {
		pattern.group = separators[0]
		if len(pattern.group) > 16 || len(groups) != len(separators)+1 {
			return pattern, ErrInvalidLocale
		}
		for _, s := range separators {
			if s != pattern.group {
				return pattern, ErrInvalidLocale
			}
		}
		pattern.primary = groups[len(groups)-1]
		pattern.secondary = pattern.primary
		if len(groups) > 2 {
			pattern.secondary = groups[len(groups)-2]
		}
		for i := 1; i < len(groups)-1; i++ {
			if groups[i] != pattern.secondary {
				return pattern, ErrInvalidLocale
			}
		}
		if groups[0] > pattern.secondary {
			return pattern, ErrInvalidLocale
		}
	}
	decimal := p.Sprintf("%v", number.Decimal(1.5, number.NoSeparator(), number.Scale(1)))
	if !strings.HasPrefix(decimal, pattern.digits[1]) || !strings.HasSuffix(decimal, pattern.digits[5]) {
		return pattern, ErrInvalidLocale
	}
	pattern.decimal = strings.TrimSuffix(strings.TrimPrefix(decimal, pattern.digits[1]), pattern.digits[5])
	if pattern.decimal == "" || len(pattern.decimal) > 16 {
		return pattern, ErrInvalidLocale
	}
	negative := p.Sprintf("%v", number.Decimal(-1, number.NoSeparator(), number.Scale(0)))
	if strings.Count(negative, pattern.digits[1]) != 1 || len(negative) > 64 {
		return pattern, ErrInvalidLocale
	}
	sign := strings.SplitN(negative, pattern.digits[1], 2)
	pattern.negativePrefix, pattern.negativeSuffix = sign[0], sign[1]
	return pattern, nil
}

func (p numberPattern) format(d decimalValue, group bool) string {
	var out strings.Builder
	if d.negative {
		out.WriteString(p.negativePrefix)
	}
	for i, c := range d.whole {
		remaining := len(d.whole) - i
		if group && i > 0 && p.primary > 0 && (remaining == p.primary || remaining > p.primary && (remaining-p.primary)%p.secondary == 0) {
			out.WriteString(p.group)
		}
		out.WriteString(p.digits[c-'0'])
	}
	if d.fraction != "" {
		out.WriteString(p.decimal)
		for _, c := range d.fraction {
			out.WriteString(p.digits[c-'0'])
		}
	}
	if d.negative {
		out.WriteString(p.negativeSuffix)
	}
	return out.String()
}
