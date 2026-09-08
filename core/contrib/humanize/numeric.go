package humanize

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxDigits = 2048

type decimalValue struct {
	negative                  bool
	whole, fraction, original string
	text                      bool
	floatInteger              string
}

// Number conversion never invokes application String/MarshalJSON methods.
// Decimal strings and JSON numbers retain every digit; binary floats retain
// their supplied Go value, not precision already lost before this call.
func numericValue(value any) (decimalValue, bool, error) {
	return numericValueMode(value, true)
}

func numericValueMode(value any, parse bool) (decimalValue, bool, error) {
	var raw string
	var floatInteger string
	text := false
	switch value := value.(type) {
	case nil:
		return decimalValue{}, false, nil
	case json.Number:
		raw = string(value)
	case big.Int:
		if value.BitLen() > maxDigits*4 {
			return decimalValue{}, false, ErrInvalidValue
		}
		raw = value.String()
	case *big.Int:
		if value == nil {
			return decimalValue{}, false, nil
		}
		if value.BitLen() > maxDigits*4 {
			return decimalValue{}, false, ErrInvalidValue
		}
		raw = value.String()
	default:
		v := reflect.ValueOf(value)
		for depth := 0; v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface; depth++ {
			if v.IsNil() {
				return decimalValue{}, false, nil
			}
			if depth == 8 {
				return decimalValue{}, false, ErrInvalidValue
			}
			v = v.Elem()
		}
		switch v.Kind() {
		case reflect.String:
			raw, text = v.String(), v.Type() != reflect.TypeFor[json.Number]()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			raw = strconv.FormatInt(v.Int(), 10)
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			raw = strconv.FormatUint(v.Uint(), 10)
		case reflect.Float32, reflect.Float64:
			n := v.Float()
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return decimalValue{}, false, ErrInvalidValue
			}
			raw = strconv.FormatFloat(n, 'g', -1, v.Type().Bits())
			// Integer helpers truncate the actual binary value, not its shortest
			// round-trip display string (which can cross an integer boundary).
			integer, _ := new(big.Float).SetFloat64(n).Int(nil)
			floatInteger = integer.String()
		default:
			return decimalValue{}, false, ErrInvalidValue
		}
	}
	if len(raw) > maxDigits || !utf8.ValidString(raw) || strings.ContainsRune(raw, 0) {
		return decimalValue{}, false, ErrInvalidValue
	}
	d := decimalValue{original: raw, text: text, floatInteger: floatInteger}
	if !parse {
		return d, false, nil
	}
	valueText := strings.TrimSpace(raw)
	invalid := func() (decimalValue, bool, error) {
		if text {
			return d, false, nil
		}
		return decimalValue{}, false, ErrInvalidValue
	}
	if valueText == "" {
		return invalid()
	}
	if valueText[0] == '-' || valueText[0] == '+' {
		d.negative = valueText[0] == '-'
		valueText = valueText[1:]
	}
	if valueText == "" {
		return invalid()
	}
	exponent := 0
	if i := strings.IndexAny(valueText, "eE"); i >= 0 {
		exp := valueText[i+1:]
		if len(exp) == 0 || len(exp) > 5 {
			return invalid()
		}
		var err error
		exponent, err = strconv.Atoi(exp)
		if err != nil || exponent < -maxDigits || exponent > maxDigits {
			return invalid()
		}
		valueText = valueText[:i]
	}
	if strings.Count(valueText, ".") > 1 {
		return invalid()
	}
	parts := strings.SplitN(valueText, ".", 2)
	whole, fraction := parts[0], ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if whole == "" && fraction == "" {
		return invalid()
	}
	for _, part := range []string{whole, fraction} {
		for _, c := range part {
			if c < '0' || c > '9' {
				return invalid()
			}
		}
	}
	digits := whole + fraction
	point := len(whole) + exponent
	if point <= 0 {
		if len(digits)-point > maxDigits {
			return decimalValue{}, false, ErrInvalidValue
		}
		whole = "0"
		fraction = strings.Repeat("0", -point) + digits
	} else if point >= len(digits) {
		if point > maxDigits {
			return decimalValue{}, false, ErrInvalidValue
		}
		whole = digits + strings.Repeat("0", point-len(digits))
		fraction = ""
	} else {
		whole, fraction = digits[:point], digits[point:]
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	d.whole, d.fraction = whole, fraction
	return d, true, nil
}

func (d decimalValue) integer() (string, bool) {
	if d.floatInteger != "" {
		return d.floatInteger, true
	}
	if d.text && strings.ContainsAny(strings.TrimSpace(d.original), ".eE") {
		return d.original, false
	}
	value := d.whole
	if d.negative && value != "0" {
		value = "-" + value
	}
	return value, true
}
