package http

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/models"
)

const genericLookupTextBytes = 4096

// genericModelFieldSupported limits this initial generic read boundary to
// intrinsic field representations. In particular it never invokes a custom
// codec, relationship lookup, validator, default or model cleaning hook.
func genericModelFieldSupported(field models.Field, lookup bool) bool {
	if field.Codec != nil || field.Relation != nil {
		return false
	}
	switch field.Kind {
	case models.SmallInteger, models.Integer, models.BigInteger,
		models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger,
		models.SmallAuto, models.Auto, models.BigAuto, models.UUID, models.Float,
		models.Boolean, models.Char, models.Text, models.Slug, models.Email, models.URL,
		models.GenericIPAddress, models.FilePath, models.Date, models.Time,
		models.DateTime, models.Duration:
		return true
	case models.Decimal:
		return field.MaxDigits > 0 && field.DecimalPlaces >= 0 && field.DecimalPlaces <= field.MaxDigits
	case models.JSON:
		return !lookup
	default:
		return false
	}
}

func decodeGenericLookupValue(field models.Field, raw any) (any, error) {
	if raw == nil || !genericModelFieldSupported(field, true) {
		return nil, ErrInvalidLookup
	}
	value, ok := genericModelScalar(field, raw, genericLookupTextBytes)
	if !ok {
		return nil, ErrInvalidLookup
	}
	return value, nil
}

// normalizeGenericModelValue returns detached canonical values for policy
// records. SQL NULL remains nil; a top-level JSON null remains models.JSONNull.
// Decimal text is exact, not rounded or converted through a binary float.
func normalizeGenericModelValue(field models.Field, raw any) (any, error) {
	return genericModelValue(field, raw, false)
}

// normalizeGenericModelValueWithJSONBudget shares JSON parsing and decoded-data
// work across all selected cells of one result page. The caller owns the budget
// and abandons the page on any error; failures do not refund consumed work.
// Non-JSON fields retain the ordinary scalar normalization boundary.
func normalizeGenericModelValueWithJSONBudget(field models.Field, raw any, budget *genericModelJSONBudget) (any, error) {
	if budget == nil || budget.html {
		return nil, ErrUnavailable
	}
	return genericModelValueWithJSONBudget(field, raw, budget)
}

// projectGenericModelValue creates data, never trusted HTML. JSON numbers become
// their exact lexical strings and JSON null becomes nil for HTML projection.
// Date and Time stay calendar/clock strings; only DateTime is a timezone-aware
// instant. Duration uses Go's duration spelling, not calendar arithmetic.
func projectGenericModelValue(field models.Field, raw any) (any, error) {
	value, err := genericModelValue(field, raw, true)
	if err != nil || value == nil || field.Kind == models.JSON {
		return value, err
	}
	switch field.Kind {
	case models.Date:
		return value.(time.Time).Format("2006-01-02"), nil
	case models.Time:
		return value.(time.Time).Format("15:04:05.999999999"), nil
	case models.Duration:
		return value.(time.Duration).String(), nil
	}
	return value, nil
}

func genericModelValue(field models.Field, raw any, html bool) (any, error) {
	budget := genericModelJSONBudget{
		values: templateContextMaxValues, text: templateContextMaxBytes,
		raw: templateContextMaxBytes, html: html,
	}
	return genericModelValueWithJSONBudget(field, raw, &budget)
}

func genericModelValueWithJSONBudget(field models.Field, raw any, budget *genericModelJSONBudget) (any, error) {
	if !genericModelFieldSupported(field, false) {
		return nil, ErrUnavailable
	}
	if raw == nil {
		if field.Null {
			return nil, nil
		}
		return nil, ErrUnavailable
	}
	if field.Kind == models.JSON {
		if value, ok := raw.(json.RawMessage); ok && value == nil {
			if field.Null {
				return nil, nil
			}
			return nil, ErrUnavailable
		}
		value, ok := budget.copy(reflect.ValueOf(raw), 0)
		if !ok {
			return nil, ErrUnavailable
		}
		if value == nil && !budget.html {
			return models.JSONNull, nil
		}
		return value, nil
	}
	value, ok := genericModelScalar(field, raw, templateContextMaxBytes)
	if !ok {
		return nil, ErrUnavailable
	}
	return value, nil
}

func genericModelScalar(field models.Field, raw any, limit int) (any, bool) {
	switch field.Kind {
	case models.SmallInteger, models.Integer, models.BigInteger,
		models.PositiveSmallInteger, models.PositiveInteger, models.PositiveBigInteger,
		models.SmallAuto, models.Auto, models.BigAuto:
		value, ok := genericModelInteger(raw, limit)
		if !ok {
			return nil, false
		}
		low, high := int64(math.MinInt64), int64(math.MaxInt64)
		switch field.Kind {
		case models.SmallInteger, models.SmallAuto:
			low, high = math.MinInt16, math.MaxInt16
		case models.Integer, models.Auto:
			low, high = math.MinInt32, math.MaxInt32
		case models.PositiveSmallInteger:
			low, high = 0, math.MaxInt16
		case models.PositiveInteger:
			low, high = 0, math.MaxInt32
		case models.PositiveBigInteger:
			low = 0
		}
		return value, value >= low && value <= high
	case models.Char, models.Text, models.Slug, models.Email, models.URL, models.FilePath:
		return genericModelText(raw, limit)
	case models.UUID:
		value, ok := genericModelText(raw, limit)
		if !ok || len(value) != 36 {
			return nil, false
		}
		for i := range len(value) {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if value[i] != '-' {
					return nil, false
				}
			} else if !genericRedirectHex(value[i]) {
				return nil, false
			}
		}
		return strings.ToLower(value), true
	case models.GenericIPAddress:
		if address, ok := raw.(netip.Addr); ok {
			if !address.IsValid() || address.Zone() != "" {
				return nil, false
			}
			return address.String(), true
		}
		value, ok := genericModelText(raw, limit)
		if !ok {
			return nil, false
		}
		address, err := netip.ParseAddr(value)
		if err != nil || address.Zone() != "" {
			return nil, false
		}
		return address.String(), true
	case models.Decimal:
		value, ok := genericModelNumberText(raw, limit, false)
		if !ok || !genericModelDecimal(field, value) {
			return nil, false
		}
		return value, true
	case models.Float:
		primitive := reflect.ValueOf(raw)
		if primitive.IsValid() && (primitive.Kind() == reflect.Float32 || primitive.Kind() == reflect.Float64) {
			// Preserve the actual binary value of float32 when promoting it;
			// formatting it with 32-bit precision and parsing as float64 changes it.
			number := primitive.Float()
			return number, !math.IsNaN(number) && !math.IsInf(number, 0)
		}
		value, ok := genericModelNumberText(raw, limit, true)
		if !ok {
			return nil, false
		}
		number, err := strconv.ParseFloat(value, 64)
		return number, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
	case models.Boolean:
		value := reflect.ValueOf(raw)
		if value.IsValid() && value.Kind() == reflect.Bool {
			return value.Bool(), true
		}
		text, ok := genericModelText(raw, limit)
		if !ok {
			return nil, false
		}
		boolean, err := strconv.ParseBool(text)
		return boolean, err == nil
	case models.Date, models.Time, models.DateTime:
		return genericModelTemporal(field.Kind, raw, limit)
	case models.Duration:
		value := reflect.ValueOf(raw)
		if value.IsValid() && value.Kind() >= reflect.Int && value.Kind() <= reflect.Int64 {
			return time.Duration(value.Int()), true
		}
		text, ok := genericModelText(raw, limit)
		if !ok {
			return nil, false
		}
		duration, err := time.ParseDuration(text)
		return duration, err == nil
	}
	return nil, false
}

func genericModelText(raw any, limit int) (string, bool) {
	if value, ok := raw.([]byte); ok {
		if len(value) > limit || !utf8.Valid(value) || bytes.IndexByte(value, 0) >= 0 {
			return "", false
		}
		return string(value), true
	}
	value := reflect.ValueOf(raw)
	if !value.IsValid() || value.Kind() != reflect.String || value.Len() > limit {
		return "", false
	}
	text := value.String() // Reflection reads a scalar; no String method runs.
	return text, utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

func genericModelInteger(raw any, limit int) (int64, bool) {
	value := reflect.ValueOf(raw)
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(value.Uint()), value.Uint() <= math.MaxInt64
	}
	text, ok := genericModelText(raw, limit)
	if !ok {
		return 0, false
	}
	number, err := strconv.ParseInt(text, 10, 64)
	return number, err == nil
}

func genericModelNumberText(raw any, limit int, floats bool) (string, bool) {
	value := reflect.ValueOf(raw)
	if !value.IsValid() {
		return "", false
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(value.Uint(), 10), true
	case reflect.Float32, reflect.Float64:
		if !floats || math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) {
			return "", false
		}
		return strconv.FormatFloat(value.Float(), 'g', -1, value.Type().Bits()), true
	}
	return genericModelText(raw, limit)
}

func genericModelDecimal(field models.Field, value string) bool {
	if value == "" {
		return false
	}
	if value[0] == '-' || value[0] == '+' {
		value = value[1:]
	}
	whole, fraction, dot := strings.Cut(value, ".")
	if whole == "" && (!dot || fraction == "") {
		return false
	}
	for _, part := range []string{whole, fraction} {
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	wholeDigits := len(strings.TrimLeft(whole, "0"))
	return len(fraction) <= field.DecimalPlaces && wholeDigits <= field.MaxDigits-field.DecimalPlaces && wholeDigits+len(fraction) <= field.MaxDigits
}

func genericModelTemporal(kind models.Kind, raw any, limit int) (any, bool) {
	value, known := raw.(time.Time)
	if !known {
		text, ok := genericModelText(raw, limit)
		if !ok {
			return nil, false
		}
		layout := time.RFC3339Nano
		switch kind {
		case models.Date:
			if len(text) != 10 {
				return nil, false
			}
			layout = "2006-01-02"
		case models.Time:
			if !genericModelClock(text) {
				return nil, false
			}
			layout = "15:04:05.999999999"
		case models.DateTime:
			if !genericModelRFC3339(text) {
				return nil, false
			}
		}
		var err error
		value, err = time.Parse(layout, text)
		if err != nil {
			return nil, false
		}
	}
	if kind == models.Time {
		hour, minute, second := value.Clock()
		value = time.Date(0, time.January, 1, hour, minute, second, value.Nanosecond(), time.UTC)
	} else {
		if value.Year() < 1 || value.Year() > 9999 {
			return nil, false
		}
		if kind == models.Date {
			value = time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
		} else {
			_, offset := value.Zone()
			if offset <= -86400 || offset >= 86400 {
				return nil, false
			}
			value = value.UTC()
			if value.Year() < 1 || value.Year() > 9999 {
				return nil, false
			}
		}
	}
	// UTC itself is publicly mutable through *Time.Location(). Copy even for
	// zero time. Normalization intentionally removes monotonic-only metadata.
	zone := value.Location()
	_ = zone.String()
	copy := *zone
	return value.In(&copy), true
}

func genericModelClock(text string) bool {
	if len(text) < 8 || text[2] != ':' || text[5] != ':' {
		return false
	}
	for _, index := range []int{0, 1, 3, 4, 6, 7} {
		if text[index] < '0' || text[index] > '9' {
			return false
		}
	}
	if text[:2] > "23" || text[3:5] > "59" || text[6:8] > "59" {
		return false
	}
	if len(text) == 8 {
		return true
	}
	if text[8] != '.' || len(text) < 10 || len(text) > 18 {
		return false
	}
	for _, c := range text[9:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func genericModelRFC3339(text string) bool {
	if len(text) < 20 || text[10] != 'T' {
		return false
	}
	clock := text[11:]
	if strings.HasSuffix(clock, "Z") {
		clock = clock[:len(clock)-1]
	} else {
		if len(clock) < 14 {
			return false
		}
		zone := clock[len(clock)-6:]
		if zone[0] != '+' && zone[0] != '-' || zone[3] != ':' || zone[1:3] > "23" || zone[4:] > "59" {
			return false
		}
		for _, index := range []int{1, 2, 4, 5} {
			if zone[index] < '0' || zone[index] > '9' {
				return false
			}
		}
		clock = clock[:len(clock)-6]
	}
	return genericModelClock(clock)
}

type genericModelJSONBudget struct {
	values, text, raw int
	html              bool
}

func (b *genericModelJSONBudget) visit(depth int) bool {
	if depth > templateContextMaxDepth || b.values == 0 {
		return false
	}
	b.values--
	return true
}

func (b *genericModelJSONBudget) string(value string) bool {
	if len(value) > b.text || !utf8.ValidString(value) {
		return false
	}
	b.text -= len(value)
	return true
}

func (b *genericModelJSONBudget) number(value string) (any, bool) {
	if !b.string(value) || !genericModelJSONNumber(value) {
		return nil, false
	}
	if b.html {
		return value, true
	}
	return json.Number(value), true
}

func (b *genericModelJSONBudget) copy(value reflect.Value, depth int) (any, bool) {
	for value.IsValid() && value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil, b.visit(depth)
		}
		value = value.Elem()
	}
	if value.IsValid() && value.CanInterface() {
		switch raw := value.Interface().(type) {
		case json.RawMessage:
			return b.decode(raw, depth)
		case []byte:
			if depth != 0 {
				// Only the top-level provider representation is raw JSON bytes.
				// Nested bytes have no implicit base64 or JSON-text interpretation.
				return nil, false
			}
			return b.decode(raw, depth)
		case json.Number:
			if !b.visit(depth) {
				return nil, false
			}
			return b.number(string(raw))
		}
		if value.Type() == reflect.TypeOf(models.JSONNull) {
			return nil, value.Interface() == models.JSONNull && b.visit(depth)
		}
	}
	if !b.visit(depth) {
		return nil, false
	}
	if !value.IsValid() {
		return nil, true
	}
	switch value.Kind() {
	case reflect.Bool:
		return value.Bool(), true
	case reflect.String:
		text := value.String()
		return text, b.string(text)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return b.number(strconv.FormatInt(value.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return b.number(strconv.FormatUint(value.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		if math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) {
			return nil, false
		}
		return b.number(strconv.FormatFloat(value.Float(), 'g', -1, value.Type().Bits()))
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String || value.Len() > b.values/2 {
			return nil, false
		}
		if value.IsNil() {
			return nil, true
		}
		result := make(map[string]any, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key().String()
			if !b.visit(depth+1) || !b.string(key) {
				return nil, false
			}
			item, ok := b.copy(iterator.Value(), depth+1)
			if !ok {
				return nil, false
			}
			result[key] = item
		}
		return result, true
	case reflect.Array, reflect.Slice:
		if value.Len() > b.values {
			return nil, false
		}
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil, true
		}
		result := make([]any, value.Len())
		for i := range result {
			item, ok := b.copy(value.Index(i), depth+1)
			if !ok {
				return nil, false
			}
			result[i] = item
		}
		return result, true
	default:
		return nil, false
	}
}

func (b *genericModelJSONBudget) decode(raw []byte, depth int) (any, bool) {
	if len(raw) == 0 || len(raw) > b.raw || !utf8.Valid(raw) || !genericModelJSONEscapes(raw) {
		return nil, false
	}
	b.raw -= len(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, ok := b.read(decoder, depth)
	if !ok {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return value, true
}

func (b *genericModelJSONBudget) read(decoder *json.Decoder, depth int) (any, bool) {
	if !b.visit(depth) {
		return nil, false
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	switch value := token.(type) {
	case nil, bool:
		return value, true
	case string:
		return value, b.string(value)
	case json.Number:
		return b.number(string(value))
	case json.Delim:
		if value == '[' {
			result := []any{}
			for decoder.More() {
				item, ok := b.read(decoder, depth+1)
				if !ok {
					return nil, false
				}
				result = append(result, item)
			}
			end, err := decoder.Token()
			return result, err == nil && end == json.Delim(']')
		}
		if value == '{' {
			result := map[string]any{}
			for decoder.More() {
				if !b.visit(depth + 1) {
					return nil, false
				}
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || !b.string(key) {
					return nil, false
				}
				if _, duplicate := result[key]; duplicate {
					return nil, false
				}
				item, ok := b.read(decoder, depth+1)
				if !ok {
					return nil, false
				}
				result[key] = item
			}
			end, err := decoder.Token()
			return result, err == nil && end == json.Delim('}')
		}
	}
	return nil, false
}

// Validate a JSON number lexeme directly. This preserves arbitrarily large
// bounded exponents and integer precision without float conversion or methods.
func genericModelJSONNumber(value string) bool {
	if value == "" {
		return false
	}
	i := 0
	if value[i] == '-' {
		i++
	}
	if i == len(value) {
		return false
	}
	if value[i] == '0' {
		i++
	} else {
		if value[i] < '1' || value[i] > '9' {
			return false
		}
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
	}
	if i < len(value) && value[i] == '.' {
		i++
		start := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	if i < len(value) && (value[i] == 'e' || value[i] == 'E') {
		i++
		if i < len(value) && (value[i] == '+' || value[i] == '-') {
			i++
		}
		start := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	return i == len(value)
}

// encoding/json replaces unpaired escaped surrogates with RuneError. Refuse
// those spellings instead of changing stored data or object-key identity.
func genericModelJSONEscapes(raw []byte) bool {
	quoted := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			quoted = !quoted
		} else if quoted && raw[i] == '\\' {
			i++
			if i >= len(raw) {
				return false
			}
			if raw[i] != 'u' {
				continue
			}
			code, ok := genericModelJSONHex(raw[i+1:])
			if !ok {
				return false
			}
			i += 4
			if code >= 0xdc00 && code <= 0xdfff {
				return false
			}
			if code >= 0xd800 && code <= 0xdbff {
				if len(raw)-i <= 6 || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return false
				}
				low, ok := genericModelJSONHex(raw[i+3:])
				if !ok || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
		}
	}
	return true
}

func genericModelJSONHex(raw []byte) (uint16, bool) {
	if len(raw) < 4 {
		return 0, false
	}
	var value uint16
	for _, c := range raw[:4] {
		if !genericRedirectHex(c) {
			return 0, false
		}
		value <<= 4
		if c >= '0' && c <= '9' {
			value += uint16(c - '0')
		} else {
			value += uint16(c|32) - 'a' + 10
		}
	}
	return value, true
}
