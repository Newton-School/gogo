package serialization

import (
	"encoding/base64"
	"encoding/json"
	"github.com/Newton-School/gogo/core/models"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var uuidText = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var decimalText = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)
var numberText = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

const maxJSONNumberExponent int64 = (1 << 31) + 128

func copyTime(t time.Time) time.Time { z := time.FixedZone("UTC", 0); return t.UTC().In(z) }
func textValue(v any) (string, bool) {
	if b, ok := v.([]byte); ok {
		return string(b), true
	}
	r := reflect.ValueOf(v)
	if r.IsValid() && r.Kind() == reflect.String {
		return r.String(), true
	}
	return "", false
}

// canonical returns detached database/policy data and its lossless JSON cell
// value. Only builtins and the exact framework JSON-null marker are inspected;
// no Stringer, marshaler, codec or field validator is invoked.
func canonical(f models.Field, v any, primary bool) (data, wire any, err error) {
	if v == nil {
		if f.Null && !primary {
			return nil, nil, nil
		}
		return nil, nil, ErrInvalid
	}
	if f.Kind == models.JSON {
		b := valueBudget{nodes: 65536, text: MaxRecordBytes}
		n, e := b.json(v, 0)
		if e != nil {
			return nil, nil, e
		}
		if n == nil {
			return models.JSONNull, nil, nil
		}
		return n, n, nil
	}
	if f.Kind == models.Binary {
		b, ok := v.([]byte)
		if !ok {
			return nil, nil, ErrInvalid
		}
		if len(b) > MaxRecordBytes {
			return nil, nil, ErrLimit
		}
		n := append([]byte(nil), b...)
		if n == nil {
			n = []byte{}
		}
		return n, base64.StdEncoding.EncodeToString(n), nil
	}
	if f.Kind == models.Boolean {
		r := reflect.ValueOf(v)
		if r.Kind() != reflect.Bool {
			return nil, nil, ErrInvalid
		}
		return r.Bool(), r.Bool(), nil
	}
	if f.Kind == models.Date || f.Kind == models.Time || f.Kind == models.DateTime {
		var t time.Time
		if original, ok := v.(time.Time); ok {
			t = original
		} else {
			s, ok := textValue(v)
			if !ok || len(s) > 64 {
				return nil, nil, ErrInvalid
			}
			layout := time.RFC3339Nano
			if f.Kind == models.Date {
				layout = "2006-01-02"
			}
			if f.Kind == models.Time {
				layout = "15:04:05.999999999"
			}
			var e error
			t, e = time.Parse(layout, s)
			if e != nil {
				return nil, nil, ErrInvalid
			}
		}
		if (f.Kind != models.Time && t.Year() < 1) || t.Year() > 9999 || t.Nanosecond()%1000 != 0 {
			return nil, nil, ErrInvalid
		}
		if f.Kind == models.Date {
			s := t.Format("2006-01-02")
			n, _ := time.Parse("2006-01-02", s)
			return copyTime(n), s, nil
		}
		if f.Kind == models.Time {
			s := t.Format("15:04:05.999999999")
			n, _ := time.Parse("15:04:05.999999999", s)
			return copyTime(n), s, nil
		}
		t = copyTime(t)
		if t.Year() < 1 || t.Year() > 9999 {
			return nil, nil, ErrInvalid
		}
		return t, t.Format(time.RFC3339Nano), nil
	}
	if f.Kind == models.Duration {
		d, ok := v.(time.Duration)
		if !ok {
			return nil, nil, ErrInvalid
		}
		if d%time.Microsecond != 0 {
			return nil, nil, ErrInvalid
		}
		return d, int64(d / time.Microsecond), nil
	}
	if f.Kind == models.Decimal {
		s, ok := textValue(v)
		if !ok || len(s) > 1024 || !decimalText.MatchString(s) {
			return nil, nil, ErrInvalid
		}
		parts := strings.Split(strings.TrimPrefix(s, "-"), ".")
		whole := strings.TrimLeft(parts[0], "0")
		frac := ""
		if len(parts) == 2 {
			frac = parts[1]
		}
		if len(frac) > f.DecimalPlaces || len(whole) > f.MaxDigits-f.DecimalPlaces {
			return nil, nil, ErrInvalid
		}
		frac += strings.Repeat("0", f.DecimalPlaces-len(frac))
		s = parts[0]
		if f.DecimalPlaces > 0 {
			s += "." + frac
		}
		if strings.HasPrefix(strings.TrimSpace(mustText(v)), "-") && strings.Trim(s, "0.") != "" {
			s = "-" + s
		}
		return s, s, nil
	}
	if f.Kind == models.Float {
		r := reflect.ValueOf(v)
		var n float64
		switch r.Kind() {
		case reflect.Float32, reflect.Float64:
			n = r.Float()
		default:
			s, ok := textValue(v)
			if !ok {
				return nil, nil, ErrInvalid
			}
			var e error
			n, e = strconv.ParseFloat(s, 64)
			if e != nil {
				return nil, nil, ErrInvalid
			}
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, nil, ErrInvalid
		}
		return n, n, nil
	}
	if integerKind(f.Kind) {
		n, e := integer(v)
		if e != nil {
			return nil, nil, e
		}
		min, max := int64(math.MinInt64), int64(math.MaxInt64)
		switch f.Kind {
		case models.SmallInteger, models.SmallAuto, models.PositiveSmallInteger:
			min, max = math.MinInt16, math.MaxInt16
		case models.Integer, models.Auto, models.PositiveInteger:
			min, max = math.MinInt32, math.MaxInt32
		}
		if strings.HasPrefix(string(f.Kind), "positive_") {
			min = 0
		}
		if f.IsAuto() {
			min = 1
		}
		if n < min || n > max {
			return nil, nil, ErrInvalid
		}
		return n, n, nil
	}
	s, ok := textValue(v)
	if !ok || len(s) > MaxRecordBytes || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return nil, nil, ErrInvalid
	}
	if f.MaxLength > 0 && utf8.RuneCountInString(s) > f.MaxLength || utf8.RuneCountInString(s) < f.MinLength || primary && (s == "" || len(s) > 512) || f.Kind == models.UUID && !uuidText.MatchString(s) {
		return nil, nil, ErrInvalid
	}
	return s, s, nil
}
func mustText(v any) string { s, _ := textValue(v); return s }
func integerKind(k models.Kind) bool {
	switch k {
	case models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger, models.SmallAuto, models.Auto, models.BigAuto:
		return true
	}
	return false
}
func integer(v any) (int64, error) {
	r := reflect.ValueOf(v)
	if !r.IsValid() {
		return 0, ErrInvalid
	}
	switch r.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return r.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if r.Uint() <= math.MaxInt64 {
			return int64(r.Uint()), nil
		}
	default:
		if s, ok := textValue(v); ok && len(s) <= 24 {
			n, e := strconv.ParseInt(s, 10, 64)
			if e == nil && strconv.FormatInt(n, 10) == s {
				return n, nil
			}
		}
	}
	return 0, ErrInvalid
}

type valueBudget struct{ nodes, text int }

func (b *valueBudget) json(v any, depth int) (any, error) {
	if depth > 32 || b.nodes <= 0 {
		return nil, ErrLimit
	}
	b.nodes--
	if v == nil {
		return nil, nil
	}
	if reflect.TypeOf(v) == reflect.TypeOf(models.JSONNull) && v == models.JSONNull {
		return nil, nil
	}
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		if !utf8.ValidString(x) || len(x) > b.text {
			return nil, ErrLimit
		}
		b.text -= len(x)
		return x, nil
	case json.Number:
		n, e := canonicalNumber(string(x))
		if e != nil {
			return nil, e
		}
		if len(n) > b.text {
			return nil, ErrLimit
		}
		b.text -= len(n)
		return n, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, ErrInvalid
		}
		n, e := canonicalNumber(strconv.FormatFloat(x, 'g', -1, 64))
		if e != nil {
			return nil, e
		}
		if len(n) > b.text {
			return nil, ErrLimit
		}
		b.text -= len(n)
		return n, nil
	case []any:
		if len(x) > b.nodes {
			return nil, ErrLimit
		}
		n := make([]any, len(x))
		for i, v := range x {
			c, e := b.json(v, depth+1)
			if e != nil {
				return nil, e
			}
			n[i] = c
		}
		return n, nil
	case map[string]any:
		if len(x) > b.nodes/2 {
			return nil, ErrLimit
		}
		n := make(map[string]any, len(x))
		for k, v := range x {
			if _, e := b.json(k, depth+1); e != nil {
				return nil, e
			}
			c, e := b.json(v, depth+1)
			if e != nil {
				return nil, e
			}
			n[k] = c
		}
		return n, nil
	}
	r := reflect.ValueOf(v)
	if r.IsValid() {
		switch r.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return b.json(json.Number(strconv.FormatInt(r.Int(), 10)), depth)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return b.json(json.Number(strconv.FormatUint(r.Uint(), 10)), depth)
		}
	}
	return nil, ErrInvalid
}

// Canonical scientific/ordinary spelling uses decimal digits, never float64.
// It makes JSONB's harmless scale/exponent normalization compare exactly while
// retaining integers beyond 2^53. Exponents do not expand unbounded strings.
func canonicalNumber(raw string) (json.Number, error) {
	if len(raw) > 256 || !numberText.MatchString(raw) {
		return "", ErrInvalid
	}
	negative := strings.HasPrefix(raw, "-")
	s := strings.TrimPrefix(raw, "-")
	exponent := int64(0)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		n, e := strconv.ParseInt(s[i+1:], 10, 64)
		if e != nil || n > maxJSONNumberExponent+256 || n < -maxJSONNumberExponent-256 {
			return "", ErrInvalid
		}
		exponent = n
		s = s[:i]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		exponent -= int64(len(s) - i - 1)
		s = s[:i] + s[i+1:]
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0", nil
	}
	trimmed := strings.TrimRight(s, "0")
	exponent += int64(len(s) - len(trimmed))
	s = trimmed
	if len(s) > 128 {
		return "", ErrInvalid
	}
	point := int64(len(s)) + exponent
	if point-1 > maxJSONNumberExponent || point-1 < -maxJSONNumberExponent {
		return "", ErrInvalid
	}
	var out string
	if point > 0 && point <= 21 && exponent >= 0 {
		out = s + strings.Repeat("0", int(exponent))
	} else if point > 0 && point < int64(len(s)) {
		out = s[:int(point)] + "." + s[int(point):]
	} else if point <= 0 && point >= -5 {
		out = "0." + strings.Repeat("0", int(-point)) + s
	} else {
		out = s[:1]
		if len(s) > 1 {
			out += "." + s[1:]
		}
		out += "e" + strconv.FormatInt(point-1, 10)
	}
	if negative {
		out = "-" + out
	}
	return json.Number(out), nil
}

func fromWire(f models.Field, cell any, primary bool) (any, error) {
	m, ok := cell.(map[string]any)
	if !ok || len(m) != 1 {
		return nil, ErrInvalid
	}
	if n, ok := m["sql_null"]; ok {
		if n != true {
			return nil, ErrInvalid
		}
		d, _, e := canonical(f, nil, primary)
		return d, e
	}
	v, ok := m["value"]
	if !ok {
		return nil, ErrInvalid
	}
	if f.Kind == models.JSON && v == nil {
		return models.JSONNull, nil
	}
	if v == nil {
		return nil, ErrInvalid
	}
	// JSON wire types are explicit, not form-like coercion. Provider decoding
	// separately accepts the database's conventional textual scalar values.
	switch {
	case f.Kind == models.JSON:
	case integerKind(f.Kind) || f.Kind == models.Float || f.Kind == models.Duration:
		if _, ok := v.(json.Number); !ok {
			return nil, ErrInvalid
		}
	case f.Kind == models.Boolean:
		if _, ok := v.(bool); !ok {
			return nil, ErrInvalid
		}
	default:
		if _, ok := v.(string); !ok {
			return nil, ErrInvalid
		}
	}
	if f.Kind == models.Binary {
		s, ok := v.(string)
		if !ok {
			return nil, ErrInvalid
		}
		raw, e := base64.StdEncoding.Strict().DecodeString(s)
		if e != nil || base64.StdEncoding.EncodeToString(raw) != s {
			return nil, ErrInvalid
		}
		v = raw
	}
	if f.Kind == models.Duration {
		n, e := integer(v)
		if e != nil || n > math.MaxInt64/int64(time.Microsecond) || n < math.MinInt64/int64(time.Microsecond) {
			return nil, ErrInvalid
		}
		v = time.Duration(n) * time.Microsecond
	}
	d, _, e := canonical(f, v, primary)
	return d, e
}
