package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxReceiptBytes = 1 << 20

// Clone through bounded JSON, keeping exact number tokens and rejecting
// duplicate keys, unsupported values and excessive nesting. No custom values
// survive a callback boundary into the stored receipt.
func receiptObject(value any) (result Values, err error) {
	// Custom JSON marshalers are application code. A panic here, including
	// before Execute starts a transaction, is invalid JSON, not evidence of an
	// uncertain commit. Never propagate a marshaler's private panic value.
	defer func() {
		if recover() != nil {
			result, err = nil, errors.New("api: invalid operation JSON")
		}
	}()
	budget := 65536
	if !validReceiptText(value, 32, &budget) {
		return nil, errors.New("api: invalid operation JSON")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxReceiptBytes || !utf8.Valid(encoded) {
		return nil, errors.New("api: invalid operation JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	decoded, err := decodeJSON(decoder, 32)
	if err != nil {
		return nil, errors.New("api: invalid operation JSON")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("api: invalid operation JSON")
	}
	object, ok := decoded.(Values)
	if !ok {
		return nil, errors.New("api: operation JSON must be an object")
	}
	return Values(object), nil
}

// encoding/json replaces malformed UTF-8 strings. Reject those in ordinary
// JSON containers first, so two different inputs cannot silently hash alike.
func validReceiptText(value any, depth int, budget *int) bool {
	*budget--
	if depth < 0 || *budget < 0 {
		return false
	}
	switch value := value.(type) {
	case string:
		return utf8.ValidString(value)
	case json.RawMessage:
		return utf8.Valid(value)
	case Values:
		return validReceiptText(map[string]any(value), depth, budget)
	case map[string]any:
		for key, item := range value {
			if !utf8.ValidString(key) || !validReceiptText(item, depth-1, budget) {
				return false
			}
		}
	case []any:
		for _, item := range value {
			if !validReceiptText(item, depth-1, budget) {
				return false
			}
		}
	default:
		// Typed JSON containers and string aliases must obey the same bounds.
		v := reflect.ValueOf(value)
		if !v.IsValid() {
			return true
		}
		switch v.Kind() {
		case reflect.String:
			return utf8.ValidString(v.String())
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				key := iter.Key()
				if key.Kind() == reflect.String && !utf8.ValidString(key.String()) || !validReceiptText(iter.Value().Interface(), depth-1, budget) {
					return false
				}
			}
		case reflect.Array, reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				if !validReceiptText(v.Index(i).Interface(), depth-1, budget) {
					return false
				}
			}
		}
	}
	return true
}

// Object identity numbers use bounded ordinary decimal notation so existing
// integer-key policies can use json.Number.Int64 on both first use and replay.
func canonicalKeyNumber(number json.Number) (json.Number, error) {
	normalized, err := canonicalNumber(number)
	if err != nil {
		return "", err
	}
	digits, exponentText, found := strings.Cut(string(normalized), "e")
	if !found {
		return normalized, nil
	}
	negative := strings.HasPrefix(digits, "-")
	digits = strings.TrimPrefix(digits, "-")
	exponent, err := strconv.ParseInt(exponentText, 10, 64)
	if err != nil || exponent > 1024 || exponent < -1024 || int64(len(digits))+exponent > 1024 {
		return "", errors.New("api: operation identity number too large")
	}
	point := int64(len(digits)) + exponent
	if exponent >= 0 {
		digits += strings.Repeat("0", int(exponent))
	} else if point > 0 {
		digits = digits[:point] + "." + digits[point:]
	} else {
		digits = "0." + strings.Repeat("0", int(-point)) + digits
	}
	if negative {
		digits = "-" + digits
	}
	if len(digits) > 1024 {
		return "", errors.New("api: operation identity number too large")
	}
	return json.Number(digits), nil
}

// Normalize decimal JSON numbers without float conversion or expanding an
// exponent into an attacker-sized integer. Equivalent forms 1/1.0/10e-1 bind
// identically, while integers above 2^53 remain distinct.
func canonicalNumber(number json.Number) (json.Number, error) {
	raw := string(number)
	if len(raw) > 1024 {
		return "", errors.New("api: operation number too large")
	}
	negative := strings.HasPrefix(raw, "-")
	raw = strings.TrimPrefix(raw, "-")
	coefficient, exponentText, hasExponent := strings.Cut(strings.ToLower(raw), "e")
	var exponent int64
	if hasExponent {
		var err error
		exponent, err = strconv.ParseInt(exponentText, 10, 32)
		if err != nil {
			return "", errors.New("api: operation exponent too large")
		}
	}
	integer, fraction, _ := strings.Cut(coefficient, ".")
	digits := strings.TrimLeft(integer+fraction, "0")
	if digits == "" {
		return "0", nil
	}
	exponent -= int64(len(fraction))
	trimmed := strings.TrimRight(digits, "0")
	exponent += int64(len(digits) - len(trimmed))
	if negative {
		trimmed = "-" + trimmed
	}
	if exponent != 0 {
		trimmed += "e" + strconv.FormatInt(exponent, 10)
	}
	return json.Number(trimmed), nil
}

func canonicalOperation(value any) (any, error) {
	switch v := value.(type) {
	case json.Number:
		return canonicalNumber(v)
	case map[string]any:
		result := map[string]any{}
		for key, item := range v {
			normalized, err := canonicalOperation(item)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case Values:
		return canonicalOperation(map[string]any(v))
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			normalized, err := canonicalOperation(item)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

// A replay redactor may remove object keys (also within nested arrays), but
// cannot replace values, add fields, reorder arrays or synthesize a response.
func isRedaction(original, redacted any) bool {
	if values, ok := original.(Values); ok {
		original = map[string]any(values)
	}
	switch after := redacted.(type) {
	case map[string]any:
		before, ok := original.(map[string]any)
		if !ok {
			return false
		}
		for name, value := range after {
			old, found := before[name]
			if !found || !isRedaction(old, value) {
				return false
			}
		}
		return true
	case Values:
		return isRedaction(original, map[string]any(after))
	case []any:
		before, ok := original.([]any)
		if !ok || len(before) != len(after) {
			return false
		}
		for i, value := range after {
			if !isRedaction(before[i], value) {
				return false
			}
		}
		return true
	default:
		before, err1 := json.Marshal(original)
		afterJSON, err2 := json.Marshal(redacted)
		return err1 == nil && err2 == nil && bytes.Equal(before, afterJSON)
	}
}
