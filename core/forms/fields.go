// Package forms provides immutable bound forms, typed cleaning and accessible widgets.
package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Kind string

const (
	Char                Kind = "char"
	Boolean             Kind = "boolean"
	NullBoolean         Kind = "null_boolean"
	ChoiceKind          Kind = "choice"
	TypedChoice         Kind = "typed_choice"
	MultipleChoice      Kind = "multiple_choice"
	TypedMultipleChoice Kind = "typed_multiple_choice"
	Integer             Kind = "integer"
	Float               Kind = "float"
	Decimal             Kind = "decimal"
	Date                Kind = "date"
	DateTime            Kind = "datetime"
	Time                Kind = "time"
	Duration            Kind = "duration"
	Email               Kind = "email"
	URL                 Kind = "url"
	UUID                Kind = "uuid"
	Slug                Kind = "slug"
	IP                  Kind = "ip"
	Regex               Kind = "regex"
	JSON                Kind = "json"
	File                Kind = "file"
	Image               Kind = "image"
	FilePath            Kind = "file_path"
	ModelChoice         Kind = "model_choice"
	ModelMultipleChoice Kind = "model_multiple_choice"
	MultiValue          Kind = "multi_value"
	Combo               Kind = "combo"
	SplitDateTime       Kind = "split_datetime"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e Error) Error() string { return e.Message }

type ErrorList []Error
type ErrorDict map[string]ErrorList
type Choice struct {
	Value string
	Label string
}
type Validator func(context.Context, any) error
type Coercer func(string) (any, error)

// Field is a declaration. Use NewField for required-by-default Django form semantics.
// A raw Field value intentionally leaves Required false.
type Field struct {
	Name     string
	Kind     Kind
	Required bool
	Disabled bool
	// Strip controls whitespace trimming for Char fields. Nil preserves the
	// default of trimming; false preserves significant whitespace, e.g. passwords.
	Strip         *bool
	Initial       any
	Label         string
	HelpText      string
	MinLength     int
	MaxLength     int
	MinValue      *float64
	MaxValue      *float64
	MaxDigits     int
	DecimalPlaces int
	Choices       []Choice
	Validators    []Validator
	Clean         func(context.Context, any) (any, error)
	Coerce        Coercer
	Pattern       *regexp.Regexp
	InputFormats  []string
	Widget        Widget
	ErrorMessages map[string]string
	Fields        []Field
	Compress      func([]any) (any, error)
	// Resolve must authorize every submitted relation ID in the current actor scope.
	Resolve  func(context.Context, []string) ([]any, error)
	MaxBytes int64
}

func NewField(name string, kind Kind) Field { return Field{Name: name, Kind: kind, Required: true} }
func (f Field) failure(code, fallback string) error {
	if message, ok := f.ErrorMessages[code]; ok {
		fallback = message
	}
	return Error{Code: code, Message: fallback}
}
func (f Field) clean(ctx context.Context, raw any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v, err := f.toValue(ctx, raw)
	if err != nil {
		return nil, err
	}
	if f.Required && (empty(v) || (f.Kind == Boolean && v == false)) {
		return nil, f.failure("required", "This field is required.")
	}
	if !empty(v) {
		if s, ok := v.(string); ok {
			n := utf8.RuneCountInString(s)
			if f.MinLength > 0 && n < f.MinLength {
				return nil, f.failure("min_length", "This value is too short.")
			}
			if f.MaxLength > 0 && n > f.MaxLength {
				return nil, f.failure("max_length", "This value is too long.")
			}
		}
		if n, ok := numeric(v); ok {
			if f.MinValue != nil && n < *f.MinValue {
				return nil, f.failure("min_value", "This value is too small.")
			}
			if f.MaxValue != nil && n > *f.MaxValue {
				return nil, f.failure("max_value", "This value is too large.")
			}
		}
		var all ErrorList
		for _, validate := range f.Validators {
			if err := validate(ctx, v); err != nil {
				all = append(all, errorValue(err))
			}
		}
		if len(all) > 0 {
			return nil, validationErrors{all}
		}
	}
	if f.Clean != nil {
		return f.Clean(ctx, v)
	}
	return v, nil
}

func (f Field) toValue(ctx context.Context, raw any) (any, error) {
	s := stringValue(raw)
	invalid := func() (any, error) { return nil, f.failure("invalid", "Enter a valid value.") }
	if f.Kind == Boolean {
		if b, ok := raw.(bool); ok {
			return b, nil
		}
		return !(raw == nil || s == "" || strings.EqualFold(s, "false") || s == "0"), nil
	}
	if f.Kind == NullBoolean {
		switch strings.ToLower(s) {
		case "true", "1", "2":
			return true, nil
		case "false", "0", "3":
			return false, nil
		default:
			return nil, nil
		}
	}
	if f.Kind == MultiValue || f.Kind == Combo || f.Kind == SplitDateTime {
		return f.cleanComposite(ctx, raw)
	}
	if f.Kind == File || f.Kind == Image {
		return f.cleanFile(raw)
	}
	if f.Kind == MultipleChoice || f.Kind == TypedMultipleChoice || f.Kind == ModelMultipleChoice {
		values := stringValues(raw)
		if values == nil {
			values = []string{}
		}
		if f.Kind == ModelMultipleChoice {
			values = uniqueStrings(values)
			if len(values) == 0 {
				return []any{}, nil
			}
			if f.Resolve == nil {
				return nil, f.failure("invalid_choice", "Select a valid choice.")
			}
			resolved, err := f.Resolve(ctx, values)
			if err != nil {
				return nil, err
			}
			if len(resolved) != len(values) {
				return nil, f.failure("invalid_choice", "Select valid choices.")
			}
			return resolved, nil
		}
		result := make([]any, 0, len(values))
		for _, x := range values {
			if !f.hasChoice(x) {
				return nil, f.failure("invalid_choice", "Select a valid choice.")
			}
			var v any = x
			if f.Kind == TypedMultipleChoice && f.Coerce != nil {
				var err error
				v, err = f.Coerce(x)
				if err != nil {
					return invalid()
				}
			}
			result = append(result, v)
		}
		return result, nil
	}
	if raw == nil || s == "" {
		switch f.Kind {
		case Char, Email, URL, Slug, Regex, FilePath:
			return "", nil
		default:
			return nil, nil
		}
	}
	switch f.Kind {
	case Char, FilePath:
		if f.Kind == Char && f.Strip != nil && !*f.Strip {
			return s, nil
		}
		return strings.TrimSpace(s), nil
	case Integer:
		x, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return invalid()
		}
		return x, nil
	case Float:
		x, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(x) || math.IsInf(x, 0) {
			return invalid()
		}
		return x, nil
	case Decimal:
		if !decimalPattern.MatchString(s) {
			return invalid()
		}
		x, ok := new(big.Rat).SetString(s)
		if !ok {
			return invalid()
		}
		_ = x
		parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+"), ".")
		places := 0
		if len(parts) == 2 {
			places = len(parts[1])
		}
		digits := len(strings.TrimLeft(parts[0], "0")) + places
		if digits == 0 {
			digits = 1
		}
		if f.MaxDigits > 0 && digits > f.MaxDigits {
			return nil, f.failure("max_digits", "This value has too many digits.")
		}
		if f.DecimalPlaces >= 0 && places > f.DecimalPlaces {
			return nil, f.failure("max_decimal_places", "This value has too many decimal places.")
		}
		return s, nil
	case Date, DateTime, Time:
		layouts := f.InputFormats
		if len(layouts) == 0 {
			switch f.Kind {
			case Date:
				layouts = []string{"2006-01-02"}
			case Time:
				layouts = []string{"15:04:05.999999999", "15:04"}
			case DateTime:
				layouts = []string{time.RFC3339Nano, "2006-01-02T15:04", "2006-01-02 15:04:05"}
			}
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, s); err == nil {
				return t, nil
			}
		}
		return invalid()
	case Duration:
		d, err := time.ParseDuration(s)
		if err != nil {
			return invalid()
		}
		return d, nil
	case Email:
		s = strings.TrimSpace(s)
		a, err := mail.ParseAddress(s)
		if err != nil || a.Address != s || !strings.Contains(s, "@") {
			return invalid()
		}
		return s, nil
	case URL:
		s = strings.TrimSpace(s)
		u, err := url.Parse(s)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ftp" && u.Scheme != "ftps") {
			return invalid()
		}
		return s, nil
	case UUID:
		if !uuidPattern.MatchString(s) {
			return invalid()
		}
		return strings.ToLower(s), nil
	case Slug:
		if !slugPattern.MatchString(s) {
			return invalid()
		}
		return s, nil
	case IP:
		x := net.ParseIP(s)
		if x == nil {
			return invalid()
		}
		return x.String(), nil
	case Regex:
		if f.Pattern == nil || !f.Pattern.MatchString(s) {
			return invalid()
		}
		return s, nil
	case JSON:
		if _, ok := raw.(string); !ok {
			encoded, err := json.Marshal(raw)
			if err != nil {
				return invalid()
			}
			s = string(encoded)
		}
		if len(s) > 1<<20 {
			return nil, f.failure("max_length", "This value is too large.")
		}
		var v any
		decoder := json.NewDecoder(strings.NewReader(s))
		decoder.UseNumber()
		if err := decoder.Decode(&v); err != nil || !json.Valid([]byte(s)) {
			return invalid()
		}
		return v, nil
	case ChoiceKind, TypedChoice:
		if !f.hasChoice(s) {
			return nil, f.failure("invalid_choice", "Select a valid choice.")
		}
		if f.Kind == TypedChoice && f.Coerce != nil {
			return f.Coerce(s)
		}
		return s, nil
	case ModelChoice:
		if f.Resolve == nil {
			return nil, f.failure("invalid_choice", "Select a valid choice.")
		}
		values, err := f.Resolve(ctx, []string{s})
		if err != nil {
			return nil, err
		}
		if len(values) != 1 {
			return nil, f.failure("invalid_choice", "Select a valid choice.")
		}
		return values[0], nil
	default:
		return invalid()
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func (f Field) hasChoice(s string) bool {
	for _, c := range f.Choices {
		if c.Value == s {
			return true
		}
	}
	return false
}
func (f Field) cleanComposite(ctx context.Context, raw any) (any, error) {
	if f.Kind == Combo {
		v := raw
		for _, part := range f.Fields {
			var err error
			v, err = part.clean(ctx, v)
			if err != nil {
				return nil, err
			}
		}
		return v, nil
	}
	parts := stringValues(raw)
	if value, ok := raw.(time.Time); ok && f.Kind == SplitDateTime {
		parts = []string{value.Format("2006-01-02"), value.Format("15:04:05.999999999")}
	}
	fields := f.Fields
	if f.Kind == SplitDateTime && len(fields) == 0 {
		fields = []Field{NewField("date", Date), NewField("time", Time)}
	}
	allEmpty := true
	for _, part := range parts {
		if part != "" {
			allEmpty = false
			break
		}
	}
	if len(parts) == 0 || allEmpty {
		return nil, nil
	}
	values := make([]any, len(fields))
	var errs ErrorList
	for i, part := range fields {
		var value any
		if i < len(parts) {
			value = parts[i]
		}
		v, err := part.clean(ctx, value)
		if err != nil {
			errs = append(errs, errorValue(err))
		}
		values[i] = v
	}
	if len(errs) > 0 {
		return nil, validationErrors{errs}
	}
	if f.Compress != nil {
		return f.Compress(values)
	}
	if f.Kind == SplitDateTime {
		a, ok := values[0].(time.Time)
		if !ok {
			return nil, f.failure("invalid", "Enter a valid date and time.")
		}
		b, ok := values[1].(time.Time)
		if !ok {
			return nil, f.failure("invalid", "Enter a valid date and time.")
		}
		return time.Date(a.Year(), a.Month(), a.Day(), b.Hour(), b.Minute(), b.Second(), b.Nanosecond(), a.Location()), nil
	}
	return values, nil
}

var decimalPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var slugPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type validationErrors struct{ values ErrorList }

func (e validationErrors) Error() string { return "Validation failed." }
func errorValue(err error) Error {
	var e Error
	if errors.As(err, &e) {
		return e
	}
	return Error{"invalid", "Enter a valid value."}
}
func stringValue(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if s, ok := v.([]string); ok && len(s) > 0 {
		return s[len(s)-1]
	}
	return fmt.Sprint(v)
}
func stringValues(v any) []string {
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		r := make([]string, len(x))
		for i, v := range x {
			r[i] = stringValue(v)
		}
		return r
	case nil:
		return nil
	default:
		return []string{stringValue(v)}
	}
}
func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []string:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}
func numeric(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}
